package registryproxy

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Backend serves manifests, blobs, and referrers for one upstream registry.
type Backend interface {
	GetManifest(ctx context.Context, registry, repo, ref string) (Manifest, error)
	GetBlob(ctx context.Context, registry, repo, digest, rangeHeader string) (io.ReadCloser, int64, string, error)
	Referrers(ctx context.Context, registry, repo, digest string) ([]byte, error)
}

// backendTimeout bounds the upstream fetch after the client request is
// detached from it.
const backendTimeout = 2 * time.Minute

// ErrConverting tells the client that the mirror copy/conversion job is still
// running. The server answers 503 with Retry-After; a later retry sees the
// nydus index.
var ErrConverting = errors.New("registry mirror conversion in progress")

// conversionRetryDelay throttles automatic retries after a failed conversion
// while still serving the source OCI manifest.
const conversionRetryDelay = time.Minute

// backendContext detaches the upstream call from the client request. dfdaemon
// cancels cold pulls early; the Harbor fetch should still complete and warm
// the cache instead of returning "context canceled".
func backendContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), backendTimeout)
}

// HarborBackend reads the writable mirror first and falls back to the Harbor
// proxy project (source cache). It never writes from the read path.
type HarborBackend struct {
	Source        Registry
	Mirror        Registry
	MirrorProject string
	Provisioner   *Provisioner
	Jobs          *Manager
}

func (b *HarborBackend) ensure(ctx context.Context, registry string) error {
	if b.Provisioner != nil {
		return b.Provisioner.Ensure(ctx, registry)
	}
	return nil
}

func sourceRepo(registry, repo string) string {
	return ProjectName(registry) + "/" + strings.TrimPrefix(repo, "/")
}

func (b *HarborBackend) mirrorRepo(registry, repo string) string {
	return b.MirrorProject + "/" + strings.TrimSuffix(registry, "/") + "/" + strings.TrimPrefix(repo, "/")
}

func (b *HarborBackend) GetManifest(ctx context.Context, registry, repo, ref string) (Manifest, error) {
	if err := b.ensure(ctx, registry); err != nil {
		return Manifest{}, err
	}
	if b.Mirror != nil {
		if manifest, err := b.Mirror.GetManifest(ctx, b.mirrorRepo(registry, repo), ref); err == nil {
			return manifest, nil
		}
	}
	manifest, err := b.Source.GetManifest(ctx, sourceRepo(registry, repo), ref)
	if err != nil {
		return Manifest{}, err
	}
	// Copy-on-read: the first pull of a tag starts the mirror copy and nydus
	// conversion. While it runs, answer 503/Retry-After instead of returning a
	// plain OCI manifest that nydus would skip. Digest refs cannot get a nydus
	// index under the same reference, so they keep the source OCI manifest.
	if b.Jobs != nil && !strings.HasPrefix(ref, "sha256:") {
		job := Job{
			Registry:   registry,
			Repository: repo,
			Reference:  ref,
			Tag:        ref,
			Digest:     manifest.Digest,
			Trigger:    "pull",
		}
		if state, ok := b.Jobs.State(job.Key()); ok {
			switch state.State {
			case "failed":
				// Conversion already failed: serve the original OCI manifest
				// and retry in the background at most once per cooldown.
				if time.Since(state.UpdatedAt) >= conversionRetryDelay {
					b.Jobs.Enqueue(job)
				}
				return manifest, nil
			case "pending", "converting":
				return Manifest{}, ErrConverting
			}
		}
		if b.Jobs.Enqueue(job) {
			return Manifest{}, ErrConverting
		}
	}
	return manifest, nil
}

func (b *HarborBackend) GetBlob(ctx context.Context, registry, repo, digest, rangeHeader string) (io.ReadCloser, int64, string, error) {
	if err := b.ensure(ctx, registry); err != nil {
		return nil, 0, "", err
	}
	if b.Mirror != nil {
		body, size, contentRange, err := b.Mirror.GetBlob(ctx, b.mirrorRepo(registry, repo), digest, rangeHeader)
		if err == nil {
			return body, size, contentRange, nil
		}
		if body != nil {
			body.Close()
		}
	}
	return b.Source.GetBlob(ctx, sourceRepo(registry, repo), digest, rangeHeader)
}

func (b *HarborBackend) Referrers(ctx context.Context, registry, repo, digest string) ([]byte, error) {
	if err := b.ensure(ctx, registry); err != nil {
		return nil, err
	}
	if b.Mirror != nil {
		if refs, err := b.Mirror.Referrers(ctx, b.mirrorRepo(registry, repo), digest); err == nil {
			return refs, nil
		}
	}
	return b.Source.Referrers(ctx, sourceRepo(registry, repo), digest)
}

// Server exposes the read-only OCI API, health/readiness, metrics, and the
// Harbor webhook handler.
type Server struct {
	Config  *Config
	Backend Backend
	Convert http.Handler
	Manager *Manager
	// AuthUsername/AuthPassword enable optional Basic auth on /v2 reads.
	// Empty values mean network-only (no client auth).
	AuthUsername string
	AuthPassword string
	requests     atomic.Uint64
	errors       atomic.Uint64
}

func (s *Server) Handler() http.Handler {
	handler := http.HandlerFunc(s.serve)
	if s.AuthUsername == "" && s.AuthPassword == "" {
		return handler
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath := stripBasePath(r.URL.Path, s.Config.BasePath())
		// Health probes stay open; /api/convert has its own Bearer secret.
		if strings.HasPrefix(requestPath, "/v2") {
			user, password, ok := r.BasicAuth()
			if !ok || !equalConstantTime(user, s.AuthUsername) || !equalConstantTime(password, s.AuthPassword) {
				w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		handler.ServeHTTP(w, r)
	})
}

func equalConstantTime(value, expected string) bool {
	return subtle.ConstantTimeCompare([]byte(value), []byte(expected)) == 1
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.requests.Add(1)
	requestPath := stripBasePath(r.URL.Path, s.Config.BasePath())
	switch {
	case requestPath == "/healthz":
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
		return
	case requestPath == "/readyz":
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
		return
	case requestPath == "/metrics":
		s.metrics(w)
		return
	case requestPath == "/api/convert":
		if s.Convert == nil {
			http.NotFound(w, r)
			return
		}
		s.Convert.ServeHTTP(w, r)
		return
	}

	registry, rest, ok := s.route(requestPath, r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if rest == "/v2/" || rest == "/v2" {
		w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
		w.WriteHeader(http.StatusOK)
		return
	}
	if registry == "" {
		http.Error(w, "registry hint is required", http.StatusForbidden)
		return
	}
	if !s.Config.Allowlisted(registry) {
		s.errors.Add(1)
		http.Error(w, "registry is not allowlisted", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		s.errors.Add(1)
		http.Error(w, "read-only registry", http.StatusMethodNotAllowed)
		return
	}
	if !strings.HasPrefix(rest, "/v2/") {
		http.NotFound(w, r)
		return
	}
	repo, kind, ref, ok := splitOCIPath(strings.TrimPrefix(rest, "/v2/"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	switch kind {
	case "manifests":
		s.manifest(w, r, registry, repo, ref)
	case "blobs":
		s.blob(w, r, registry, repo, ref)
	case "referrers":
		s.referrers(w, r, registry, repo, ref)
	default:
		http.NotFound(w, r)
	}
}

func stripBasePath(path, base string) string {
	if base == "" || base == "/" {
		return path
	}
	if path == base {
		return "/"
	}
	if strings.HasPrefix(path, base+"/") {
		return strings.TrimPrefix(path, base)
	}
	return path
}

func (s *Server) route(requestPath string, r *http.Request) (string, string, bool) {
	rest, ok := ociRest(requestPath)
	if !ok {
		return "", "", false
	}
	// ?ns is the dfdaemon/containerd hint: the whole /v2 path is the
	// repository. A hint naming the proxy itself is not an upstream; fall
	// through to the direct-ref route instead of rejecting the pull.
	if registry := queryRegistry(r); registry != "" && !s.selfRegistry(registry) {
		return registry, rest, true
	}
	trimmed := strings.Trim(requestPath, "/")
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) == 2 && (parts[1] == "v2" || strings.HasPrefix(parts[1], "v2/")) {
		return normalizeRegistryAlias(parts[0]), rest, true
	}
	if registry := headerRegistry(r); registry != "" && !s.selfRegistry(registry) {
		return registry, rest, true
	}
	// Direct ref: /v2/<registry>/<repo>/... (admission-rewritten image). Only
	// a host-looking first segment counts as a registry; anything else stays
	// unresolved and gets 403, never a guessed Harbor project. This check is
	// independent of the allowlist ("*" allows any explicit registry).
	if repoPath, ok := strings.CutPrefix(rest, "/v2/"); ok {
		first, remaining, found := strings.Cut(repoPath, "/")
		registry := normalizeRegistryAlias(first)
		if found && looksLikeRegistry(registry) && s.Config.Allowlisted(registry) {
			return registry, "/v2/" + remaining, true
		}
	}
	return "", rest, true
}

func (s *Server) selfRegistry(registry string) bool {
	return s.Config != nil && s.Config.SelfRegistry(registry)
}

// looksLikeRegistry matches Docker's implicit-registry rule: an explicit
// registry segment contains a dot or port, or is localhost. This keeps
// unqualified repository paths from being guessed as registries when the
// allowlist is "*".
func looksLikeRegistry(registry string) bool {
	if registry == "" {
		return false
	}
	return registry == "localhost" || strings.ContainsAny(registry, ".:")
}

func ociRest(raw string) (string, bool) {
	trimmed := strings.Trim(raw, "/")
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) == 2 && (parts[1] == "v2" || strings.HasPrefix(parts[1], "v2/")) {
		return "/" + parts[1], true
	}
	if len(parts) == 1 && parts[0] == "v2" {
		return "/v2/", true
	}
	if strings.HasPrefix(raw, "/v2/") {
		return raw, true
	}
	return "", false
}

func queryRegistry(r *http.Request) string {
	return normalizeRegistry(r.URL.Query().Get("ns"))
}

func headerRegistry(r *http.Request) string {
	return normalizeRegistry(r.Header.Get("X-Dragonfly-Registry"))
}

func normalizeRegistry(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if parsed, err := url.Parse(raw); err == nil && parsed.Host != "" {
		if pathPart := strings.Trim(parsed.Path, "/"); pathPart != "" {
			raw = pathPart
		} else {
			raw = parsed.Host
		}
	}
	raw = strings.Trim(raw, "/")
	if raw == "" {
		return ""
	}
	return normalizeRegistryAlias(path.Base(raw))
}

// normalizeRegistryAlias canonicalizes registry host aliases before the
// allowlist and Harbor project mapping. Docker clients use registry-1 and
// index hostnames for the same docker.io content.
func normalizeRegistryAlias(registry string) string {
	registry = strings.ToLower(strings.TrimSpace(registry))
	registry = strings.Trim(registry, "/")
	switch registry {
	case "registry-1.docker.io", "index.docker.io":
		return "docker.io"
	default:
		return registry
	}
}

func splitOCIPath(value string) (repo, kind, ref string, ok bool) {
	for _, marker := range []string{"/manifests/", "/blobs/", "/referrers/"} {
		if idx := strings.LastIndex(value, marker); idx > 0 {
			kind = strings.TrimPrefix(marker, "/")
			kind = strings.TrimSuffix(kind, "/")
			repo = value[:idx]
			ref = value[idx+len(marker):]
			return repo, kind, ref, repo != "" && ref != ""
		}
	}
	return "", "", "", false
}

func (s *Server) manifest(w http.ResponseWriter, r *http.Request, registry, repo, ref string) {
	ctx, cancel := backendContext(r.Context())
	defer cancel()
	manifest, err := s.Backend.GetManifest(ctx, registry, repo, ref)
	if err != nil {
		s.writeError(w, err)
		return
	}
	mediaType := manifest.MediaType
	if mediaType == "" {
		mediaType = "application/vnd.oci.image.manifest.v1+json"
	}
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Docker-Content-Digest", manifest.Digest)
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(manifest.Body)))
		return
	}
	_, _ = w.Write(manifest.Body)
}

func (s *Server) blob(w http.ResponseWriter, r *http.Request, registry, repo, digest string) {
	ctx, cancel := backendContext(r.Context())
	defer cancel()
	body, size, contentRange, err := s.Backend.GetBlob(ctx, registry, repo, digest, r.Header.Get("Range"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	defer body.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Docker-Content-Digest", digest)
	if size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	status := http.StatusOK
	if contentRange != "" {
		w.Header().Set("Content-Range", contentRange)
		status = http.StatusPartialContent
	}
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.Copy(w, body); err != nil {
		s.errors.Add(1)
	}
}

func (s *Server) referrers(w http.ResponseWriter, r *http.Request, registry, repo, digest string) {
	ctx, cancel := backendContext(r.Context())
	defer cancel()
	body, err := s.Backend.Referrers(ctx, registry, repo, digest)
	if err != nil {
		s.writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
	_, _ = w.Write(body)
}

func (s *Server) writeError(w http.ResponseWriter, err error) {
	s.errors.Add(1)
	if errors.Is(err, ErrConverting) {
		w.Header().Set("Retry-After", "5")
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if IsNotFound(err) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	http.Error(w, err.Error(), http.StatusServiceUnavailable)
}

func (s *Server) metrics(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintf(w, "# HELP registry_proxy_requests_total Read API requests.\n")
	fmt.Fprintf(w, "# TYPE registry_proxy_requests_total counter\n")
	fmt.Fprintf(w, "registry_proxy_requests_total %d\n", s.requests.Load())
	fmt.Fprintf(w, "# HELP registry_proxy_errors_total Read API errors.\n")
	fmt.Fprintf(w, "# TYPE registry_proxy_errors_total counter\n")
	fmt.Fprintf(w, "registry_proxy_errors_total %d\n", s.errors.Load())
	if s.Manager != nil {
		fmt.Fprintf(w, "registry_proxy_jobs_pending %d\n", s.Manager.Pending())
		enqueued, succeeded, failed := s.Manager.Metrics()
		fmt.Fprintf(w, "registry_proxy_jobs_enqueued_total %d\n", enqueued)
		fmt.Fprintf(w, "registry_proxy_jobs_succeeded_total %d\n", succeeded)
		fmt.Fprintf(w, "registry_proxy_jobs_failed_total %d\n", failed)
	}
}

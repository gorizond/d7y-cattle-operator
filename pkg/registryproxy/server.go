package registryproxy

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync/atomic"
	"time"
)

// Backend serves manifests, blobs, and referrers for one upstream registry.
type Backend interface {
	GetManifest(ctx context.Context, registry, repo, ref string) (Manifest, error)
	GetBlob(ctx context.Context, registry, repo, digest string) ([]byte, error)
	Referrers(ctx context.Context, registry, repo, digest string) ([]byte, error)
}

// HarborBackend reads the writable mirror first and falls back to the Harbor
// proxy project (source cache). It never writes from the read path.
type HarborBackend struct {
	Source        Registry
	Mirror        Registry
	MirrorProject string
	Provisioner   *Provisioner
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
	return b.Source.GetManifest(ctx, sourceRepo(registry, repo), ref)
}

func (b *HarborBackend) GetBlob(ctx context.Context, registry, repo, digest string) ([]byte, error) {
	if err := b.ensure(ctx, registry); err != nil {
		return nil, err
	}
	if b.Mirror != nil {
		if blob, err := b.Mirror.GetBlob(ctx, b.mirrorRepo(registry, repo), digest); err == nil {
			return blob, nil
		}
	}
	return b.Source.GetBlob(ctx, sourceRepo(registry, repo), digest)
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
	Config   *Config
	Backend  Backend
	Convert  http.Handler
	Manager  *Manager
	requests atomic.Uint64
	errors   atomic.Uint64
}

func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.serve)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.requests.Add(1)
	switch {
	case r.URL.Path == "/healthz":
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
		return
	case r.URL.Path == "/readyz":
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
		return
	case r.URL.Path == "/metrics":
		s.metrics(w)
		return
	case r.URL.Path == "/api/convert":
		if s.Convert == nil {
			http.NotFound(w, r)
			return
		}
		s.Convert.ServeHTTP(w, r)
		return
	}

	registry, rest, ok := s.route(r)
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

func (s *Server) route(r *http.Request) (string, string, bool) {
	trimmed := strings.Trim(r.URL.Path, "/")
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) == 2 && parts[1] == "v2" || (len(parts) == 2 && strings.HasPrefix(parts[1], "v2/")) {
		return parts[0], "/" + parts[1], true
	}
	if len(parts) == 1 && parts[0] == "v2" {
		return headerRegistry(r), "/v2/", true
	}
	if strings.HasPrefix(r.URL.Path, "/v2/") {
		return headerRegistry(r), r.URL.Path, true
	}
	return "", "", false
}

func headerRegistry(r *http.Request) string {
	raw := strings.TrimSpace(r.Header.Get("X-Dragonfly-Registry"))
	if raw == "" {
		return ""
	}
	if parsed, err := url.Parse(raw); err == nil && parsed.Host != "" {
		raw = strings.Trim(parsed.Path, "/")
	}
	if raw == "" {
		return ""
	}
	return path.Base(raw)
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
	manifest, err := s.Backend.GetManifest(r.Context(), registry, repo, ref)
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
	body, err := s.Backend.GetBlob(r.Context(), registry, repo, digest)
	if err != nil {
		s.writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Docker-Content-Digest", digest)
	http.ServeContent(w, r, digest, time.Time{}, bytes.NewReader(body))
}

func (s *Server) referrers(w http.ResponseWriter, r *http.Request, registry, repo, digest string) {
	body, err := s.Backend.Referrers(r.Context(), registry, repo, digest)
	if err != nil {
		s.writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
	_, _ = w.Write(body)
}

func (s *Server) writeError(w http.ResponseWriter, err error) {
	s.errors.Add(1)
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
	}
}

package registryproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

type fakeBackend struct {
	manifest Manifest
	blob     []byte
	refs     []byte
}

func (f *fakeBackend) GetManifest(context.Context, string, string, string) (Manifest, error) {
	return f.manifest, nil
}
func (f *fakeBackend) GetBlob(_ context.Context, _, _, _, rangeHeader string) (io.ReadCloser, int64, string, error) {
	if rangeHeader == "" {
		return io.NopCloser(bytes.NewReader(f.blob)), int64(len(f.blob)), "", nil
	}
	var start, end int
	if _, err := fmt.Sscanf(rangeHeader, "bytes=%d-%d", &start, &end); err != nil ||
		start < 0 || end < start || end >= len(f.blob) {
		return nil, 0, "", &RegistryError{Status: http.StatusRequestedRangeNotSatisfiable, Op: "get blob"}
	}
	part := f.blob[start : end+1]
	return io.NopCloser(bytes.NewReader(part)), int64(len(part)),
		fmt.Sprintf("bytes %d-%d/%d", start, end, len(f.blob)), nil
}
func (f *fakeBackend) Referrers(context.Context, string, string, string) ([]byte, error) {
	return f.refs, nil
}

func testServer(backend Backend) *Server {
	return &Server{
		Config: &Config{Registry: struct {
			Allowlist []string `yaml:"allowlist"`
		}{Allowlist: []string{"docker.io"}}},
		Backend: backend,
	}
}

func TestServerOCIRead(t *testing.T) {
	backend := &fakeBackend{
		manifest: Manifest{Digest: "sha256:manifest", MediaType: "application/vnd.oci.image.manifest.v1+json", Body: []byte("manifest")},
		blob:     []byte("blob-data"),
		refs:     []byte(`{"schemaVersion":2,"manifests":[]}`),
	}
	server := testServer(backend)

	req := httptest.NewRequest(http.MethodGet, "/docker.io/v2/library/nginx/manifests/1.27", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "manifest" {
		t.Fatalf("manifest response = %d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Docker-Content-Digest") != "sha256:manifest" {
		t.Error("digest header missing")
	}

	req = httptest.NewRequest(http.MethodHead, "/docker.io/v2/library/nginx/manifests/1.27", nil)
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("HEAD response = %d body=%q", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v2/library/nginx/manifests/1.27", nil)
	req.Header.Set("X-Dragonfly-Registry", "https://proxy.example.com/docker.io")
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("header registry response = %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/docker.io/v2/library/nginx/blobs/sha256:blob", nil)
	req.Header.Set("Range", "bytes=0-2")
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "blo" {
		t.Errorf("range response = %d %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes 0-2/9" {
		t.Errorf("Content-Range = %q", got)
	}
	if got := rec.Header().Get("Content-Length"); got != "3" {
		t.Errorf("Content-Length = %q", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/docker.io/v2/library/nginx/referrers/sha256:manifest", nil)
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "schemaVersion") {
		t.Errorf("referrers response = %d %q", rec.Code, rec.Body.String())
	}
}

func TestServerQueryNSRegistry(t *testing.T) {
	backend := &fakeBackend{
		manifest: Manifest{Digest: "sha256:manifest", MediaType: "application/vnd.oci.image.manifest.v1+json", Body: []byte("manifest")},
	}
	server := testServer(backend)

	for _, path := range []string{
		"/v2/library/nginx/manifests/1.27?ns=docker.io",
		"/v2/library/nginx/manifests/1.27?ns=https://docker.io",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || rec.Body.String() != "manifest" {
			t.Errorf("%s response = %d %q", path, rec.Code, rec.Body.String())
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/v2/library/nginx/manifests/1.27?ns=ghcr.io", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("non-allowlisted ?ns response = %d, want 403", rec.Code)
	}
}

func TestNormalizeRegistryDockerAliases(t *testing.T) {
	for input, want := range map[string]string{
		"registry-1.docker.io":         "docker.io",
		"index.docker.io":              "docker.io",
		"https://registry-1.docker.io": "docker.io",
		"https://index.docker.io/":     "docker.io",
		"ghcr.io":                      "ghcr.io",
		"  Registry-1.Docker.IO  ":     "docker.io",
		"":                             "",
	} {
		if got := normalizeRegistry(input); got != want {
			t.Errorf("normalizeRegistry(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestServerDockerAliasRoute(t *testing.T) {
	backend := &fakeBackend{
		manifest: Manifest{Digest: "sha256:manifest", MediaType: "application/vnd.oci.image.manifest.v1+json", Body: []byte("manifest")},
	}
	server := testServer(backend)

	for _, target := range []string{
		"/registry-1.docker.io/v2/library/nginx/manifests/1.27",
		"/index.docker.io/v2/library/nginx/manifests/1.27",
		"/registry-1.docker.io/v2/library/nginx/manifests/1.27?ns=https://index.docker.io",
	} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || rec.Body.String() != "manifest" {
			t.Errorf("%s response = %d %q", target, rec.Code, rec.Body.String())
		}
	}

	registry, rest, ok := server.route("/registry-1.docker.io/v2/library/nginx/manifests/1.27", httptest.NewRequest(http.MethodGet, "/", nil))
	if !ok || registry != "docker.io" || rest != "/v2/library/nginx/manifests/1.27" {
		t.Errorf("route() = %q, %q, %t", registry, rest, ok)
	}
}

func TestServerRegistryAllowlistAndPushRejected(t *testing.T) {
	server := testServer(&fakeBackend{})
	req := httptest.NewRequest(http.MethodGet, "/ghcr.io/v2/library/nginx/manifests/1.27", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("unknown registry response = %d, want 403", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPut, "/docker.io/v2/library/nginx/manifests/1.27", nil)
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("push response = %d, want 405", rec.Code)
	}
}

func TestServerHealthMetricsAndConvert(t *testing.T) {
	server := testServer(&fakeBackend{})
	server.Manager = NewManager(nil)
	server.Convert = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	for _, path := range []string{"/healthz", "/readyz"} {
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d", path, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, metric := range []string{
		"registry_proxy_requests_total",
		"registry_proxy_jobs_enqueued_total",
		"registry_proxy_jobs_succeeded_total",
		"registry_proxy_jobs_failed_total",
	} {
		if !strings.Contains(rec.Body.String(), metric) {
			t.Errorf("metrics missing %s: %q", metric, rec.Body.String())
		}
	}
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/convert", nil))
	if rec.Code != http.StatusAccepted {
		t.Errorf("convert = %d", rec.Code)
	}
}

func TestServerProxyBasePath(t *testing.T) {
	server := testServer(&fakeBackend{manifest: Manifest{Digest: "sha256:manifest", MediaType: ociManifestType, Body: []byte("manifest")}})
	server.Config.Proxy.BasePath = "/proxy"
	for _, target := range []string{
		"/proxy/docker.io/v2/library/nginx/manifests/1.27",
		"/proxy/v2/library/nginx/manifests/1.27?ns=docker.io",
		"/docker.io/v2/library/nginx/manifests/1.27",
	} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200", target, rec.Code)
		}
	}
	server.Convert = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) })
	req := httptest.NewRequest(http.MethodPost, "/proxy/api/convert", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Errorf("/proxy/api/convert = %d, want 202", rec.Code)
	}
}

func TestServerV2Root(t *testing.T) {
	server := testServer(&fakeBackend{})
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v2/", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Docker-Distribution-Api-Version") != "registry/2.0" {
		t.Errorf("/v2/ = %d %q", rec.Code, rec.Header().Get("Docker-Distribution-Api-Version"))
	}
}

// cancelProbeBackend blocks until the test releases it and records the
// context error seen by the backend call.
type cancelProbeBackend struct {
	started chan struct{}
	release chan struct{}
	ctxErr  error
}

func (b *cancelProbeBackend) wait(ctx context.Context) {
	close(b.started)
	<-b.release
	b.ctxErr = ctx.Err()
}

func (b *cancelProbeBackend) GetManifest(ctx context.Context, _, _, _ string) (Manifest, error) {
	b.wait(ctx)
	return Manifest{Digest: "sha256:manifest", MediaType: ociManifestType, Body: []byte("manifest")}, nil
}

func (b *cancelProbeBackend) GetBlob(ctx context.Context, _, _, _, _ string) (io.ReadCloser, int64, string, error) {
	b.wait(ctx)
	return io.NopCloser(strings.NewReader("blob-data")), 9, "", nil
}

func (b *cancelProbeBackend) Referrers(ctx context.Context, _, _, _ string) ([]byte, error) {
	b.wait(ctx)
	return []byte(`{"schemaVersion":2}`), nil
}

func TestServerDetachesBackendFromClientCancel(t *testing.T) {
	for name, target := range map[string]string{
		"manifest":  "/docker.io/v2/library/nginx/manifests/1.27",
		"blob":      "/docker.io/v2/library/nginx/blobs/sha256:blob",
		"referrers": "/docker.io/v2/library/nginx/referrers/sha256:manifest",
	} {
		t.Run(name, func(t *testing.T) {
			backend := &cancelProbeBackend{started: make(chan struct{}), release: make(chan struct{})}
			server := testServer(backend)
			reqCtx, cancel := context.WithCancel(context.Background())
			req := httptest.NewRequest(http.MethodGet, target, nil).WithContext(reqCtx)
			rec := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				server.Handler().ServeHTTP(rec, req)
				close(done)
			}()

			<-backend.started
			cancel()
			close(backend.release)
			<-done

			if backend.ctxErr != nil {
				t.Errorf("backend context canceled with the client: %v", backend.ctxErr)
			}
			if rec.Code != http.StatusOK {
				t.Errorf("response = %d %q, want 200", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestBackendContextIsBounded(t *testing.T) {
	ctx, cancel := backendContext(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("backend context has no deadline")
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > backendTimeout {
		t.Errorf("backend deadline = %v, want within (0, %v]", remaining, backendTimeout)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

type trackingBody struct {
	r      io.Reader
	closed bool
}

func (b *trackingBody) Read(p []byte) (int, error) { return b.r.Read(p) }
func (b *trackingBody) Close() error {
	b.closed = true
	return nil
}

type streamBackend struct {
	body *trackingBody
	size int64
}

func (b *streamBackend) GetManifest(context.Context, string, string, string) (Manifest, error) {
	return Manifest{}, nil
}
func (b *streamBackend) GetBlob(context.Context, string, string, string, string) (io.ReadCloser, int64, string, error) {
	return b.body, b.size, "", nil
}
func (b *streamBackend) Referrers(context.Context, string, string, string) ([]byte, error) {
	return nil, nil
}

func TestServerStreamsLargeBlobWithContentLength(t *testing.T) {
	const size = 4 << 20
	body := &trackingBody{r: io.LimitReader(zeroReader{}, size)}
	server := testServer(&streamBackend{body: body, size: size})
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/docker.io/v2/library/nginx/blobs/sha256:large", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("response = %d %q", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != size {
		t.Errorf("body length = %d, want %d", rec.Body.Len(), size)
	}
	if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(size) {
		t.Errorf("Content-Length = %q, want %d", got, size)
	}
	if !body.closed {
		t.Error("backend blob stream was not closed")
	}
}

func TestHarborBackendFallsBackToSourceOnMirrorMiss(t *testing.T) {
	source := newFakeRegistry()
	mirror := newFakeRegistry()
	source.blobs["dockerio/library/nginx:sha256:layer"] = []byte("layer-data")
	backend := &HarborBackend{Source: source, Mirror: mirror, MirrorProject: "mirror"}
	body, size, contentRange, err := backend.GetBlob(context.Background(),
		"docker.io", "library/nginx", "sha256:layer", "")
	if err != nil {
		t.Fatalf("GetBlob() error = %v", err)
	}
	defer body.Close()
	data, _ := io.ReadAll(body)
	if string(data) != "layer-data" || size != int64(len(data)) || contentRange != "" {
		t.Errorf("blob = %q size=%d range=%q", data, size, contentRange)
	}
}

func directServer(backend Backend) *Server {
	return &Server{
		Config: &Config{
			Registry: struct {
				Allowlist []string `yaml:"allowlist"`
			}{Allowlist: []string{"docker.io", "registry.gitlab.com", "ghcr.io"}},
			Proxy: struct {
				PublicURL string `yaml:"public_url"`
				BasePath  string `yaml:"base_path"`
			}{PublicURL: "https://proxy.example.com"},
		},
		Backend: backend,
	}
}

type recordingBackend struct {
	registry string
	repo     string
	ref      string
	rangeHdr string
	manifest Manifest
	blob     []byte
}

func (b *recordingBackend) GetManifest(_ context.Context, registry, repo, ref string) (Manifest, error) {
	b.registry, b.repo, b.ref = registry, repo, ref
	if b.manifest.Body == nil {
		b.manifest = Manifest{Digest: "sha256:manifest", MediaType: ociManifestType, Body: []byte("manifest")}
	}
	return b.manifest, nil
}
func (b *recordingBackend) GetBlob(_ context.Context, registry, repo, digest, rangeHeader string) (io.ReadCloser, int64, string, error) {
	b.registry, b.repo, b.ref, b.rangeHdr = registry, repo, digest, rangeHeader
	return io.NopCloser(bytes.NewReader(b.blob)), int64(len(b.blob)), "", nil
}
func (b *recordingBackend) Referrers(_ context.Context, registry, repo, digest string) ([]byte, error) {
	b.registry, b.repo, b.ref = registry, repo, digest
	return []byte(`{"schemaVersion":2,"manifests":[]}`), nil
}

func TestServerDirectRefRoute(t *testing.T) {
	backend := &recordingBackend{blob: []byte("blob-data")}
	server := directServer(backend)

	cases := []struct {
		path     string
		method   string
		registry string
		repo     string
		ref      string
	}{
		{"/v2/registry.gitlab.com/gitlab-org/cli/manifests/v1.104.0", http.MethodGet,
			"registry.gitlab.com", "gitlab-org/cli", "v1.104.0"},
		{"/v2/docker.io/library/nginx/manifests/1.27", http.MethodHead,
			"docker.io", "library/nginx", "1.27"},
		{"/v2/ghcr.io/org/app/manifests/sha256:deadbeef", http.MethodGet,
			"ghcr.io", "org/app", "sha256:deadbeef"},
		{"/v2/registry.gitlab.com/gitlab-org/cli/manifests/v1.104.0?ns=proxy.example.com", http.MethodGet,
			"registry.gitlab.com", "gitlab-org/cli", "v1.104.0"},
		{"/v2/docker.io/library/nginx/blobs/sha256:abc", http.MethodGet,
			"docker.io", "library/nginx", "sha256:abc"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d %q", tc.path, rec.Code, rec.Body.String())
			continue
		}
		if backend.registry != tc.registry || backend.repo != tc.repo || backend.ref != tc.ref {
			t.Errorf("%s routed to %s/%s:%s, want %s/%s:%s",
				tc.path, backend.registry, backend.repo, backend.ref, tc.registry, tc.repo, tc.ref)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/v2/docker.io/library/nginx/blobs/sha256:abc", nil)
	req.Header.Set("Range", "bytes=0-2")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || backend.rangeHdr != "bytes=0-2" {
		t.Errorf("range request = %d range=%q", rec.Code, backend.rangeHdr)
	}
}

func TestServerDirectRefRejectsAmbiguousAndUnknown(t *testing.T) {
	server := directServer(&recordingBackend{})
	for _, target := range []string{
		"/v2/library/nginx/manifests/1.27",
		"/v2/evil.example.com/repo/manifests/1.27",
	} {
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s = %d, want 403", target, rec.Code)
		}
	}
}

func TestServerFirstPullServesSourceOCIThenNydus(t *testing.T) {
	source := newFakeRegistry()
	mirror := newFakeRegistry()
	source.manifests["dockerio/library/nginx:1.27"] = Manifest{
		Digest: "sha256:source", MediaType: ociManifestType,
		Body: []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:config"},"layers":[]}`),
	}
	manager := NewManager(nil)
	server := directServer(&HarborBackend{Source: source, Mirror: mirror, MirrorProject: "mirror", Jobs: manager})
	target := "/v2/docker.io/library/nginx/manifests/1.27"

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("pull %d = %d %q, want 200", i+1, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Docker-Content-Digest"); got != "sha256:source" {
			t.Errorf("pull %d digest = %q, want the source manifest", i+1, got)
		}
	}
	if got := manager.Pending(); got != 1 {
		t.Errorf("pending jobs = %d, want 1", got)
	}

	// The job finishes and the mirror exposes the converted nydus index.
	job := Job{Registry: "docker.io", Repository: "library/nginx", Reference: "1.27", Tag: "1.27", Digest: "sha256:source"}
	manager.setState(job.Key(), "ready", 1, "")
	mirror.manifests["mirror/docker.io/library/nginx:1.27"] = Manifest{
		Digest: "sha256:nydus", MediaType: ociIndexType,
		Body: []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`),
	}
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("converted pull = %d %q, want 200", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Docker-Content-Digest"); got != "sha256:nydus" {
		t.Errorf("converted digest = %q, want the nydus index", got)
	}
}

func TestServerConversionInProgress(t *testing.T) {
	server := directServer(&fakeBackend{})
	server.Backend = backendFunc(func(context.Context, string, string, string) (Manifest, error) {
		return Manifest{}, ErrConverting
	})
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/v2/docker.io/library/nginx/manifests/1.27", nil))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "5" {
		t.Errorf("conversion response = %d retry=%q", rec.Code, rec.Header().Get("Retry-After"))
	}
}

type backendFunc func(context.Context, string, string, string) (Manifest, error)

func (f backendFunc) GetManifest(ctx context.Context, registry, repo, ref string) (Manifest, error) {
	return f(ctx, registry, repo, ref)
}
func (f backendFunc) GetBlob(context.Context, string, string, string, string) (io.ReadCloser, int64, string, error) {
	return nil, 0, "", nil
}
func (f backendFunc) Referrers(context.Context, string, string, string) ([]byte, error) {
	return nil, nil
}

func TestServerBasicAuth(t *testing.T) {
	server := directServer(&recordingBackend{})
	username := fmt.Sprintf("test-user-%d", time.Now().UnixNano())
	password := fmt.Sprintf("test-pass-%d", time.Now().UnixNano())
	server.AuthUsername = username
	server.AuthPassword = password

	for _, target := range []string{
		"/v2/docker.io/library/nginx/manifests/1.27",
		"/healthz",
	} {
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		want := http.StatusUnauthorized
		if target == "/healthz" {
			want = http.StatusOK
		}
		if rec.Code != want {
			t.Errorf("%s anonymous = %d, want %d", target, rec.Code, want)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/v2/docker.io/library/nginx/manifests/1.27", nil)
	req.SetBasicAuth(username, password+"-wrong")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong password = %d, want 401", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/v2/docker.io/library/nginx/manifests/1.27", nil)
	req.SetBasicAuth(username, password)
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("valid basic auth = %d %q", rec.Code, rec.Body.String())
	}

	server.Convert = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) })
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/convert", nil))
	if rec.Code != http.StatusAccepted {
		t.Errorf("/api/convert must use its own Bearer secret, got %d", rec.Code)
	}
}

func TestServerDirectRefProvisioningIsLazyAndAllowlisted(t *testing.T) {
	fake := &fakeHarbor{}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	source := newFakeRegistry()
	mirror := newFakeRegistry()
	source.manifests["registrygitlabcom/gitlab-org/cli:v1.104.0"] = Manifest{
		Digest: "sha256:source", MediaType: ociManifestType, Body: []byte(`{"schemaVersion":2}`),
	}
	backend := &HarborBackend{
		Source: source, Mirror: mirror, MirrorProject: "mirror",
		Provisioner: &Provisioner{
			Harbor:         &HarborClient{BaseURL: server.URL},
			MirrorProject:  "mirror",
			WebhookAddress: "https://proxy.example.com/api/convert",
			WebhookSecret:  "shared",
		},
	}
	proxy := directServer(backend)

	rec := httptest.NewRecorder()
	proxy.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/v2/evil.example.com/repo/manifests/1.0", nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("unknown registry = %d, want 403", rec.Code)
	}
	fake.mu.Lock()
	if len(fake.posts) != 0 {
		t.Errorf("unknown registry created Harbor objects: %v", fake.posts)
	}
	fake.mu.Unlock()

	rec = httptest.NewRecorder()
	proxy.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/v2/registry.gitlab.com/gitlab-org/cli/manifests/v1.104.0", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("direct ref = %d %q", rec.Code, rec.Body.String())
	}
	fake.mu.Lock()
	first := fake.posts["registry"] + fake.posts["project"] + fake.posts["webhook"]
	fake.mu.Unlock()
	if first != 4 {
		t.Errorf("lazy provisioning posts = %v, want one registry, two projects, one webhook", fake.posts)
	}

	rec = httptest.NewRecorder()
	proxy.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/v2/registry.gitlab.com/gitlab-org/cli/manifests/v1.104.0", nil))
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if got := fake.posts["registry"] + fake.posts["project"] + fake.posts["webhook"]; got != first {
		t.Errorf("cached provisioning posts = %v, want unchanged", fake.posts)
	}
}

func TestServerAllowAnyRegistry(t *testing.T) {
	backend := &recordingBackend{}
	server := directServer(backend)
	server.Config.Registry.Allowlist = []string{"*"}

	for _, tc := range []struct {
		path     string
		registry string
		repo     string
	}{
		{"/v2/quay.io/org/image/manifests/v1", "quay.io", "org/image"},
		{"/v2/registry.example.org/team/app/manifests/v2", "registry.example.org", "team/app"},
	} {
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d %q", tc.path, rec.Code, rec.Body.String())
			continue
		}
		if backend.registry != tc.registry || backend.repo != tc.repo {
			t.Errorf("%s routed to %s/%s, want %s/%s", tc.path, backend.registry, backend.repo, tc.registry, tc.repo)
		}
	}

	// An unqualified repository path is not guessed as a registry.
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/v2/library/nginx/manifests/1.27", nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("unqualified path = %d, want 403", rec.Code)
	}
}

func TestServerUpstreamFailuresReturnBadGateway(t *testing.T) {
	for name, backendErr := range map[string]error{
		"provisioning": fmt.Errorf("provision Harbor for %q: %w", "docker.io", errors.New("admin api unavailable")),
		"source":       fmt.Errorf("read source manifest docker.io/library/nginx:1.27: %w", errors.New("upstream 500")),
	} {
		t.Run(name, func(t *testing.T) {
			server := directServer(backendFunc(func(context.Context, string, string, string) (Manifest, error) {
				return Manifest{}, backendErr
			}))
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
				"/v2/docker.io/library/nginx/manifests/1.27", nil))
			if rec.Code != http.StatusBadGateway {
				t.Errorf("status = %d, want 502", rec.Code)
			}
			if got := rec.Header().Get("Retry-After"); got != "" {
				t.Errorf("Retry-After = %q on a non-conversion error", got)
			}
		})
	}
}

func TestHarborBackendDoesNotEnqueueOnSourceError(t *testing.T) {
	source := newFakeRegistry()
	mirror := newFakeRegistry()
	manager := NewManager(nil)
	backend := &HarborBackend{Source: source, Mirror: mirror, MirrorProject: "mirror", Jobs: manager}
	if _, err := backend.GetManifest(context.Background(), "docker.io", "library/nginx", "1.27"); !IsNotFound(err) {
		t.Fatalf("GetManifest() error = %v, want 404", err)
	}
	if got := manager.Pending(); got != 0 {
		t.Errorf("pending jobs = %d, want 0", got)
	}
	if enqueued, _, _ := manager.Metrics(); enqueued != 0 {
		t.Errorf("enqueued jobs = %d, want 0", enqueued)
	}
}

func TestHarborBackendSkipsEnqueueForDigestRefs(t *testing.T) {
	source := newFakeRegistry()
	mirror := newFakeRegistry()
	digest := "sha256:source"
	source.manifests["dockerio/library/nginx:"+digest] = Manifest{
		Digest: digest, MediaType: ociManifestType, Body: []byte(`{"schemaVersion":2}`),
	}
	manager := NewManager(nil)
	backend := &HarborBackend{Source: source, Mirror: mirror, MirrorProject: "mirror", Jobs: manager}
	manifest, err := backend.GetManifest(context.Background(), "docker.io", "library/nginx", digest)
	if err != nil {
		t.Fatalf("GetManifest(digest) error = %v", err)
	}
	if manifest.Digest != digest {
		t.Errorf("manifest digest = %q", manifest.Digest)
	}
	if got := manager.Pending(); got != 0 {
		t.Errorf("digest ref enqueued a job: pending = %d", got)
	}
}

func TestHarborBackendReconvertsStaleCachedIndex(t *testing.T) {
	stale := indexDocument{
		SchemaVersion: 2,
		MediaType:     ociIndexType,
		Manifests: []indexDescriptor{
			{MediaType: ociManifestType, Digest: "sha256:oci", ArtifactType: nydusArtifactType, Platform: &indexPlatform{OS: "linux", Architecture: "amd64"}},
		},
		Annotations: map[string]string{sourceReferenceKey: "registry.example.test/team/app:1.27"},
	}
	staleBody, _ := json.Marshal(stale)
	mirror := newFakeRegistry()
	mirror.manifests["mirror/docker.io/library/nginx:1.27"] = Manifest{
		Digest: "sha256:stale", MediaType: ociIndexType, Body: staleBody,
	}
	manager := NewManager(nil)
	backend := &HarborBackend{
		Source: newFakeRegistry(), Mirror: mirror, MirrorProject: "mirror",
		ProxyPublicURL: testProxyURL, Jobs: manager,
	}
	manifest, err := backend.GetManifest(context.Background(), "docker.io", "library/nginx", "1.27")
	if err != nil {
		t.Fatalf("stale cached tag error = %v, want the mirror manifest", err)
	}
	if manifest.Digest != "sha256:stale" {
		t.Errorf("manifest digest = %q, want the stale mirror manifest", manifest.Digest)
	}
	if got := manager.Pending(); got != 1 {
		t.Errorf("pending reconversions = %d, want 1", got)
	}
}

func TestHarborBackendServesCachedIndexWithFreshSourceReference(t *testing.T) {
	fresh := indexDocument{
		SchemaVersion: 2,
		MediaType:     ociIndexType,
		Manifests: []indexDescriptor{
			{MediaType: ociManifestType, Digest: "sha256:oci", ArtifactType: nydusArtifactType, Platform: &indexPlatform{OS: "linux", Architecture: "amd64"}},
		},
		Annotations: map[string]string{sourceReferenceKey: "proxy.example.test/docker.io/library/nginx:1.27"},
	}
	freshBody, _ := json.Marshal(fresh)
	mirror := newFakeRegistry()
	mirror.manifests["mirror/docker.io/library/nginx:1.27"] = Manifest{
		Digest: "sha256:fresh", MediaType: ociIndexType, Body: freshBody,
	}
	manager := NewManager(nil)
	backend := &HarborBackend{
		Source: newFakeRegistry(), Mirror: mirror, MirrorProject: "mirror",
		ProxyPublicURL: testProxyURL, Jobs: manager,
	}
	manifest, err := backend.GetManifest(context.Background(), "docker.io", "library/nginx", "1.27")
	if err != nil {
		t.Fatalf("fresh cached tag error = %v", err)
	}
	if manifest.Digest != "sha256:fresh" {
		t.Errorf("manifest digest = %q", manifest.Digest)
	}
	if got := manager.Pending(); got != 0 {
		t.Errorf("fresh cached tag enqueued a job: pending = %d", got)
	}
}

func TestDefaultHTTPClientsAreBounded(t *testing.T) {
	if got := (&HTTPRegistry{}).client().Timeout; got != defaultHTTPTimeout {
		t.Errorf("HTTPRegistry default timeout = %v, want %v", got, defaultHTTPTimeout)
	}
	if got := (&HarborClient{}).client().Timeout; got != defaultHTTPTimeout {
		t.Errorf("HarborClient default timeout = %v, want %v", got, defaultHTTPTimeout)
	}
}

package registryproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
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
func (f *fakeBackend) GetBlob(context.Context, string, string, string) ([]byte, error) {
	return f.blob, nil
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
	if !strings.Contains(rec.Body.String(), "registry_proxy_requests_total") {
		t.Errorf("metrics = %q", rec.Body.String())
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

func (b *cancelProbeBackend) GetBlob(ctx context.Context, _, _, _ string) ([]byte, error) {
	b.wait(ctx)
	return []byte("blob-data"), nil
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

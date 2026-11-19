package registryproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebhookAcceptsPushAndDeduplicates(t *testing.T) {
	manager := NewManager(nil)
	webhook := &Webhook{Secret: "shared", Manager: manager, Config: testConfig(t)}
	body := `{"type":"PUSH_ARTIFACT","event_data":{"repository":{"repo_full_name":"dockerio/library/nginx"},"resources":[{"digest":"sha256:abc","tag":"1.27","resource_url":"harbor.example.com/dockerio/library/nginx:1.27"}]}}`
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/convert", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer shared")
		rec := httptest.NewRecorder()
		webhook.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("webhook response = %d %q", rec.Code, rec.Body.String())
		}
	}
	if manager.Pending() != 1 {
		t.Errorf("pending jobs = %d, want 1", manager.Pending())
	}
	if _, ok := manager.State("docker.io/library/nginx/1.27"); !ok {
		t.Error("expected tag-deduplicated job")
	}
}

func TestWebhookRejectsAuthAndPullEvents(t *testing.T) {
	webhook := &Webhook{Secret: "shared", Manager: NewManager(nil), Config: testConfig(t)}
	req := httptest.NewRequest(http.MethodPost, "/api/convert", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()
	webhook.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("bad auth = %d, want 401", rec.Code)
	}

	body := `{"type":"PULL_ARTIFACT","event_data":{"repository":{"repo_full_name":"dockerio/library/nginx"},"resources":[{"digest":"sha256:abc"}]}}`
	req = httptest.NewRequest(http.MethodPost, "/api/convert", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer shared")
	rec = httptest.NewRecorder()
	webhook.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("pull event = %d, want 400", rec.Code)
	}
}

func TestWebhookResourceURLFallback(t *testing.T) {
	manager := NewManager(nil)
	webhook := &Webhook{Secret: "shared", Manager: manager, Config: testConfig(t)}
	body := `{"type":"PUSH_ARTIFACT","event_data":{"resources":[{"digest":"sha256:def","resource_url":"harbor.example.com/dockerio/library/busybox:1.36"}]}}`
	req := httptest.NewRequest(http.MethodPost, "/api/convert", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer shared")
	rec := httptest.NewRecorder()
	webhook.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("webhook response = %d %q", rec.Code, rec.Body.String())
	}
	if _, ok := manager.State("docker.io/library/busybox/sha256:def"); !ok {
		t.Error("resource_url fallback job missing")
	}
}

func TestWebhookWildcardMapsProvisionedProject(t *testing.T) {
	fake := &fakeHarbor{}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	provisioner := &Provisioner{
		Harbor:         &HarborClient{BaseURL: server.URL},
		MirrorProject:  "mirror",
		WebhookAddress: "https://proxy.example.com/api/convert",
		WebhookSecret:  "shared",
	}
	if err := provisioner.Ensure(context.Background(), "quay.io"); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	cfg := testConfig(t)
	cfg.Registry.Allowlist = []string{"*"}
	manager := NewManager(nil)
	webhook := &Webhook{Secret: "shared", Manager: manager, Config: cfg, Provisioner: provisioner}
	body := `{"type":"PUSH_ARTIFACT","event_data":{"repository":{"repo_full_name":"quayio/org/image"},"resources":[{"digest":"sha256:def","tag":"v1"}]}}`
	req := httptest.NewRequest(http.MethodPost, "/api/convert", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer shared")
	rec := httptest.NewRecorder()
	webhook.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("webhook response = %d %q", rec.Code, rec.Body.String())
	}
	if _, ok := manager.State("quay.io/org/image/v1"); !ok {
		t.Error("wildcard project job missing")
	}
}

func TestWebhookOneShotReconvert(t *testing.T) {
	manager := NewManager(nil)
	webhook := &Webhook{Secret: "shared", Manager: manager, Config: testConfig(t)}
	target := "/api/convert?registry=docker.io&repository=library/nginx&tag=1.27"

	req := httptest.NewRequest(http.MethodPost, target, nil)
	req.Header.Set("Authorization", "Bearer shared")
	rec := httptest.NewRecorder()
	webhook.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("reconvert = %d %q", rec.Code, rec.Body.String())
	}
	if _, ok := manager.State("docker.io/library/nginx/1.27"); !ok {
		t.Error("reconvert job missing")
	}

	req = httptest.NewRequest(http.MethodPost, target, nil)
	req.Header.Set("Authorization", "Bearer shared")
	rec = httptest.NewRecorder()
	webhook.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Errorf("duplicate reconvert = %d, want 409", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/convert?registry=evil.example.test&repository=library/nginx&tag=1.27", nil)
	req.Header.Set("Authorization", "Bearer shared")
	rec = httptest.NewRecorder()
	webhook.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("non-allowlisted reconvert = %d, want 403", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, target, nil)
	rec = httptest.NewRecorder()
	webhook.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous reconvert = %d, want 401", rec.Code)
	}
}

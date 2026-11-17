package registryproxy

import (
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
	if _, ok := manager.State("docker.io/library/nginx/sha256:abc"); !ok {
		t.Error("expected digest-deduplicated job")
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

package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBootstrapHandlerHealthBeforeReady(t *testing.T) {
	handler := &bootstrapHandler{}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("healthz before ready = %d, want 200", rec.Code)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("readyz before ready = %d, want 503", rec.Code)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v2/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("data path before ready = %d, want 503", rec.Code)
	}

	handler.set(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("readyz after ready = %d, want 200", rec.Code)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v2/", nil))
	if rec.Code != http.StatusNoContent {
		t.Errorf("data path after ready = %d, want delegated status", rec.Code)
	}
}

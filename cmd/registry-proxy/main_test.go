package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestReadRobotCredentials(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "username"), []byte("robot$d7y-registry-proxy\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "password"), []byte("fixture-password\n"), 0600); err != nil {
		t.Fatal(err)
	}
	robot, err := readRobotCredentials(dir)
	if err != nil {
		t.Fatalf("readRobotCredentials() error = %v", err)
	}
	if robot.Name != "robot$d7y-registry-proxy" || robot.Secret != "fixture-password" {
		t.Error("robot credentials were not read from the mounted Secret")
	}

	if err := os.Remove(filepath.Join(dir, "password")); err != nil {
		t.Fatal(err)
	}
	if _, err := readRobotCredentials(dir); err == nil {
		t.Error("readRobotCredentials() error = nil, want missing password error")
	}
}

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

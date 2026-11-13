package managerapi

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
)

func TestEnsureSchedulerClusterCreatesAndReuses(t *testing.T) {
	var listCalls, createCalls atomic.Int32
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case SignInPath:
			writeJSON(w, http.StatusOK, SignInResponse{Token: "token"})
		case SchedulerClustersPath:
			if r.Method == http.MethodPost {
				createCalls.Add(1)
				writeJSON(w, http.StatusCreated, SchedulerCluster{ID: 5, Name: "ns-c1"})
				return
			}
			if got := r.URL.Query().Get("name"); got != "ns-c1" {
				t.Errorf("name filter = %q", got)
			}
			if listCalls.Add(1) == 1 {
				writeJSON(w, http.StatusOK, []SchedulerCluster{})
				return
			}
			writeJSON(w, http.StatusOK, []SchedulerCluster{{ID: 5, Name: "ns-c1"}})
		}
	}))

	req := NewSchedulerClusterRequest("ns", "c1")
	first, err := client.EnsureSchedulerCluster(context.Background(), req)
	if err != nil {
		t.Fatalf("EnsureSchedulerCluster(first) error = %v", err)
	}
	second, err := client.EnsureSchedulerCluster(context.Background(), req)
	if err != nil {
		t.Fatalf("EnsureSchedulerCluster(second) error = %v", err)
	}
	if first.ID != 5 || second.ID != 5 {
		t.Errorf("IDs = %d/%d, want 5/5", first.ID, second.ID)
	}
	if createCalls.Load() != 1 {
		t.Errorf("create calls = %d, want 1", createCalls.Load())
	}
	if listCalls.Load() != 2 {
		t.Errorf("list calls = %d, want 2", listCalls.Load())
	}
}

func TestEnsureSeedPeerClusterCreatesAndReuses(t *testing.T) {
	var createCalls atomic.Int32
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case SignInPath:
			writeJSON(w, http.StatusOK, SignInResponse{Token: "token"})
		case SeedPeerClustersPath:
			if r.Method == http.MethodPost {
				createCalls.Add(1)
				writeJSON(w, http.StatusCreated, SeedPeerCluster{ID: 7, Name: "ns-c1-seed"})
				return
			}
			writeJSON(w, http.StatusOK, []SeedPeerCluster{{ID: 7, Name: "ns-c1-seed"}})
		}
	}))

	req := NewSeedPeerClusterRequest("ns", "c1")
	first, err := client.EnsureSeedPeerCluster(context.Background(), req)
	if err != nil {
		t.Fatalf("EnsureSeedPeerCluster() error = %v", err)
	}
	second, err := client.EnsureSeedPeerCluster(context.Background(), req)
	if err != nil {
		t.Fatalf("EnsureSeedPeerCluster(second) error = %v", err)
	}
	if first.ID != 7 || second.ID != 7 {
		t.Errorf("IDs = %d/%d, want 7/7", first.ID, second.ID)
	}
	if createCalls.Load() != 0 {
		t.Errorf("create calls = %d, want 0 (existing record)", createCalls.Load())
	}
}

func TestLinkSeedPeerClusterIsIdempotent(t *testing.T) {
	var puts atomic.Int32
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == SignInPath {
			writeJSON(w, http.StatusOK, SignInResponse{Token: "token"})
			return
		}
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		if want := SeedPeerClustersPath + "/7/scheduler-clusters/5"; r.URL.Path != want {
			t.Errorf("path = %q, want %q", r.URL.Path, want)
		}
		puts.Add(1)
		w.WriteHeader(http.StatusOK)
	}))

	if err := client.LinkSeedPeerCluster(context.Background(), 7, 5); err != nil {
		t.Fatalf("LinkSeedPeerCluster() error = %v", err)
	}
	if err := client.LinkSeedPeerCluster(context.Background(), 7, 5); err != nil {
		t.Fatalf("LinkSeedPeerCluster(second) error = %v", err)
	}
	if puts.Load() != 2 {
		t.Errorf("PUT calls = %d, want 2 (idempotent re-apply)", puts.Load())
	}
}

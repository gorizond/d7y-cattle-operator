package managerapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	client, err := New(Options{
		BaseURL:    server.URL,
		Username:   "admin",
		Password:   "secret",
		HTTPClient: server.Client(),
		Timeout:    time.Second,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return client
}

func TestSignInAndBearer(t *testing.T) {
	var signins atomic.Int32
	var sawBearer atomic.Bool

	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case SignInPath:
			signins.Add(1)
			if r.Method != http.MethodPost {
				t.Errorf("signin method = %s", r.Method)
			}
			var raw map[string]any
			if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
				t.Errorf("decode signin: %v", err)
			}
			if raw["name"] != "admin" || raw["password"] != "secret" {
				t.Errorf("signin body = %+v", raw)
			}
			if _, ok := raw["username"]; ok {
				t.Error("signin body must use name, not username")
			}
			writeJSON(w, http.StatusOK, SignInResponse{Token: "token-1", Expire: "2026-01-01T00:00:00Z"})
		case SchedulerClustersPath:
			if got := r.Header.Get("Authorization"); got != "Bearer token-1" {
				t.Errorf("Authorization = %q", got)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			sawBearer.Store(true)
			writeJSON(w, http.StatusOK, []SchedulerCluster{{ID: 2, Name: "ns-c1"}})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	if err := client.SignIn(context.Background()); err != nil {
		t.Fatalf("SignIn() error = %v", err)
	}
	clusters, err := client.ListSchedulerClusters(context.Background())
	if err != nil {
		t.Fatalf("ListSchedulerClusters() error = %v", err)
	}
	if signins.Load() != 1 {
		t.Errorf("signins = %d, want 1", signins.Load())
	}
	if !sawBearer.Load() {
		t.Error("list request did not send the bearer token")
	}
	if len(clusters) != 1 || clusters[0].ID != 2 || clusters[0].Name != "ns-c1" {
		t.Errorf("clusters = %+v", clusters)
	}
}

func TestReauthOn401(t *testing.T) {
	var signins atomic.Int32
	var listCalls atomic.Int32
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case SignInPath:
			count := signins.Add(1)
			writeJSON(w, http.StatusOK, SignInResponse{Token: "token-" + strconv.Itoa(int(count))})
		case SchedulerClustersPath:
			listCalls.Add(1)
			if r.Header.Get("Authorization") == "Bearer token-1" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			writeJSON(w, http.StatusOK, []SchedulerCluster{{ID: 3, Name: "ns-c1"}})
		}
	}))

	clusters, err := client.ListSchedulerClusters(context.Background())
	if err != nil {
		t.Fatalf("ListSchedulerClusters() error = %v", err)
	}
	if len(clusters) != 1 || clusters[0].ID != 3 {
		t.Errorf("clusters = %+v", clusters)
	}
	if signins.Load() != 2 {
		t.Errorf("signins = %d, want 2", signins.Load())
	}
	if listCalls.Load() != 2 {
		t.Errorf("list calls = %d, want 2", listCalls.Load())
	}
}

func TestRepeated401DoesNotLoop(t *testing.T) {
	var signins atomic.Int32
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == SignInPath {
			signins.Add(1)
			writeJSON(w, http.StatusOK, SignInResponse{Token: "token"})
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))

	_, err := client.ListSchedulerClusters(context.Background())
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("error = %v, want ErrUnauthorized", err)
	}
	if signins.Load() != 2 {
		t.Errorf("signins = %d, want 2 (initial + one re-auth)", signins.Load())
	}
}

func TestForbiddenTreatedAsAuthFailure(t *testing.T) {
	var signins atomic.Int32
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == SignInPath {
			signins.Add(1)
			writeJSON(w, http.StatusOK, SignInResponse{Token: "token"})
			return
		}
		w.WriteHeader(http.StatusForbidden)
	}))

	_, err := client.ListSchedulerClusters(context.Background())
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("error = %v, want ErrUnauthorized", err)
	}
	if signins.Load() != 2 {
		t.Errorf("signins = %d, want 2 (one re-signin)", signins.Load())
	}
}

func TestGetNotFound(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == SignInPath {
			writeJSON(w, http.StatusOK, SignInResponse{Token: "token"})
			return
		}
		if r.URL.Path == SchedulerClustersPath+"/7" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))

	if _, err := client.GetSchedulerCluster(context.Background(), 7); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetSchedulerCluster() error = %v, want ErrNotFound", err)
	}
}

func TestEnsureSchedulerClusterConflict(t *testing.T) {
	var listCalls atomic.Int32
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case SignInPath:
			writeJSON(w, http.StatusOK, SignInResponse{Token: "token"})
		case SchedulerClustersPath:
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusConflict)
				return
			}
			if got := r.URL.Query().Get("name"); got != "ns-c1" {
				t.Errorf("list name filter = %q, want ns-c1", got)
			}
			call := listCalls.Add(1)
			if call == 1 {
				writeJSON(w, http.StatusOK, []SchedulerCluster{})
				return
			}
			writeJSON(w, http.StatusOK, []SchedulerCluster{{ID: 9, Name: "ns-c1"}})
		}
	}))

	req := NewSchedulerClusterRequest("ns", "c1")
	cluster, err := client.EnsureSchedulerCluster(context.Background(), req)
	if err != nil {
		t.Fatalf("EnsureSchedulerCluster() error = %v", err)
	}
	if cluster.ID != 9 || cluster.Name != "ns-c1" {
		t.Errorf("cluster = %+v", cluster)
	}
	if listCalls.Load() != 2 {
		t.Errorf("list calls = %d, want 2", listCalls.Load())
	}
}

func TestCreateSchedulerClusterBody(t *testing.T) {
	var body map[string]any
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case SignInPath:
			writeJSON(w, http.StatusOK, SignInResponse{Token: "token"})
		case SchedulerClustersPath:
			if r.Method != http.MethodPost {
				t.Errorf("method = %s", r.Method)
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode create: %v", err)
			}
			writeJSON(w, http.StatusCreated, SchedulerCluster{ID: 10, Name: "ns-c1"})
		}
	}))

	created, err := client.CreateSchedulerCluster(context.Background(), NewSchedulerClusterRequest("ns", "c1"))
	if err != nil {
		t.Fatalf("CreateSchedulerCluster() error = %v", err)
	}
	if created.ID != 10 {
		t.Errorf("created = %+v", created)
	}
	if _, ok := body["id"]; ok {
		t.Error("create request must not contain id")
	}
	if body["name"] != "ns-c1" || body["bio"] != ManagedByBio || body["is_default"] != false {
		t.Errorf("body = %+v", body)
	}
	config := body["config"].(map[string]any)
	if config["candidate_parent_limit"] != float64(100) || config["filter_parent_limit"] != float64(100) || config["job_rate_limit"] != float64(0) {
		t.Errorf("config = %+v", config)
	}
	clientConfig := body["client_config"].(map[string]any)
	if clientConfig["load_limit"] != float64(500) {
		t.Errorf("client_config = %+v", clientConfig)
	}
	assertEmptyObject(t, clientConfig["block_list"], "client_config.block_list")
	seedClientConfig := body["seed_client_config"].(map[string]any)
	assertEmptyObject(t, seedClientConfig["block_list"], "seed_client_config.block_list")
	if _, ok := body["seed_peer_cluster_id"]; ok {
		t.Error("create request must not contain seed_peer_cluster_id")
	}
	if _, ok := body["scopes"]; ok {
		t.Errorf("scopes omitted by default, got %+v", body["scopes"])
	}
}

func TestCreateSeedPeerClusterBody(t *testing.T) {
	var body map[string]any
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case SignInPath:
			writeJSON(w, http.StatusOK, SignInResponse{Token: "token"})
		case SeedPeerClustersPath:
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode create: %v", err)
			}
			writeJSON(w, http.StatusCreated, SeedPeerCluster{ID: 11, Name: "ns-c1-seed"})
		}
	}))

	created, err := client.CreateSeedPeerCluster(context.Background(), NewSeedPeerClusterRequest("ns", "c1"))
	if err != nil {
		t.Fatalf("CreateSeedPeerCluster() error = %v", err)
	}
	if created.ID != 11 || created.Name != "ns-c1-seed" {
		t.Errorf("created = %+v", created)
	}
	if body["name"] != "ns-c1-seed" || body["bio"] != ManagedByBio {
		t.Errorf("body = %+v", body)
	}
	config := body["config"].(map[string]any)
	if config["load_limit"] != float64(500) {
		t.Errorf("config = %+v", config)
	}
	assertEmptyObject(t, config["block_list"], "config.block_list")
	if _, ok := body["id"]; ok {
		t.Error("create request must not contain id")
	}
	if _, ok := body["scheduler_cluster_id"]; ok {
		t.Error("create request must not contain scheduler_cluster_id")
	}
}

func TestListPaginationFollowsLink(t *testing.T) {
	var pages atomic.Int32
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case SignInPath:
			writeJSON(w, http.StatusOK, SignInResponse{Token: "token"})
		case SchedulerClustersPath:
			if got := r.URL.Query().Get("per_page"); got != "50" {
				t.Errorf("per_page = %q", got)
			}
			pages.Add(1)
			if r.URL.Query().Get("page") == "1" {
				next := (&url.URL{
					Scheme:   "https",
					Host:     r.Host,
					Path:     SchedulerClustersPath,
					RawQuery: "page=2&per_page=50",
				}).String()
				w.Header().Set("Link", "<"+next+">; rel=\"next\"")
				// A short page must not stop pagination; only the Link header does.
				writeJSON(w, http.StatusOK, []SchedulerCluster{{ID: 2, Name: "first"}})
				return
			}
			writeJSON(w, http.StatusOK, []SchedulerCluster{{ID: 3, Name: "second"}})
		}
	}))

	clusters, err := client.ListSchedulerClusters(context.Background())
	if err != nil {
		t.Fatalf("ListSchedulerClusters() error = %v", err)
	}
	if len(clusters) != 2 {
		t.Errorf("len(clusters) = %d, want 2", len(clusters))
	}
	if pages.Load() != 2 {
		t.Errorf("pages = %d, want 2", pages.Load())
	}
}

func TestListNullIsEmpty(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == SignInPath {
			writeJSON(w, http.StatusOK, SignInResponse{Token: "token"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("null"))
	}))

	clusters, err := client.ListSchedulerClusters(context.Background())
	if err != nil {
		t.Fatalf("ListSchedulerClusters() error = %v", err)
	}
	if len(clusters) != 0 {
		t.Errorf("clusters = %+v, want empty", clusters)
	}
}

func TestHTTPError(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == SignInPath {
			writeJSON(w, http.StatusOK, SignInResponse{Token: "token"})
			return
		}
		http.Error(w, "boom", http.StatusInternalServerError)
	}))

	_, err := client.ListSchedulerClusters(context.Background())
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error = %v, want *HTTPError", err)
	}
	if httpErr.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d", httpErr.StatusCode)
	}
}

func TestContextTimeout(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == SignInPath {
			writeJSON(w, http.StatusOK, SignInResponse{Token: "token"})
			return
		}
		time.Sleep(100 * time.Millisecond)
		writeJSON(w, http.StatusOK, []SchedulerCluster{})
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := client.ListSchedulerClusters(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context deadline exceeded", err)
	}
}

func TestNamesAndRequests(t *testing.T) {
	if got := SchedulerClusterName("ns", "c1"); got != "ns-c1" {
		t.Errorf("SchedulerClusterName = %q", got)
	}
	if got := SeedPeerClusterName("ns", "c1"); got != "ns-c1-seed" {
		t.Errorf("SeedPeerClusterName = %q", got)
	}

	scheduler := NewSchedulerClusterRequest("ns", "c1")
	if scheduler.Bio != ManagedByBio || scheduler.Config.CandidateParentLimit != 100 ||
		scheduler.Config.FilterParentLimit != 100 || scheduler.ClientConfig.LoadLimit != 500 ||
		scheduler.ClientConfig.BlockList == nil || scheduler.SeedClientConfig.BlockList == nil {
		t.Errorf("scheduler request = %+v", scheduler)
	}
	seed := NewSeedPeerClusterRequest("ns", "c1")
	if seed.Bio != ManagedByBio || seed.Config.LoadLimit != 500 || seed.Config.BlockList == nil {
		t.Errorf("seed request = %+v", seed)
	}
}

func TestNewValidation(t *testing.T) {
	if _, err := New(Options{BaseURL: "http://manager.example.com", Username: "u", Password: "p"}); !errors.Is(err, ErrInvalidBaseURL) {
		t.Errorf("http base URL error = %v", err)
	}
	if _, err := New(Options{BaseURL: "https://manager.example.com", Password: "p"}); !errors.Is(err, ErrMissingCredentials) {
		t.Errorf("missing credentials error = %v", err)
	}
}

func TestLinkSeedPeerCluster(t *testing.T) {
	var method, path string
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == SignInPath {
			writeJSON(w, http.StatusOK, SignInResponse{Token: "token"})
			return
		}
		method, path = r.Method, r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))

	if err := client.LinkSeedPeerCluster(context.Background(), 7, 9); err != nil {
		t.Fatalf("LinkSeedPeerCluster() error = %v", err)
	}
	if method != http.MethodPut {
		t.Errorf("method = %s, want PUT", method)
	}
	if want := SeedPeerClustersPath + "/7/scheduler-clusters/9"; path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
}

func TestUnprocessableIsNotRetried(t *testing.T) {
	var signins atomic.Int32
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == SignInPath {
			signins.Add(1)
			writeJSON(w, http.StatusOK, SignInResponse{Token: "token"})
			return
		}
		w.WriteHeader(http.StatusUnprocessableEntity)
	}))

	_, err := client.ListSchedulerClusters(context.Background())
	if !errors.Is(err, ErrUnprocessable) {
		t.Fatalf("error = %v, want ErrUnprocessable", err)
	}
	if signins.Load() != 1 {
		t.Errorf("signins = %d, want 1 (no re-auth on 422)", signins.Load())
	}
}

func assertEmptyObject(t *testing.T, value any, field string) {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s = %T, want an object", field, value)
	}
	if len(object) != 0 {
		t.Errorf("%s = %+v, want an empty object", field, object)
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

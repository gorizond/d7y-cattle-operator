package cluster

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/gorizond/d7y-cattle-operator/pkg/managerapi"
	"github.com/gorizond/d7y-cattle-operator/pkg/registryproxy"
)

type fakePreheatEnsurer struct {
	mu    sync.Mutex
	calls []preheatCall
	err   error
}

type preheatCall struct {
	Name     string
	Endpoint string
	Token    string
	Insecure bool
}

func (f *fakePreheatEnsurer) EnsurePreheatInstance(_ context.Context, name, endpoint, token string, insecure bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, preheatCall{Name: name, Endpoint: endpoint, Token: token, Insecure: insecure})
	return f.err
}

type preheatHarborState struct {
	mu         sync.Mutex
	instances  []registryproxy.HarborPreheatInstance
	posts      int
	puts       int
	pings      int
	pingStatus int
	lastToken  string
}

func (f *preheatHarborState) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2.0/p2p/preheat/instances":
			_ = json.NewEncoder(w).Encode(f.instances)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2.0/p2p/preheat/instances":
			f.posts++
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.lastToken = authToken(body)
			f.instances = append(f.instances, registryproxy.HarborPreheatInstance{
				ID:       7,
				Name:     managerString(body["name"]),
				Vendor:   managerString(body["vendor"]),
				Endpoint: managerString(body["endpoint"]),
			})
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/v2.0/p2p/preheat/instances/"):
			f.puts++
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.lastToken = authToken(body)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2.0/p2p/preheat/instances/ping":
			f.pings++
			if f.pingStatus >= http.StatusBadRequest {
				http.Error(w, "manager endpoint is unreachable", f.pingStatus)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	})
}

func authToken(body map[string]any) string {
	auth, _ := body["auth_info"].(map[string]any)
	token, _ := auth["token"].(string)
	return token
}

func managerString(value any) string {
	text, _ := value.(string)
	return text
}

type preheatManagerState struct {
	mu      sync.Mutex
	signins int
	pats    int
	token   string
}

func (f *preheatManagerState) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case managerapi.SignInPath:
			f.signins++
			writeTestJSON(w, http.StatusOK, managerapi.SignInResponse{Token: "jwt-1"})
		case managerapi.PersonalAccessTokensPath:
			f.pats++
			var body managerapi.CreatePersonalAccessTokenRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode PAT body: %v", err)
			}
			if body.Name != harborPreheatPATName || body.UserID != harborPreheatPATUserID ||
				len(body.Scopes) != 1 || body.Scopes[0] != harborPreheatPATScope {
				t.Errorf("PAT body = %+v", body)
			}
			writeTestJSON(w, http.StatusCreated, managerapi.CreatePersonalAccessTokenResponse{Token: f.token})
		default:
			http.NotFound(w, r)
		}
	})
}

func writeTestJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func newPreheatManager(t *testing.T, token string) (*preheatManagerState, *httptest.Server) {
	t.Helper()
	state := &preheatManagerState{token: token}
	server := httptest.NewTLSServer(state.handler(t))
	t.Cleanup(server.Close)
	return state, server
}

func TestEnsureHarborPreheatCreatesPATAndInstance(t *testing.T) {
	controller, _, events := newTestController(t)
	harbor := &preheatHarborState{}
	harborServer := httptest.NewServer(harbor.handler())
	t.Cleanup(harborServer.Close)
	manager, managerServer := newPreheatManager(t, "pat-token")
	controller.opts.ManagerAPIURL = managerServer.URL
	controller.managerHTTPClient = managerServer.Client()
	delete(controller.secrets.(*fakeSecretStore).secrets, "dragonfly-system/"+harborPreheatPATSecretName)
	controller.secrets.(*fakeSecretStore).put(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "manager-api-credentials", Namespace: "dragonfly-system"},
		Data:       map[string][]byte{"username": []byte("root"), "password": []byte("password")},
	})
	cluster := testHarborCluster()
	controller.secrets.(*fakeSecretStore).put(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "c1-harbor-admin", Namespace: "fleet-default"},
		Data:       map[string][]byte{"HARBOR_ADMIN_PASSWORD": []byte("admin-password")},
	})
	hc := controller.helmopCluster(cluster)
	hc.HarborURL = harborServer.URL
	controller.preheatEnsurer = &registryproxy.HarborClient{
		BaseURL: harborServer.URL, Username: "admin", Password: "admin-password",
	}

	if err := controller.ensureHarborPreheat(cluster, hc); err != nil {
		t.Fatalf("ensureHarborPreheat() error = %v", err)
	}
	pat, err := controller.secrets.Get("dragonfly-system", harborPreheatPATSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("PAT Secret was not created: %v", err)
	}
	if got := string(pat.Data[harborPreheatPATSecretKey]); got != "pat-token" {
		t.Errorf("PAT Secret token = %q, want the created token", got)
	}
	if manager.signins != 1 || manager.pats != 1 {
		t.Errorf("manager signin/PAT = %d/%d, want 1/1", manager.signins, manager.pats)
	}
	if harbor.posts != 1 || harbor.puts != 0 || harbor.pings != 1 {
		t.Errorf("Harbor POST/PUT/ping = %d/%d/%d, want 1/0/1", harbor.posts, harbor.puts, harbor.pings)
	}
	if len(harbor.instances) != 1 || harbor.instances[0].Name != "dragonfly" ||
		harbor.instances[0].Vendor != "dragonfly" || harbor.instances[0].Endpoint != managerServer.URL {
		t.Errorf("Harbor instances = %+v", harbor.instances)
	}
	if harbor.lastToken != "pat-token" {
		t.Error("Harbor did not receive the manager PAT")
	}

	// The second reconcile reuses the PAT Secret and updates the instance.
	if err := controller.ensureHarborPreheat(cluster, hc); err != nil {
		t.Fatalf("ensureHarborPreheat(second) error = %v", err)
	}
	if manager.signins != 1 || manager.pats != 1 {
		t.Errorf("manager signin/PAT after reuse = %d/%d, want 1/1", manager.signins, manager.pats)
	}
	if harbor.posts != 1 || harbor.puts != 1 || harbor.pings != 2 {
		t.Errorf("Harbor POST/PUT/ping after reuse = %d/%d/%d, want 1/1/2", harbor.posts, harbor.puts, harbor.pings)
	}
	if len(events.events) != 0 {
		t.Errorf("events = %+v, want none", events.events)
	}
}

func TestEnsureHarborPreheatEmitsEventOnFailure(t *testing.T) {
	controller, _, events := newTestController(t)
	harbor := &preheatHarborState{pingStatus: http.StatusInternalServerError}
	harborServer := httptest.NewServer(harbor.handler())
	t.Cleanup(harborServer.Close)
	controller.secrets.(*fakeSecretStore).put(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: harborPreheatPATSecretName, Namespace: "dragonfly-system"},
		Data:       map[string][]byte{harborPreheatPATSecretKey: []byte("existing-pat")},
	})
	cluster := testHarborCluster()
	controller.secrets.(*fakeSecretStore).put(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "c1-harbor-admin", Namespace: "fleet-default"},
		Data:       map[string][]byte{"HARBOR_ADMIN_PASSWORD": []byte("admin-password")},
	})
	hc := controller.helmopCluster(cluster)
	hc.HarborURL = harborServer.URL
	controller.preheatEnsurer = &registryproxy.HarborClient{
		BaseURL: harborServer.URL, Username: "admin", Password: "admin-password",
	}

	err := controller.ensureHarborPreheat(cluster, hc)
	if err == nil {
		t.Fatal("ensureHarborPreheat() error = nil, want ping failure")
	}
	if strings.Contains(err.Error(), "existing-pat") {
		t.Error("error contains the PAT")
	}
	found := false
	for _, event := range events.events {
		if event.Reason == "HarborPreheatFailed" {
			found = true
		}
	}
	if !found {
		t.Errorf("events = %+v, want HarborPreheatFailed", events.events)
	}
}

func TestReconcileCreatesHarborPreheatInstance(t *testing.T) {
	controller, _, _ := newTestController(t)
	harbor := &preheatHarborState{}
	harborServer := httptest.NewServer(harbor.handler())
	t.Cleanup(harborServer.Close)
	controller.opts.ProxyHarborURL = harborServer.URL
	// The PAT Secret is reused, so the manager API is never contacted.
	controller.opts.ManagerAPIURL = "https://manager.invalid"
	controller.preheatEnsurer = &registryproxy.HarborClient{
		BaseURL: harborServer.URL, Username: "admin", Password: "admin-password",
	}
	controller.secrets.(*fakeSecretStore).put(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: harborPreheatPATSecretName, Namespace: "dragonfly-system"},
		Data:       map[string][]byte{harborPreheatPATSecretKey: []byte("existing-pat")},
	})
	cluster := testHarborCluster()
	controller.clusters.(*fakeClusterStore).set(cluster)

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	if harbor.posts != 1 || harbor.pings != 1 {
		t.Errorf("Harbor POST/ping = %d/%d, want 1/1", harbor.posts, harbor.pings)
	}
	if harbor.lastToken != "existing-pat" {
		t.Error("Harbor did not receive the stored PAT")
	}
}

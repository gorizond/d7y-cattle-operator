package registryproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const testPreheatToken = "preheat-pat-token"

type fakePreheatHarbor struct {
	mu           sync.Mutex
	instances    []HarborPreheatInstance
	posts        int
	puts         int
	pings        int
	conflictOnce bool
	pingStatus   int
	lastBody     map[string]any
}

func (f *fakePreheatHarbor) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == harborPreheatInstancesPath:
			_ = json.NewEncoder(w).Encode(f.instances)
		case r.Method == http.MethodPost && r.URL.Path == harborPreheatInstancesPath:
			f.posts++
			if f.conflictOnce {
				f.conflictOnce = false
				// A 409 means the name already exists; the next list must see it.
				f.instances = append(f.instances, HarborPreheatInstance{ID: 7, Name: "dragonfly", Vendor: harborPreheatVendor})
				w.WriteHeader(http.StatusConflict)
				return
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.lastBody = body
			f.instances = append(f.instances, HarborPreheatInstance{
				ID:       7,
				Name:     stringValue(body["name"]),
				Vendor:   stringValue(body["vendor"]),
				Endpoint: stringValue(body["endpoint"]),
			})
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPut && r.URL.Path == harborPreheatInstancesPath+"/dragonfly":
			f.puts++
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.lastBody = body
			if body["id"] != float64(7) {
				t.Errorf("PUT body id = %v, want 7", body["id"])
			}
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Path == harborPreheatPingPath:
			f.pings++
			if f.pingStatus >= http.StatusBadRequest {
				http.Error(w, "manager endpoint is unreachable", f.pingStatus)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected Harbor request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func newPreheatTestClient(t *testing.T) (*HarborClient, *fakePreheatHarbor) {
	t.Helper()
	harbor := &fakePreheatHarbor{}
	server := httptest.NewServer(harbor.handler(t))
	t.Cleanup(server.Close)
	return &HarborClient{BaseURL: server.URL, Username: "admin", Password: "password"}, harbor
}

func TestEnsurePreheatInstanceCreates(t *testing.T) {
	client, harbor := newPreheatTestClient(t)

	if err := client.EnsurePreheatInstance(context.Background(), "dragonfly", "https://manager.example.com", testPreheatToken, false); err != nil {
		t.Fatalf("EnsurePreheatInstance() error = %v", err)
	}
	if harbor.posts != 1 || harbor.puts != 0 || harbor.pings != 1 {
		t.Errorf("POST/PUT/ping = %d/%d/%d, want 1/0/1", harbor.posts, harbor.puts, harbor.pings)
	}
	body := harbor.lastBody
	if body["name"] != "dragonfly" || body["vendor"] != harborPreheatVendor ||
		body["endpoint"] != "https://manager.example.com" || body["auth_mode"] != harborPreheatAuthMode {
		t.Errorf("preheat body = %v", body)
	}
	if body["enabled"] != true || body["default"] != false || body["insecure"] != false {
		t.Errorf("preheat flags = enabled:%v default:%v insecure:%v", body["enabled"], body["default"], body["insecure"])
	}
	auth, _ := body["auth_info"].(map[string]any)
	if auth["token"] != testPreheatToken {
		t.Error("preheat body does not carry the token")
	}
	if len(harbor.instances) != 1 || harbor.instances[0].Endpoint != "https://manager.example.com" {
		t.Errorf("instances = %+v", harbor.instances)
	}
}

func TestEnsurePreheatInstanceUpdates(t *testing.T) {
	client, harbor := newPreheatTestClient(t)
	harbor.instances = []HarborPreheatInstance{{ID: 7, Name: "dragonfly", Vendor: "dragonfly"}}

	if err := client.EnsurePreheatInstance(context.Background(), "dragonfly", "https://manager.example.com", testPreheatToken, false); err != nil {
		t.Fatalf("EnsurePreheatInstance() error = %v", err)
	}
	if harbor.posts != 0 || harbor.puts != 1 || harbor.pings != 1 {
		t.Errorf("POST/PUT/ping = %d/%d/%d, want 0/1/1", harbor.posts, harbor.puts, harbor.pings)
	}
	if harbor.lastBody["id"] != float64(7) {
		t.Errorf("PUT body id = %v, want 7", harbor.lastBody["id"])
	}
}

func TestEnsurePreheatInstanceConflictReListsAndUpdates(t *testing.T) {
	client, harbor := newPreheatTestClient(t)
	harbor.conflictOnce = true

	if err := client.EnsurePreheatInstance(context.Background(), "dragonfly", "https://manager.example.com", testPreheatToken, false); err != nil {
		t.Fatalf("EnsurePreheatInstance() error = %v", err)
	}
	if harbor.posts != 1 || harbor.puts != 1 || harbor.pings != 1 {
		t.Errorf("POST/PUT/ping = %d/%d/%d, want 1/1/1", harbor.posts, harbor.puts, harbor.pings)
	}
}

func TestEnsurePreheatInstancePingFailureKeepsInstance(t *testing.T) {
	client, harbor := newPreheatTestClient(t)
	harbor.pingStatus = http.StatusInternalServerError

	err := client.EnsurePreheatInstance(context.Background(), "dragonfly", "https://manager.example.com", testPreheatToken, false)
	if err == nil {
		t.Fatal("EnsurePreheatInstance() error = nil, want ping failure")
	}
	if strings.Contains(err.Error(), testPreheatToken) {
		t.Error("error contains the token")
	}
	if len(harbor.instances) != 1 {
		t.Errorf("instance was not kept after ping failure: %+v", harbor.instances)
	}
}

func TestEnsurePreheatInstanceRejectsInvalidEndpoint(t *testing.T) {
	client, harbor := newPreheatTestClient(t)

	if err := client.EnsurePreheatInstance(context.Background(), "dragonfly", "manager.example.com", testPreheatToken, false); err == nil {
		t.Fatal("EnsurePreheatInstance() error = nil, want invalid endpoint error")
	}
	if harbor.posts != 0 || harbor.puts != 0 || harbor.pings != 0 {
		t.Errorf("Harbor was called for an invalid endpoint: %+v", harbor)
	}
}

func TestEnsurePreheatInstanceRejectsVendorChange(t *testing.T) {
	client, harbor := newPreheatTestClient(t)
	harbor.instances = []HarborPreheatInstance{{ID: 7, Name: "dragonfly", Vendor: "harbor"}}

	err := client.EnsurePreheatInstance(context.Background(), "dragonfly", "https://manager.example.com", testPreheatToken, false)
	if err == nil {
		t.Fatal("EnsurePreheatInstance() error = nil, want vendor mismatch error")
	}
	if !strings.Contains(err.Error(), "vendor") {
		t.Errorf("error = %v, want vendor mismatch", err)
	}
	if harbor.puts != 0 {
		t.Errorf("PUT calls = %d, want 0", harbor.puts)
	}
}

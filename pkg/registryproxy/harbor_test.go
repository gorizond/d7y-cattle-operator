package registryproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

type fakeHarbor struct {
	mu         sync.Mutex
	registries []HarborRegistry
	projects   []HarborProject
	webhooks   []string
	robots     []HarborRobot
	posts      map[string]int
}

func (f *fakeHarbor) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.posts == nil {
			f.posts = map[string]int{}
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2.0/registries":
			_ = json.NewEncoder(w).Encode(f.registries)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2.0/registries":
			var item HarborRegistry
			_ = json.NewDecoder(r.Body).Decode(&item)
			f.posts["registry"]++
			item.ID = len(f.registries) + 1
			f.registries = append(f.registries, item)
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2.0/projects":
			_ = json.NewEncoder(w).Encode(f.projects)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2.0/projects":
			var item struct {
				ProjectName string `json:"project_name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&item)
			f.posts["project"]++
			f.projects = append(f.projects, HarborProject{ProjectID: len(f.projects) + 1, Name: item.ProjectName})
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2.0/projects/dockerio/webhook/policies":
			var items []map[string]any
			for _, name := range f.webhooks {
				items = append(items, map[string]any{"id": 1, "name": name})
			}
			_ = json.NewEncoder(w).Encode(items)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2.0/projects/dockerio/webhook/policies":
			f.posts["webhook"]++
			f.webhooks = append(f.webhooks, "nydus-convert")
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2.0/robots":
			_ = json.NewEncoder(w).Encode(f.robots)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2.0/robots":
			f.posts["robot"]++
			robot := HarborRobot{Name: "d7y-registry-proxy", Secret: "robot-secret"}
			f.robots = append(f.robots, robot)
			_ = json.NewEncoder(w).Encode(robot)
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	})
}

func TestProvisionerCreatesRegistryProjectMirrorAndWebhook(t *testing.T) {
	fake := &fakeHarbor{}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	provisioner := &Provisioner{
		Harbor:         &HarborClient{BaseURL: server.URL},
		MirrorProject:  "mirror",
		WebhookAddress: "https://proxy.example.com/api/convert",
		WebhookSecret:  "shared",
	}
	if err := provisioner.Ensure(context.Background(), "docker.io"); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if err := provisioner.Ensure(context.Background(), "docker.io"); err != nil {
		t.Fatalf("Ensure(cached) error = %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.posts["registry"] != 1 || fake.posts["project"] != 2 || fake.posts["webhook"] != 1 {
		t.Errorf("Harbor POST counts = %v, want one registry, two projects, one webhook", fake.posts)
	}
}

type fakeSecretStore struct {
	data map[string]map[string][]byte
}

func (f *fakeSecretStore) Get(_ context.Context, name string) (map[string][]byte, bool, error) {
	value, ok := f.data[name]
	return value, ok, nil
}
func (f *fakeSecretStore) Put(_ context.Context, name string, data map[string][]byte) error {
	if f.data == nil {
		f.data = map[string]map[string][]byte{}
	}
	f.data[name] = data
	return nil
}

func TestEnsureRobotSecretCreatesOnceAndReuses(t *testing.T) {
	fake := &fakeHarbor{}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	store := &fakeSecretStore{}
	harbor := &HarborClient{BaseURL: server.URL}
	robot, err := EnsureRobotSecret(context.Background(), harbor, "robot-secret", "mirror", []string{"dockerio"}, store)
	if err != nil {
		t.Fatalf("EnsureRobotSecret() error = %v", err)
	}
	if robot.Name == "" || robot.Secret == "" || store.data["robot-secret"]["password"] == nil {
		t.Error("robot credentials were not persisted")
	}
	if _, err := EnsureRobotSecret(context.Background(), harbor, "robot-secret", "mirror", []string{"dockerio"}, store); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.posts["robot"] != 1 {
		t.Errorf("robot POSTs = %d, want 1", fake.posts["robot"])
	}
}

func TestEnsureRobotSecretUsesExistingCredentials(t *testing.T) {
	store := &fakeSecretStore{data: map[string]map[string][]byte{
		"robot-secret": {"username": []byte("robot$d7y"), "password": []byte("existing")},
	}}
	robot, err := EnsureRobotSecret(context.Background(), &HarborClient{BaseURL: "http://127.0.0.1:1"}, "robot-secret", "mirror", []string{"dockerio"}, store)
	if err != nil {
		t.Fatalf("EnsureRobotSecret() error = %v", err)
	}
	if robot.Name != "robot$d7y" || robot.Secret != "existing" {
		t.Errorf("robot = %+v, want persisted credentials", robot)
	}
}

func TestEnsureProjectMapsDots(t *testing.T) {
	if got := ProjectName("docker.io"); got != "dockerio" {
		t.Errorf("ProjectName() = %q", got)
	}
	if got := ProjectName("ghcr.io"); got != "ghcrio" {
		t.Errorf("ProjectName(ghcr.io) = %q", got)
	}
}

package registryproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeHarbor struct {
	mu         sync.Mutex
	registries []HarborRegistry
	projects   []HarborProject
	webhooks   []string
	// webhookAddress/webhookAuth mirror the nydus-convert policy target.
	webhookAddress string
	webhookAuth    string
	robots         []HarborRobot
	posts          map[string]int
	// lastRegistryBody is the raw create body (checks that private upstream
	// credentials reach Harbor without appearing in logs).
	lastRegistryBody map[string]any
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
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.lastRegistryBody = body
			item := HarborRegistry{Name: fmt.Sprint(body["name"]), Type: fmt.Sprint(body["type"]), URL: fmt.Sprint(body["url"])}
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
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v2.0/projects/") &&
			strings.HasSuffix(r.URL.Path, "/webhook/policies"):
			var items []map[string]any
			for i, name := range f.webhooks {
				item := map[string]any{"id": i + 1, "name": name}
				if f.webhookAddress != "" {
					item["targets"] = []map[string]any{{"address": f.webhookAddress, "auth_header": f.webhookAuth}}
				}
				items = append(items, item)
			}
			_ = json.NewEncoder(w).Encode(items)
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v2.0/projects/") &&
			strings.HasSuffix(r.URL.Path, "/webhook/policies"):
			f.posts["webhook"]++
			f.webhooks = append(f.webhooks, "nydus-convert")
			f.setWebhookTarget(r)
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/v2.0/projects/") &&
			strings.Contains(r.URL.Path, "/webhook/policies/"):
			f.posts["webhook_update"]++
			f.setWebhookTarget(r)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2.0/robots":
			_ = json.NewEncoder(w).Encode(f.robots)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2.0/robots":
			var body struct {
				Name string `json:"name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.posts["robot"]++
			robot := HarborRobot{ID: len(f.robots) + 1, Name: "robot$" + body.Name, Secret: "robot-secret"}
			f.robots = append(f.robots, robot)
			_ = json.NewEncoder(w).Encode(robot)
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v2.0/robots/"):
			id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v2.0/robots/"))
			f.posts["robot_delete"]++
			for i, robot := range f.robots {
				if robot.ID == id {
					f.robots = append(f.robots[:i], f.robots[i+1:]...)
					break
				}
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	})
}

func (f *fakeHarbor) setWebhookTarget(r *http.Request) {
	var body struct {
		Targets []struct {
			Address    string `json:"address"`
			AuthHeader string `json:"auth_header"`
		} `json:"targets"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if len(body.Targets) > 0 {
		f.webhookAddress = body.Targets[0].Address
		f.webhookAuth = body.Targets[0].AuthHeader
	}
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

func TestEnsureWebhookUpdatesChangedTarget(t *testing.T) {
	fake := &fakeHarbor{
		webhooks:       []string{"nydus-convert"},
		webhookAddress: "https://old.example.com/api/convert",
		webhookAuth:    "Bearer old",
	}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	client := &HarborClient{BaseURL: server.URL}
	address := "http://c1-registry-proxy.dragonfly-system.svc:8080/api/convert"

	if err := client.EnsureWebhook(context.Background(), "dockerio", address, "shared"); err != nil {
		t.Fatalf("EnsureWebhook(update) error = %v", err)
	}
	if err := client.EnsureWebhook(context.Background(), "dockerio", address, "shared"); err != nil {
		t.Fatalf("EnsureWebhook(idempotent) error = %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.posts["webhook"] != 0 || fake.posts["webhook_update"] != 1 {
		t.Errorf("webhook POST/PUT = %d/%d, want 0/1", fake.posts["webhook"], fake.posts["webhook_update"])
	}
	if fake.webhookAddress != address || fake.webhookAuth != "Bearer shared" {
		t.Errorf("policy target = %q/%q", fake.webhookAddress, fake.webhookAuth)
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
	username := fmt.Sprintf("robot$d7y-%d", time.Now().UnixNano())
	password := fmt.Sprintf("fixture-%d", time.Now().UnixNano())
	store := &fakeSecretStore{data: map[string]map[string][]byte{
		"robot-secret": {"username": []byte(username), "password": []byte(password)},
	}}
	robot, err := EnsureRobotSecret(context.Background(), &HarborClient{BaseURL: "http://127.0.0.1:1"}, "robot-secret", "mirror", []string{"dockerio"}, store)
	if err != nil {
		t.Fatalf("EnsureRobotSecret() error = %v", err)
	}
	if robot.Name != username || robot.Secret != password {
		t.Errorf("robot = %+v, want persisted credentials", robot)
	}
}

func TestEnsureSystemRobotMatchesPrefixedName(t *testing.T) {
	existingSecret := fmt.Sprintf("fixture-%d", time.Now().UnixNano())
	fake := &fakeHarbor{robots: []HarborRobot{{
		ID: 9, Name: "robot$d7y-registry-proxy", Secret: existingSecret,
	}}}
	server := httptest.NewServer(fake.handler())
	defer server.Close()

	robot, err := (&HarborClient{BaseURL: server.URL}).EnsureSystemRobot(
		context.Background(), "d7y-registry-proxy", []string{"dockerio"}, "mirror")
	if err != nil {
		t.Fatalf("EnsureSystemRobot() error = %v", err)
	}
	if robot.Name != "robot$d7y-registry-proxy" || robot.Secret != existingSecret {
		t.Errorf("robot = %+v, want the existing robot", robot)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.posts["robot"] != 0 || fake.posts["robot_delete"] != 0 {
		t.Errorf("robot POST/DELETE = %d/%d, want 0/0", fake.posts["robot"], fake.posts["robot_delete"])
	}
}

func TestEnsureSystemRobotRecreatesSecretlessRobot(t *testing.T) {
	fake := &fakeHarbor{robots: []HarborRobot{{
		ID: 7, Name: "robot$d7y-registry-proxy",
	}}}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	harbor := &HarborClient{BaseURL: server.URL}

	robot, err := harbor.EnsureSystemRobot(context.Background(), "d7y-registry-proxy", []string{"dockerio"}, "mirror")
	if err != nil {
		t.Fatalf("EnsureSystemRobot() error = %v", err)
	}
	if robot.Name != "robot$d7y-registry-proxy" || robot.Secret == "" {
		t.Errorf("robot = %+v, want recreated robot with a secret", robot)
	}
	// A second call reuses the recreated robot instead of deleting it again.
	if _, err := harbor.EnsureSystemRobot(context.Background(), "d7y-registry-proxy", []string{"dockerio"}, "mirror"); err != nil {
		t.Fatalf("EnsureSystemRobot(idempotent) error = %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.posts["robot_delete"] != 1 || fake.posts["robot"] != 1 {
		t.Errorf("robot DELETE/POST = %d/%d, want 1/1", fake.posts["robot_delete"], fake.posts["robot"])
	}
	if len(fake.robots) != 1 {
		t.Errorf("robots = %+v, want exactly one", fake.robots)
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

func TestEnsureRegistrySendsPrivateUpstreamCredentials(t *testing.T) {
	accessKey := fmt.Sprintf("test-user-%d", time.Now().UnixNano())
	accessSecret := fmt.Sprintf("test-secret-%d", time.Now().UnixNano())
	fake := &fakeHarbor{}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	client := &HarborClient{
		BaseURL: server.URL,
		Credentials: map[string]HarborCredential{
			"registry.gitlab.com": {AccessKey: accessKey, AccessSecret: accessSecret},
		},
	}
	if _, err := client.EnsureRegistry(context.Background(), "registry.gitlab.com"); err != nil {
		t.Fatalf("EnsureRegistry() error = %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.lastRegistryBody["access_key"] != accessKey || fake.lastRegistryBody["access_secret"] != accessSecret {
		t.Errorf("registry body = %v", fake.lastRegistryBody)
	}
	if fake.lastRegistryBody["name"] != "registry.gitlab.com" {
		t.Errorf("registry name = %v", fake.lastRegistryBody["name"])
	}
}

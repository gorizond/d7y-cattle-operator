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
	// webhookFail/registryFail simulate optional and required Harbor failures.
	webhookFail  bool
	registryFail bool
	// registryUpdateFail simulates a Harbor that rejects registry updates.
	registryUpdateFail bool
	// projectUpdateFail simulates a Harbor that refuses to re-point a
	// proxy-cache project.
	projectUpdateFail bool
	// lastRegistryBody/lastRobotBody are raw create bodies (checks that
	// credentials and wildcard permissions reach Harbor).
	lastRegistryBody map[string]any
	lastRobotBody    map[string]any
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
			f.posts["registry"]++
			if f.registryFail {
				http.Error(w, "harbor registry health check failed", http.StatusInternalServerError)
				return
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.lastRegistryBody = body
			item := HarborRegistry{Name: fmt.Sprint(body["name"]), Type: fmt.Sprint(body["type"]), URL: fmt.Sprint(body["url"])}
			item.Insecure, _ = body["insecure"].(bool)
			item.ID = len(f.registries) + 1
			f.registries = append(f.registries, item)
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/v2.0/registries/"):
			f.posts["registry_update"]++
			if f.registryUpdateFail {
				http.Error(w, "harbor registry update failed", http.StatusInternalServerError)
				return
			}
			id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v2.0/registries/"))
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.lastRegistryBody = body
			// Harbor 2.15.4 ignores the type field on PUT and answers 200
			// with the old type; only the URL and insecure flag change.
			for i := range f.registries {
				if f.registries[i].ID == id {
					f.registries[i].URL = fmt.Sprint(body["url"])
					f.registries[i].Insecure, _ = body["insecure"].(bool)
				}
			}
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v2.0/registries/"):
			id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v2.0/registries/"))
			f.posts["registry_delete"]++
			for _, project := range f.projects {
				if project.RegistryID == id {
					http.Error(w, `{"errors":[{"code":"PRECONDITION","message":"the registry is referenced by a project"}]}`, http.StatusPreconditionFailed)
					return
				}
			}
			for i, item := range f.registries {
				if item.ID == id {
					f.registries = append(f.registries[:i], f.registries[i+1:]...)
					break
				}
			}
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2.0/projects":
			_ = json.NewEncoder(w).Encode(f.projects)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2.0/projects":
			var item struct {
				ProjectName string `json:"project_name"`
				RegistryID  int    `json:"registry_id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&item)
			f.posts["project"]++
			f.projects = append(f.projects, HarborProject{ProjectID: len(f.projects) + 1, Name: item.ProjectName, RegistryID: item.RegistryID})
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
			if f.webhookFail {
				http.Error(w, "harbor webhook policy is broken", http.StatusInternalServerError)
				return
			}
			f.posts["webhook"]++
			f.webhooks = append(f.webhooks, "nydus-convert")
			f.setWebhookTarget(r)
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/v2.0/projects/") &&
			strings.Contains(r.URL.Path, "/webhook/policies/"):
			f.posts["webhook_update"]++
			f.setWebhookTarget(r)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/v2.0/projects/"):
			f.posts["project_update"]++
			if f.projectUpdateFail {
				http.Error(w, "harbor project registry cannot be changed", http.StatusBadRequest)
				return
			}
			name := strings.TrimPrefix(r.URL.Path, "/api/v2.0/projects/")
			var body struct {
				RegistryID int `json:"registry_id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			for i := range f.projects {
				if f.projects[i].Name == name {
					f.projects[i].RegistryID = body.RegistryID
				}
			}
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2.0/robots":
			_ = json.NewEncoder(w).Encode(f.robots)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2.0/robots":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.lastRobotBody = body
			f.posts["robot"]++
			name, _ := body["name"].(string)
			robot := HarborRobot{ID: len(f.robots) + 1, Name: "robot$" + name, Secret: "robot-secret"}
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
	fingerprint := robotFingerprint(systemRobotName, "mirror", []string{"dockerio"})
	store := &fakeSecretStore{data: map[string]map[string][]byte{
		"robot-secret": {
			"username":                []byte(username),
			"password":                []byte(password),
			"permissions-fingerprint": []byte(fingerprint),
		},
	}}
	fake := &fakeHarbor{robots: []HarborRobot{{ID: 9, Name: "robot$" + systemRobotName, Secret: password}}}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	robot, err := EnsureRobotSecret(context.Background(), &HarborClient{BaseURL: server.URL}, "robot-secret", "mirror", []string{"dockerio"}, store)
	if err != nil {
		t.Fatalf("EnsureRobotSecret() error = %v", err)
	}
	if robot.Name != username || robot.Secret != password {
		t.Errorf("robot = %+v, want persisted credentials", robot)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.posts["robot"] != 0 || fake.posts["robot_delete"] != 0 {
		t.Errorf("robot POST/DELETE = %d/%d, want 0/0", fake.posts["robot"], fake.posts["robot_delete"])
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

func TestEnsureRegistryRefreshesCredentialsOnMatchingURL(t *testing.T) {
	accessKey := fmt.Sprintf("test-user-%d", time.Now().UnixNano())
	accessSecret := fmt.Sprintf("test-secret-%d", time.Now().UnixNano())
	fake := &fakeHarbor{registries: []HarborRegistry{{
		ID: 2, Name: "registry.gitlab.com", Type: "docker-registry", URL: "https://registry.gitlab.com",
	}}}
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
	if fake.posts["registry_update"] != 1 || fake.posts["registry"] != 0 || fake.posts["registry_delete"] != 0 {
		t.Errorf("registry POST/PUT/DELETE = %d/%d/%d, want 0/1/0",
			fake.posts["registry"], fake.posts["registry_update"], fake.posts["registry_delete"])
	}
	if fake.lastRegistryBody["access_key"] != accessKey || fake.lastRegistryBody["access_secret"] != accessSecret {
		t.Errorf("matching URL update did not include configured credentials: %v", fake.lastRegistryBody)
	}
	if fake.lastRegistryBody["url"] != "https://registry.gitlab.com" {
		t.Errorf("update body url = %v", fake.lastRegistryBody["url"])
	}
}

func TestEnsureSystemRobotUsesWildcardPull(t *testing.T) {
	fake := &fakeHarbor{}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	if _, err := (&HarborClient{BaseURL: server.URL}).EnsureSystemRobot(
		context.Background(), "d7y-registry-proxy", []string{"*"}, "mirror"); err != nil {
		t.Fatalf("EnsureSystemRobot() error = %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	permissions, _ := fake.lastRobotBody["permissions"].([]any)
	if len(permissions) != 2 {
		t.Fatalf("permissions = %v", fake.lastRobotBody["permissions"])
	}
	first, _ := permissions[0].(map[string]any)
	if first["kind"] != "project" || first["namespace"] != "*" {
		t.Errorf("wildcard permission = %v", first)
	}
}

func TestEnsureSystemRobotGrantMatrix(t *testing.T) {
	fake := &fakeHarbor{}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	if _, err := (&HarborClient{BaseURL: server.URL}).EnsureSystemRobot(
		context.Background(), "d7y-registry-proxy", []string{"dockerio"}, "mirror"); err != nil {
		t.Fatalf("EnsureSystemRobot() error = %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	permissions, _ := fake.lastRobotBody["permissions"].([]any)
	grants := map[string]map[string]bool{}
	for _, raw := range permissions {
		permission, _ := raw.(map[string]any)
		namespace, _ := permission["namespace"].(string)
		if grants[namespace] == nil {
			grants[namespace] = map[string]bool{}
		}
		access, _ := permission["access"].([]any)
		for _, entry := range access {
			item, _ := entry.(map[string]any)
			resource, _ := item["resource"].(string)
			action, _ := item["action"].(string)
			grants[namespace][resource+":"+action] = true
		}
	}
	if !grants["dockerio"]["repository:pull"] || grants["dockerio"]["repository:push"] {
		t.Errorf("source project grants = %v, want pull only", grants["dockerio"])
	}
	if !grants["mirror"]["repository:pull"] || !grants["mirror"]["repository:push"] || !grants["mirror"]["tag:create"] {
		t.Errorf("mirror grants = %v, want repository pull/push and tag create", grants["mirror"])
	}
}

func TestEnsureRobotSecretRecreatesOnPermissionChange(t *testing.T) {
	fake := &fakeHarbor{}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	store := &fakeSecretStore{}
	harbor := &HarborClient{BaseURL: server.URL}

	if _, err := EnsureRobotSecret(context.Background(), harbor, "robot-secret", "mirror", []string{"dockerio"}, store); err != nil {
		t.Fatalf("EnsureRobotSecret(first) error = %v", err)
	}
	firstFingerprint := string(store.data["robot-secret"]["permissions-fingerprint"])
	if firstFingerprint == "" {
		t.Fatal("permission fingerprint was not stored")
	}
	// Same grants reuse the stored credentials.
	if _, err := EnsureRobotSecret(context.Background(), harbor, "robot-secret", "mirror", []string{"dockerio"}, store); err != nil {
		t.Fatalf("EnsureRobotSecret(same grants) error = %v", err)
	}
	// A new source project invalidates the stored credentials; the stale robot
	// must be deleted and recreated with the new grants.
	if _, err := EnsureRobotSecret(context.Background(), harbor, "robot-secret", "mirror", []string{"dockerio", "ghcrio"}, store); err != nil {
		t.Fatalf("EnsureRobotSecret(new grants) error = %v", err)
	}
	if got := string(store.data["robot-secret"]["permissions-fingerprint"]); got == firstFingerprint || got == "" {
		t.Errorf("fingerprint after permission change = %q", got)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.posts["robot"] != 2 || fake.posts["robot_delete"] != 1 {
		t.Errorf("robot POST/DELETE = %d/%d, want 2/1", fake.posts["robot"], fake.posts["robot_delete"])
	}
}

func TestEnsureRegistryMapsQuayToDockerRegistry(t *testing.T) {
	fake := &fakeHarbor{}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	if _, err := (&HarborClient{BaseURL: server.URL}).EnsureRegistry(context.Background(), "quay.io"); err != nil {
		t.Fatalf("EnsureRegistry() error = %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if got := fake.lastRegistryBody["type"]; got != "docker-registry" {
		t.Errorf("quay registry type = %v, want the generic docker-registry adapter", got)
	}
}

func TestRegistrySpec(t *testing.T) {
	tests := []struct {
		host string
		typ  string
		url  string
	}{
		{"docker.io", "docker-hub", "https://hub.docker.com"},
		{"ghcr.io", "docker-registry", "https://ghcr.io"},
		{"quay.io", "docker-registry", "https://quay.io"},
		{"registry.gitlab.com", "docker-registry", "https://registry.gitlab.com"},
		{"gcr.io", "docker-registry", "https://gcr.io"},
		{"public.ecr.aws", "docker-registry", "https://public.ecr.aws"},
		{"mcr.microsoft.com", "docker-registry", "https://mcr.microsoft.com"},
		{"registry.k8s.io", "docker-registry", "https://registry.k8s.io"},
		{"nvcr.io", "docker-registry", "https://nvcr.io"},
		{"registry.example.com:5000", "docker-registry", "https://registry.example.com:5000"},
	}
	for _, test := range tests {
		spec := registrySpec(test.host)
		if spec.Name != test.host || spec.Type != test.typ || spec.URL != test.url {
			t.Errorf("registrySpec(%q) = %+v, want type %q url %q", test.host, spec, test.typ, test.url)
		}
	}
}

func TestEnsureRegistryDockerHubUsesDockerHubAdapter(t *testing.T) {
	fake := &fakeHarbor{}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	if _, err := (&HarborClient{BaseURL: server.URL}).EnsureRegistry(context.Background(), "docker.io"); err != nil {
		t.Fatalf("EnsureRegistry() error = %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	// The generic adapter health check against https://hub.docker.com returns
	// 404, so Docker Hub must keep the dedicated docker-hub type.
	if fake.lastRegistryBody["type"] != "docker-hub" || fake.lastRegistryBody["url"] != "https://hub.docker.com" {
		t.Errorf("docker.io registry body = %v", fake.lastRegistryBody)
	}
	if _, ok := fake.lastRegistryBody["access_key"]; ok {
		t.Errorf("unconfigured registry create sent credentials: %v", fake.lastRegistryBody)
	}
}

func TestEnsureRegistryTypeMismatchCreatesGenericEndpoint(t *testing.T) {
	fake := &fakeHarbor{
		registries: []HarborRegistry{{ID: 3, Name: "quay.io", Type: "quay", URL: "https://quay.io"}},
		// A proxy-cache project still references the stale endpoint: DELETE
		// must never be attempted (Harbor answers 412) and PUT must not send a
		// different type (Harbor ignores it and keeps the old type).
		projects: []HarborProject{{ProjectID: 1, Name: "quayio", RegistryID: 3}},
	}
	server := httptest.NewServer(fake.handler())
	defer server.Close()

	client := &HarborClient{
		BaseURL: server.URL,
		Credentials: map[string]HarborCredential{
			"quay.io": {AccessKey: "test-user", AccessSecret: "test-secret"},
		},
	}
	got, err := client.EnsureRegistry(context.Background(), "quay.io")
	if err != nil {
		t.Fatalf("EnsureRegistry() error = %v", err)
	}
	if got.Name != "quay.io-generic" || got.Type != "docker-registry" || got.URL != "https://quay.io" {
		t.Errorf("generic endpoint = %+v", got)
	}
	if got.ID == 3 {
		t.Error("generic endpoint reused the stale endpoint ID")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.posts["registry"] != 1 || fake.posts["registry_update"] != 0 || fake.posts["registry_delete"] != 0 {
		t.Errorf("registry POST/PUT/DELETE = %d/%d/%d, want 1/0/0",
			fake.posts["registry"], fake.posts["registry_update"], fake.posts["registry_delete"])
	}
	if fake.lastRegistryBody["access_key"] != "test-user" || fake.lastRegistryBody["access_secret"] != "test-secret" {
		t.Errorf("generic endpoint create did not include configured credentials: %v", fake.lastRegistryBody)
	}
	if len(fake.registries) != 2 {
		t.Fatalf("registries = %+v, want the untouched stale endpoint and the new generic one", fake.registries)
	}
	if fake.registries[0].Name != "quay.io" || fake.registries[0].Type != "quay" {
		t.Errorf("stale endpoint was modified: %+v", fake.registries[0])
	}
}

func TestEnsureRegistryTypeMismatchCreateFailureIsActionable(t *testing.T) {
	fake := &fakeHarbor{
		registries:   []HarborRegistry{{ID: 3, Name: "quay.io", Type: "quay", URL: "https://quay.io"}},
		projects:     []HarborProject{{ProjectID: 1, Name: "quayio", RegistryID: 3}},
		registryFail: true,
	}
	server := httptest.NewServer(fake.handler())
	defer server.Close()

	_, err := (&HarborClient{BaseURL: server.URL}).EnsureRegistry(context.Background(), "quay.io")
	if err == nil {
		t.Fatal("EnsureRegistry() error = nil")
	}
	for _, want := range []string{"quay.io-generic", "quay"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.posts["registry_delete"] != 0 || fake.posts["registry_update"] != 0 {
		t.Errorf("failing type reconcile attempted DELETE/PUT = %d/%d, want 0/0",
			fake.posts["registry_delete"], fake.posts["registry_update"])
	}
	if fake.posts["registry"] != 1 {
		t.Errorf("registry POST attempts = %d, want exactly one (no loop)", fake.posts["registry"])
	}
}

func TestEnsureRegistryURLMismatchUpdatesExactlyOnce(t *testing.T) {
	fake := &fakeHarbor{registries: []HarborRegistry{{
		ID: 7, Name: "ghcr.io", Type: "docker-registry", URL: "http://ghcr.io",
	}}}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	client := &HarborClient{
		BaseURL: server.URL,
		Credentials: map[string]HarborCredential{
			"ghcr.io": {AccessKey: "test-user", AccessSecret: "test-secret"},
		},
	}

	got, err := client.EnsureRegistry(context.Background(), "ghcr.io")
	if err != nil {
		t.Fatalf("EnsureRegistry() error = %v", err)
	}
	if got.URL != "https://ghcr.io" || got.Type != "docker-registry" {
		t.Errorf("reconciled registry = %+v", got)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.posts["registry_update"] != 1 || fake.posts["registry"] != 0 || fake.posts["registry_delete"] != 0 {
		t.Errorf("registry POST/PUT/DELETE = %d/%d/%d, want 0/1/0",
			fake.posts["registry"], fake.posts["registry_update"], fake.posts["registry_delete"])
	}
	// A URL-only reconcile must not try to change the immutable type.
	if fake.lastRegistryBody["type"] != "docker-registry" || fake.lastRegistryBody["url"] != "https://ghcr.io" {
		t.Errorf("update body = %v", fake.lastRegistryBody)
	}
	if fake.lastRegistryBody["access_key"] != "test-user" || fake.lastRegistryBody["access_secret"] != "test-secret" {
		t.Errorf("update did not include configured credentials: %v", fake.lastRegistryBody)
	}
}

func TestEnsureRegistryURLReconcileFailureKeepsExistingEndpoint(t *testing.T) {
	fake := &fakeHarbor{
		registries:         []HarborRegistry{{ID: 7, Name: "ghcr.io", Type: "docker-registry", URL: "http://ghcr.io"}},
		registryUpdateFail: true,
	}
	server := httptest.NewServer(fake.handler())
	defer server.Close()

	got, err := (&HarborClient{BaseURL: server.URL}).EnsureRegistry(context.Background(), "ghcr.io")
	if err != nil {
		t.Fatalf("EnsureRegistry() error = %v, want nil (best-effort reconcile)", err)
	}
	if got.URL != "http://ghcr.io" || got.Name != "ghcr.io" {
		t.Errorf("existing endpoint = %+v", got)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.posts["registry_update"] != 1 || fake.posts["registry_delete"] != 0 || fake.posts["registry"] != 0 {
		t.Errorf("registry POST/PUT/DELETE = %d/%d/%d, want 0/1/0",
			fake.posts["registry"], fake.posts["registry_update"], fake.posts["registry_delete"])
	}
}

func TestEnsureRegistryLeavesMatchingEndpointsUntouched(t *testing.T) {
	fake := &fakeHarbor{registries: []HarborRegistry{
		{ID: 1, Name: "docker.io", Type: "docker-hub", URL: "https://hub.docker.com"},
		{ID: 2, Name: "ghcr.io", Type: "docker-registry", URL: "https://ghcr.io"},
		{ID: 3, Name: "registry.gitlab.com", Type: "docker-registry", URL: "https://registry.gitlab.com"},
	}}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	client := &HarborClient{BaseURL: server.URL}

	for _, host := range []string{"docker.io", "ghcr.io", "registry.gitlab.com"} {
		if _, err := client.EnsureRegistry(context.Background(), host); err != nil {
			t.Fatalf("EnsureRegistry(%s) error = %v", host, err)
		}
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.posts["registry"] != 0 || fake.posts["registry_update"] != 0 || fake.posts["registry_delete"] != 0 {
		t.Errorf("matching endpoints were mutated: %v", fake.posts)
	}
}

func TestEnsureProjectRepointsRegistry(t *testing.T) {
	fake := &fakeHarbor{projects: []HarborProject{{ProjectID: 1, Name: "quayio", RegistryID: 3}}}
	server := httptest.NewServer(fake.handler())
	defer server.Close()

	project, err := (&HarborClient{BaseURL: server.URL}).EnsureProject(context.Background(), "quayio", 4)
	if err != nil {
		t.Fatalf("EnsureProject() error = %v", err)
	}
	if project.Name != "quayio" || project.RegistryID != 4 {
		t.Errorf("project = %+v", project)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.posts["project_update"] != 1 || fake.posts["project"] != 0 {
		t.Errorf("project POST/PUT = %d/%d, want 0/1", fake.posts["project"], fake.posts["project_update"])
	}
}

func TestEnsureProjectFallsBackWhenRepointRefused(t *testing.T) {
	fake := &fakeHarbor{
		projects:          []HarborProject{{ProjectID: 1, Name: "quayio", RegistryID: 3}},
		projectUpdateFail: true,
	}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	client := &HarborClient{BaseURL: server.URL}

	project, err := client.EnsureProject(context.Background(), "quayio", 4)
	if err != nil {
		t.Fatalf("EnsureProject() error = %v", err)
	}
	if project.Name != "quayio-generic" || project.RegistryID != 4 {
		t.Errorf("fallback project = %+v", project)
	}
	// A second call reuses the deterministic fallback without new writes.
	if _, err := client.EnsureProject(context.Background(), "quayio", 4); err != nil {
		t.Fatalf("EnsureProject(idempotent) error = %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.posts["project_update"] != 1 || fake.posts["project"] != 1 {
		t.Errorf("project POST/PUT = %d/%d, want 1/1", fake.posts["project"], fake.posts["project_update"])
	}
}

func TestProvisionerUsesGenericProjectForStaleEndpoint(t *testing.T) {
	fake := &fakeHarbor{
		registries:        []HarborRegistry{{ID: 3, Name: "quay.io", Type: "quay", URL: "https://quay.io"}},
		projects:          []HarborProject{{ProjectID: 1, Name: "quayio", RegistryID: 3}},
		projectUpdateFail: true,
	}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	provisioner := &Provisioner{
		Harbor:         &HarborClient{BaseURL: server.URL},
		MirrorProject:  "mirror",
		WebhookAddress: "https://proxy.example.test/api/convert",
		WebhookSecret:  "shared",
	}
	if err := provisioner.Ensure(context.Background(), "quay.io"); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if got := provisioner.ProjectFor("quay.io"); got != "quayio-generic" {
		t.Errorf("ProjectFor(quay.io) = %q, want quayio-generic", got)
	}
	if got := provisioner.RegistryForProject("quayio-generic"); got != "quay.io" {
		t.Errorf("RegistryForProject(quayio-generic) = %q", got)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	var generic *HarborProject
	for i := range fake.projects {
		if fake.projects[i].Name == "quayio-generic" {
			generic = &fake.projects[i]
		}
	}
	if generic == nil || generic.RegistryID == 0 || generic.RegistryID == 3 {
		t.Errorf("generic project was not created against the new endpoint: %+v", fake.projects)
	}
}

func TestHarborBackendUsesProvisionedGenericProject(t *testing.T) {
	source := newFakeRegistry()
	body := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:config"},"layers":[]}`)
	source.manifests["quayio-generic/skopeo/stable:1.16.0"] = Manifest{
		Digest: "sha256:manifest", MediaType: ociManifestType, Body: body,
	}
	backend := &HarborBackend{
		Source:        source,
		MirrorProject: "mirror",
		Provisioner:   &Provisioner{projectFor: map[string]string{"quay.io": "quayio-generic"}},
	}
	manifest, err := backend.GetManifest(context.Background(), "quay.io", "skopeo/stable", "1.16.0")
	if err != nil {
		t.Fatalf("GetManifest() error = %v", err)
	}
	if manifest.Digest != "sha256:manifest" {
		t.Errorf("manifest digest = %q", manifest.Digest)
	}
}

func TestProvisionerWebhookFailureIsNonFatal(t *testing.T) {
	fake := &fakeHarbor{webhookFail: true}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	provisioner := &Provisioner{
		Harbor:         &HarborClient{BaseURL: server.URL},
		MirrorProject:  "mirror",
		WebhookAddress: "https://proxy.example.test/api/convert",
		WebhookSecret:  "shared",
	}
	if err := provisioner.Ensure(context.Background(), "docker.io"); err != nil {
		t.Fatalf("Ensure() with a broken webhook = %v, want nil", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.posts["registry"] != 1 || fake.posts["project"] != 2 {
		t.Errorf("required Harbor objects = %v", fake.posts)
	}
}

func TestProvisionerRegistryFailureIsFatal(t *testing.T) {
	fake := &fakeHarbor{registryFail: true}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	provisioner := &Provisioner{
		Harbor:         &HarborClient{BaseURL: server.URL},
		MirrorProject:  "mirror",
		WebhookAddress: "https://proxy.example.test/api/convert",
		WebhookSecret:  "shared",
	}
	err := provisioner.Ensure(context.Background(), "docker.io")
	if err == nil {
		t.Fatal("Ensure() error = nil for a failing registry creation")
	}
	if !strings.Contains(err.Error(), "Harbor registry") {
		t.Errorf("Ensure() error = %q, want clear registry context", err)
	}
}

func TestEnsureRobotSecretRecreatesMissingRobot(t *testing.T) {
	fingerprint := robotFingerprint(systemRobotName, "mirror", []string{"dockerio"})
	store := &fakeSecretStore{data: map[string]map[string][]byte{
		"robot-secret": {
			"username":                []byte("robot$stale"),
			"password":                []byte("stale"),
			"permissions-fingerprint": []byte(fingerprint),
		},
	}}
	fake := &fakeHarbor{} // Harbor was reinstalled: no robots exist.
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	robot, err := EnsureRobotSecret(context.Background(), &HarborClient{BaseURL: server.URL},
		"robot-secret", "mirror", []string{"dockerio"}, store)
	if err != nil {
		t.Fatalf("EnsureRobotSecret() error = %v", err)
	}
	if robot.Secret == "stale" || robot.Secret == "" {
		t.Errorf("robot secret was not recreated: %+v", robot)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.posts["robot"] != 1 {
		t.Errorf("robot POST = %d, want 1", fake.posts["robot"])
	}
}

func TestCleanupStaleRegistryDeletesUnreferencedEndpoint(t *testing.T) {
	fake := &fakeHarbor{
		registries: []HarborRegistry{
			{ID: 4, Name: "quay.io", Type: "quay", URL: "https://quay.io"},
			{ID: 13, Name: "quay.io-generic", Type: "docker-registry", URL: "https://quay.io"},
		},
		projects: []HarborProject{{ProjectID: 1, Name: "quayio", RegistryID: 13}},
	}
	server := httptest.NewServer(fake.handler())
	defer server.Close()

	if err := (&HarborClient{BaseURL: server.URL}).CleanupStaleRegistry(context.Background(), "quay.io"); err != nil {
		t.Fatalf("CleanupStaleRegistry() error = %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.posts["registry_delete"] != 1 {
		t.Errorf("registry DELETE = %d, want 1", fake.posts["registry_delete"])
	}
	if len(fake.registries) != 1 || fake.registries[0].Name != "quay.io-generic" {
		t.Errorf("registries = %+v, want only the generic endpoint", fake.registries)
	}
}

func TestCleanupStaleRegistryKeepsReferencedEndpoint(t *testing.T) {
	fake := &fakeHarbor{
		registries: []HarborRegistry{
			{ID: 4, Name: "quay.io", Type: "quay", URL: "https://quay.io"},
			{ID: 13, Name: "quay.io-generic", Type: "docker-registry", URL: "https://quay.io"},
		},
		// The project still points at the stale endpoint: Harbor answers 412.
		projects: []HarborProject{{ProjectID: 1, Name: "quayio", RegistryID: 4}},
	}
	server := httptest.NewServer(fake.handler())
	defer server.Close()

	if err := (&HarborClient{BaseURL: server.URL}).CleanupStaleRegistry(context.Background(), "quay.io"); err != nil {
		t.Fatalf("CleanupStaleRegistry() error = %v, want nil on 412", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.posts["registry_delete"] != 1 {
		t.Errorf("registry DELETE = %d, want one attempt", fake.posts["registry_delete"])
	}
	if len(fake.registries) != 2 || fake.registries[0].Name != "quay.io" {
		t.Errorf("referenced stale endpoint was deleted: %+v", fake.registries)
	}
}

func TestProvisionerCleansStaleEndpointAfterRepoint(t *testing.T) {
	fake := &fakeHarbor{
		registries: []HarborRegistry{{ID: 3, Name: "quay.io", Type: "quay", URL: "https://quay.io"}},
		projects:   []HarborProject{{ProjectID: 1, Name: "quayio", RegistryID: 3}},
	}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	provisioner := &Provisioner{
		Harbor:        &HarborClient{BaseURL: server.URL},
		MirrorProject: "mirror",
	}
	if err := provisioner.Ensure(context.Background(), "quay.io"); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.posts["registry_delete"] != 1 {
		t.Errorf("registry DELETE = %d, want the unreferenced stale endpoint removed", fake.posts["registry_delete"])
	}
	var generic *HarborRegistry
	for i := range fake.registries {
		if fake.registries[i].Name == "quay.io" {
			t.Errorf("stale endpoint still present: %+v", fake.registries[i])
		}
		if fake.registries[i].Name == "quay.io-generic" {
			generic = &fake.registries[i]
		}
	}
	if generic == nil || generic.Type != "docker-registry" {
		t.Errorf("generic endpoint = %+v", generic)
	}
	if generic != nil {
		for _, project := range fake.projects {
			if project.Name == "quayio" && project.RegistryID != generic.ID {
				t.Errorf("project registry_id = %d, want %d", project.RegistryID, generic.ID)
			}
		}
	}
}

func TestCleanupStaleRegistryIgnoresMatchingAndGenericEndpoints(t *testing.T) {
	fake := &fakeHarbor{registries: []HarborRegistry{
		{ID: 1, Name: "quay.io-generic", Type: "docker-registry", URL: "https://quay.io"},
		{ID: 2, Name: "ghcr.io", Type: "docker-registry", URL: "https://ghcr.io"},
	}}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	client := &HarborClient{BaseURL: server.URL}
	for _, host := range []string{"quay.io-generic", "ghcr.io"} {
		if err := client.CleanupStaleRegistry(context.Background(), host); err != nil {
			t.Fatalf("CleanupStaleRegistry(%s) error = %v", host, err)
		}
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.posts["registry_delete"] != 0 {
		t.Errorf("registry DELETE = %d, want 0 for matching/generic endpoints", fake.posts["registry_delete"])
	}
}

func TestEnsureRegistryKeepsCompatibleGitHubEndpoint(t *testing.T) {
	fake := &fakeHarbor{
		registries: []HarborRegistry{{ID: 3, Name: "ghcr.io", Type: "github-ghcr", URL: "https://ghcr.io"}},
		projects:   []HarborProject{{ProjectID: 1, Name: "ghcrio", RegistryID: 3}},
	}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	got, err := (&HarborClient{BaseURL: server.URL}).EnsureRegistry(context.Background(), "ghcr.io")
	if err != nil {
		t.Fatalf("EnsureRegistry() error = %v", err)
	}
	if got.ID != 3 || got.Type != "github-ghcr" {
		t.Errorf("endpoint = %+v, want the existing github-ghcr endpoint", got)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.posts["registry"] != 0 || fake.posts["registry_update"] != 0 || fake.posts["registry_delete"] != 0 {
		t.Errorf("registry POST/PUT/DELETE = %d/%d/%d, want 0/0/0",
			fake.posts["registry"], fake.posts["registry_update"], fake.posts["registry_delete"])
	}
}

func TestProvisionerRemovesUnusedGenericForGHCR(t *testing.T) {
	fake := &fakeHarbor{
		registries: []HarborRegistry{
			{ID: 3, Name: "ghcr.io", Type: "github-ghcr", URL: "https://ghcr.io"},
			{ID: 14, Name: "ghcr.io-generic", Type: "docker-registry", URL: "https://ghcr.io"},
		},
		projects: []HarborProject{{ProjectID: 1, Name: "ghcrio", RegistryID: 3}},
	}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	provisioner := &Provisioner{Harbor: &HarborClient{BaseURL: server.URL}, MirrorProject: "mirror"}
	if err := provisioner.Ensure(context.Background(), "ghcr.io"); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.posts["project_update"] != 0 {
		t.Errorf("project PUT = %d, want 0 (the project already uses the canonical endpoint)", fake.posts["project_update"])
	}
	if fake.posts["registry_delete"] != 1 {
		t.Errorf("registry DELETE = %d, want the unused generic endpoint removed", fake.posts["registry_delete"])
	}
	for _, registry := range fake.registries {
		if registry.Name == "ghcr.io-generic" {
			t.Errorf("unused generic endpoint still present: %+v", registry)
		}
	}
	if len(fake.registries) != 1 || fake.registries[0].Name != "ghcr.io" || fake.registries[0].Type != "github-ghcr" {
		t.Errorf("registries = %+v, want only the working github-ghcr endpoint", fake.registries)
	}
	for _, project := range fake.projects {
		if project.Name == "ghcrio" && project.RegistryID != 3 {
			t.Errorf("ghcrio registry_id = %d, want 3", project.RegistryID)
		}
	}
}

func TestEnsureRegistryReusesExistingGenericWhenCanonicalMissing(t *testing.T) {
	fake := &fakeHarbor{
		registries: []HarborRegistry{{ID: 13, Name: "quay.io-generic", Type: "docker-registry", URL: "https://quay.io"}},
		projects:   []HarborProject{{ProjectID: 1, Name: "quayio", RegistryID: 13}},
	}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	provisioner := &Provisioner{Harbor: &HarborClient{BaseURL: server.URL}, MirrorProject: "mirror"}
	if err := provisioner.Ensure(context.Background(), "quay.io"); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if got := provisioner.ProjectFor("quay.io"); got != "quayio" {
		t.Errorf("ProjectFor(quay.io) = %q", got)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.posts["registry"] != 0 {
		t.Errorf("registry POST = %d, want no recreated canonical endpoint", fake.posts["registry"])
	}
	if fake.posts["registry_delete"] != 0 {
		t.Errorf("registry DELETE = %d, want 0 while the generic endpoint is in use", fake.posts["registry_delete"])
	}
	if len(fake.registries) != 1 || fake.registries[0].Name != "quay.io-generic" {
		t.Errorf("registries = %+v, want the existing generic endpoint", fake.registries)
	}
}

func TestCleanupUnusedGenericKeepsReferencedGeneric(t *testing.T) {
	fake := &fakeHarbor{
		registries: []HarborRegistry{
			{ID: 3, Name: "ghcr.io", Type: "github-ghcr", URL: "https://ghcr.io"},
			{ID: 14, Name: "ghcr.io-generic", Type: "docker-registry", URL: "https://ghcr.io"},
		},
		// The fallback project still uses the generic endpoint: Harbor 412.
		projects: []HarborProject{{ProjectID: 2, Name: "ghcrio-generic", RegistryID: 14}},
	}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	if err := (&HarborClient{BaseURL: server.URL}).CleanupUnusedGenericRegistry(context.Background(), "ghcr.io"); err != nil {
		t.Fatalf("CleanupUnusedGenericRegistry() error = %v, want nil on 412", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.posts["registry_delete"] != 1 {
		t.Errorf("registry DELETE = %d, want one attempt", fake.posts["registry_delete"])
	}
	if len(fake.registries) != 2 {
		t.Errorf("referenced generic endpoint was deleted: %+v", fake.registries)
	}
}

func TestCleanupStaleRegistryKeepsCompatibleGitHubEndpoint(t *testing.T) {
	fake := &fakeHarbor{
		registries: []HarborRegistry{{ID: 3, Name: "ghcr.io", Type: "github-ghcr", URL: "https://ghcr.io"}},
		projects:   []HarborProject{{ProjectID: 1, Name: "ghcrio", RegistryID: 3}},
	}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	if err := (&HarborClient{BaseURL: server.URL}).CleanupStaleRegistry(context.Background(), "ghcr.io"); err != nil {
		t.Fatalf("CleanupStaleRegistry() error = %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.posts["registry_delete"] != 0 {
		t.Errorf("registry DELETE = %d, want 0 for a compatible github-ghcr endpoint", fake.posts["registry_delete"])
	}
}

func TestEnsureRegistryDoesNotReuseGenericForDockerHub(t *testing.T) {
	fake := &fakeHarbor{registries: []HarborRegistry{
		{ID: 9, Name: "docker.io-generic", Type: "docker-registry", URL: "https://docker.io"},
	}}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	got, err := (&HarborClient{BaseURL: server.URL}).EnsureRegistry(context.Background(), "docker.io")
	if err != nil {
		t.Fatalf("EnsureRegistry() error = %v", err)
	}
	if got.Name != "docker.io" || got.Type != "docker-hub" {
		t.Errorf("endpoint = %+v, want a canonical docker-hub endpoint", got)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.posts["registry"] != 1 {
		t.Errorf("registry POST = %d, want 1", fake.posts["registry"])
	}
	if fake.lastRegistryBody["type"] != "docker-hub" {
		t.Errorf("create body = %v, want docker-hub", fake.lastRegistryBody)
	}
}

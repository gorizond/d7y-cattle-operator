package registryproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// HarborClient is the system-admin Harbor API subset used to create registry
// endpoints, projects, webhook policies, and robots.
type HarborClient struct {
	BaseURL  string
	Username string
	Password string
	Client   *http.Client
	// Credentials holds optional upstream registry credentials (access_key /
	// access_secret). They are mounted from a Secret and never logged.
	Credentials map[string]HarborCredential
}

// HarborCredential is a private upstream registry login for a Harbor registry
// endpoint.
type HarborCredential struct {
	AccessKey    string `yaml:"access_key"`
	AccessSecret string `yaml:"access_secret"`
}

type HarborRegistry struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
	URL  string `json:"url"`
}

type HarborProject struct {
	ProjectID  int    `json:"project_id"`
	Name       string `json:"name"`
	RegistryID int    `json:"registry_id"`
}

type HarborRobot struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Secret string `json:"secret"`
}

func (h *HarborClient) client() *http.Client {
	if h.Client != nil {
		return h.Client
	}
	return http.DefaultClient
}

func (h *HarborClient) do(ctx context.Context, method, path string, body any, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(h.BaseURL, "/")+path, reader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if h.Username != "" {
		req.SetBasicAuth(h.Username, h.Password)
	}
	resp, err := h.client().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if out != nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, out)
	}
	if resp.StatusCode >= 400 && resp.StatusCode != http.StatusConflict {
		return resp.StatusCode, fmt.Errorf("harbor %s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return resp.StatusCode, nil
}

func registrySpec(host string) HarborRegistry {
	switch host {
	case "docker.io":
		return HarborRegistry{Name: host, Type: "docker-hub", URL: "https://hub.docker.com"}
	case "ghcr.io":
		return HarborRegistry{Name: host, Type: "github-ghcr", URL: "https://ghcr.io"}
	case "quay.io":
		return HarborRegistry{Name: host, Type: "quay", URL: "https://quay.io"}
	default:
		return HarborRegistry{Name: host, Type: "docker-registry", URL: "https://" + host}
	}
}

// EnsureRegistry returns the Harbor registry endpoint ID, creating it once.
func (h *HarborClient) EnsureRegistry(ctx context.Context, host string) (HarborRegistry, error) {
	var list []HarborRegistry
	if _, err := h.do(ctx, http.MethodGet, "/api/v2.0/registries?name="+url.QueryEscape(host), nil, &list); err != nil {
		return HarborRegistry{}, err
	}
	for _, item := range list {
		if item.Name == host {
			return item, nil
		}
	}
	spec := registrySpec(host)
	body := map[string]any{
		"name": spec.Name, "type": spec.Type, "url": spec.URL, "insecure": false,
	}
	if credential, ok := h.Credentials[host]; ok && credential.AccessKey != "" {
		body["access_key"] = credential.AccessKey
		body["access_secret"] = credential.AccessSecret
	}
	status, err := h.do(ctx, http.MethodPost, "/api/v2.0/registries", body, nil)
	if err == nil && (status == http.StatusCreated || status == http.StatusOK) {
		return h.getRegistry(ctx, host)
	}
	if status != http.StatusConflict && err != nil {
		return HarborRegistry{}, err
	}
	return h.getRegistry(ctx, host)
}

func (h *HarborClient) getRegistry(ctx context.Context, host string) (HarborRegistry, error) {
	var list []HarborRegistry
	if _, err := h.do(ctx, http.MethodGet, "/api/v2.0/registries?name="+url.QueryEscape(host), nil, &list); err != nil {
		return HarborRegistry{}, err
	}
	for _, item := range list {
		if item.Name == host {
			return item, nil
		}
	}
	return HarborRegistry{}, fmt.Errorf("harbor registry %q not found after create", host)
}

// EnsureProject creates a Harbor project once. registryID 0 creates a local
// writable project (mirror); a non-zero ID creates a read-only proxy project.
func (h *HarborClient) EnsureProject(ctx context.Context, name string, registryID int) (HarborProject, error) {
	var list []HarborProject
	if _, err := h.do(ctx, http.MethodGet, "/api/v2.0/projects?name="+url.QueryEscape(name), nil, &list); err != nil {
		return HarborProject{}, err
	}
	for _, item := range list {
		if item.Name == name {
			return item, nil
		}
	}
	body := map[string]any{"project_name": name, "metadata": map[string]string{"public": "false"}}
	if registryID > 0 {
		body["registry_id"] = registryID
	}
	status, err := h.do(ctx, http.MethodPost, "/api/v2.0/projects", body, nil)
	if err != nil && status != http.StatusConflict {
		return HarborProject{}, err
	}
	var projects []HarborProject
	if _, err := h.do(ctx, http.MethodGet, "/api/v2.0/projects?name="+url.QueryEscape(name), nil, &projects); err != nil {
		return HarborProject{}, err
	}
	for _, item := range projects {
		if item.Name == name {
			return item, nil
		}
	}
	return HarborProject{}, fmt.Errorf("harbor project %q not found after create", name)
}

// EnsureWebhook creates the nydus-convert PUSH_ARTIFACT policy, updating an
// existing policy when its target address or auth header changed.
func (h *HarborClient) EnsureWebhook(ctx context.Context, project, address, secret string) error {
	path := "/api/v2.0/projects/" + url.PathEscape(project) + "/webhook/policies"
	authHeader := "Bearer " + secret
	body := map[string]any{
		"name":        "nydus-convert",
		"enabled":     true,
		"event_types": []string{"PUSH_ARTIFACT"},
		"targets": []map[string]any{{
			"type":             "http",
			"address":          address,
			"auth_header":      authHeader,
			"skip_cert_verify": false,
			"payload_format":   "Default",
		}},
	}
	var list []struct {
		ID      int    `json:"id"`
		Name    string `json:"name"`
		Targets []struct {
			Address    string `json:"address"`
			AuthHeader string `json:"auth_header"`
		} `json:"targets"`
	}
	if _, err := h.do(ctx, http.MethodGet, path, nil, &list); err != nil {
		return err
	}
	for _, item := range list {
		if item.Name != "nydus-convert" {
			continue
		}
		if len(item.Targets) == 1 && item.Targets[0].Address == address &&
			item.Targets[0].AuthHeader == authHeader {
			return nil
		}
		if _, err := h.do(ctx, http.MethodPut, path+"/"+strconv.Itoa(item.ID), body, nil); err != nil {
			return err
		}
		return nil
	}
	status, err := h.do(ctx, http.MethodPost, path, body, nil)
	if err != nil && status != http.StatusConflict {
		return err
	}
	return nil
}

// EnsureRobot creates a project robot once and returns its credentials.
func (h *HarborClient) EnsureRobot(ctx context.Context, name, project string) (HarborRobot, error) {
	var list []HarborRobot
	if _, err := h.do(ctx, http.MethodGet, "/api/v2.0/robots?q="+url.QueryEscape("name="+name), nil, &list); err != nil {
		return HarborRobot{}, err
	}
	for _, item := range list {
		if item.Name == name {
			return item, nil
		}
	}
	var robot HarborRobot
	status, err := h.do(ctx, http.MethodPost, "/api/v2.0/robots", map[string]any{
		"name":     name,
		"level":    "project",
		"duration": -1,
		"permissions": []map[string]any{{
			"kind":      "project",
			"namespace": project,
			"access": []map[string]string{
				{"resource": "repository", "action": "pull"},
				{"resource": "repository", "action": "push"},
				{"resource": "tag", "action": "create"},
			},
		}},
	}, &robot)
	if err != nil && status != http.StatusConflict {
		return HarborRobot{}, err
	}
	return robot, nil
}

// EnsureSystemRobot creates a system robot with pull access on source proxy
// projects and push/tag access on the mirror project.
func (h *HarborClient) EnsureSystemRobot(ctx context.Context, name string, sourceProjects []string, mirrorProject string) (HarborRobot, error) {
	var list []HarborRobot
	if _, err := h.do(ctx, http.MethodGet, "/api/v2.0/robots?q="+url.QueryEscape("name="+name), nil, &list); err != nil {
		return HarborRobot{}, err
	}
	for _, item := range list {
		if robotName(item.Name) != name {
			continue
		}
		if item.Secret != "" {
			return item, nil
		}
		// Harbor only returns a robot secret on creation. Delete the
		// secret-less leftover so the POST below issues fresh credentials.
		if _, err := h.do(ctx, http.MethodDelete, "/api/v2.0/robots/"+strconv.Itoa(item.ID), nil, nil); err != nil {
			return HarborRobot{}, err
		}
	}
	permissions := make([]map[string]any, 0, len(sourceProjects)+1)
	for _, project := range sourceProjects {
		permissions = append(permissions, map[string]any{
			"kind": "project", "namespace": project,
			"access": []map[string]string{{"resource": "repository", "action": "pull"}},
		})
	}
	permissions = append(permissions, map[string]any{
		"kind": "project", "namespace": mirrorProject,
		"access": []map[string]string{
			{"resource": "repository", "action": "pull"},
			{"resource": "repository", "action": "push"},
			{"resource": "tag", "action": "create"},
		},
	})
	var robot HarborRobot
	status, err := h.do(ctx, http.MethodPost, "/api/v2.0/robots", map[string]any{
		"name": name, "level": "system", "duration": -1, "permissions": permissions,
	}, &robot)
	if err != nil && status != http.StatusConflict {
		return HarborRobot{}, err
	}
	if robot.Secret != "" {
		return robot, nil
	}
	var created []HarborRobot
	if _, err := h.do(ctx, http.MethodGet, "/api/v2.0/robots?q="+url.QueryEscape("name="+name), nil, &created); err != nil {
		return HarborRobot{}, err
	}
	for _, item := range created {
		if robotName(item.Name) == name {
			return item, nil
		}
	}
	return HarborRobot{}, fmt.Errorf("harbor robot %q not found after create", name)
}

// robotName normalizes Harbor's login name for system robots, which is
// returned as "robot$<name>".
func robotName(name string) string { return strings.TrimPrefix(name, "robot$") }

// Provisioner creates the registry, read-only proxy project, writable mirror,
// and webhook policy on the first use of an upstream registry.
type Provisioner struct {
	Harbor         *HarborClient
	MirrorProject  string
	WebhookAddress string
	WebhookSecret  string

	mu    sync.Mutex
	cache map[string]bool
}

func (p *Provisioner) Ensure(ctx context.Context, registry string) error {
	if p == nil || p.Harbor == nil {
		return nil
	}
	p.mu.Lock()
	if p.cache[registry] {
		p.mu.Unlock()
		return nil
	}
	p.mu.Unlock()

	endpoint, err := p.Harbor.EnsureRegistry(ctx, registry)
	if err != nil {
		return err
	}
	if _, err := p.Harbor.EnsureProject(ctx, ProjectName(registry), endpoint.ID); err != nil {
		return err
	}
	if _, err := p.Harbor.EnsureProject(ctx, p.MirrorProject, 0); err != nil {
		return err
	}
	if err := p.Harbor.EnsureWebhook(ctx, ProjectName(registry), p.WebhookAddress, p.WebhookSecret); err != nil {
		return err
	}
	p.mu.Lock()
	if p.cache == nil {
		p.cache = map[string]bool{}
	}
	p.cache[registry] = true
	p.mu.Unlock()
	return nil
}

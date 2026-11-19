package registryproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	URL      string `json:"url"`
	Insecure bool   `json:"insecure"`
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
	return defaultHTTPClient
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
		// Harbor 2.x has no "quay" adapter; the generic docker-registry
		// adapter works with Quay's OCI API. A "quay" type returns HTTP 400.
		return HarborRegistry{Name: host, Type: "docker-registry", URL: "https://quay.io"}
	default:
		return HarborRegistry{Name: host, Type: "docker-registry", URL: "https://" + host}
	}
}

// EnsureRegistry returns a Harbor registry endpoint matching
// registrySpec(host). An existing endpoint with a stale type or URL (for
// example the retired "quay" adapter) is updated in place; if Harbor refuses
// the update the endpoint is deleted and recreated so a stale type is never
// reused by EnsureProject.
func (h *HarborClient) EnsureRegistry(ctx context.Context, host string) (HarborRegistry, error) {
	var list []HarborRegistry
	if _, err := h.do(ctx, http.MethodGet, "/api/v2.0/registries?name="+url.QueryEscape(host), nil, &list); err != nil {
		return HarborRegistry{}, err
	}
	spec := registrySpec(host)
	for _, item := range list {
		if item.Name != host {
			continue
		}
		if item.Type == spec.Type && item.URL == spec.URL {
			return item, nil
		}
		reconciled, err := h.updateRegistry(ctx, item, spec)
		if err == nil {
			return reconciled, nil
		}
		if _, deleteErr := h.do(ctx, http.MethodDelete, "/api/v2.0/registries/"+strconv.Itoa(item.ID), nil, nil); deleteErr != nil {
			return HarborRegistry{}, fmt.Errorf("reconcile harbor registry %q: update: %w; delete: %v", host, err, deleteErr)
		}
		break
	}
	return h.createRegistry(ctx, host, spec)
}

// registryBody builds the Harbor create/update payload. Without configured
// credentials access_key/access_secret stay empty; Harbor keeps the stored
// credential in that case, so an update never drops upstream auth.
func (h *HarborClient) registryBody(spec HarborRegistry, insecure bool) map[string]any {
	body := map[string]any{
		"name": spec.Name, "type": spec.Type, "url": spec.URL, "insecure": insecure,
	}
	if credential, ok := h.Credentials[spec.Name]; ok && credential.AccessKey != "" {
		body["access_key"] = credential.AccessKey
		body["access_secret"] = credential.AccessSecret
	}
	return body
}

func (h *HarborClient) updateRegistry(ctx context.Context, existing, spec HarborRegistry) (HarborRegistry, error) {
	path := "/api/v2.0/registries/" + strconv.Itoa(existing.ID)
	status, err := h.do(ctx, http.MethodPut, path, h.registryBody(spec, existing.Insecure), nil)
	if err != nil {
		return HarborRegistry{}, err
	}
	if status != http.StatusOK && status != http.StatusNoContent {
		return HarborRegistry{}, fmt.Errorf("harbor PUT %s: status %d", path, status)
	}
	return h.getRegistry(ctx, existing.Name)
}

func (h *HarborClient) createRegistry(ctx context.Context, host string, spec HarborRegistry) (HarborRegistry, error) {
	status, err := h.do(ctx, http.MethodPost, "/api/v2.0/registries", h.registryBody(spec, false), nil)
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

	mu       sync.Mutex
	cache    map[string]bool
	projects map[string]string
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
		return fmt.Errorf("ensure Harbor registry %q: %w", registry, err)
	}
	project := ProjectName(registry)
	if _, err := p.Harbor.EnsureProject(ctx, project, endpoint.ID); err != nil {
		return fmt.Errorf("ensure Harbor source project %q: %w", project, err)
	}
	if _, err := p.Harbor.EnsureProject(ctx, p.MirrorProject, 0); err != nil {
		return fmt.Errorf("ensure Harbor mirror project %q: %w", p.MirrorProject, err)
	}
	// The webhook is an optional acceleration path: pull-triggered conversion
	// still enqueues jobs, so a broken Harbor webhook policy must not fail the
	// read path or bootstrap.
	if p.WebhookAddress != "" {
		if err := p.Harbor.EnsureWebhook(ctx, project, p.WebhookAddress, p.WebhookSecret); err != nil {
			slog.Warn("harbor webhook policy is unavailable; pull-triggered conversion still works",
				"registry", registry, "project", project, "error", err)
		}
	}
	p.mu.Lock()
	if p.cache == nil {
		p.cache = map[string]bool{}
	}
	if p.projects == nil {
		p.projects = map[string]string{}
	}
	p.cache[registry] = true
	p.projects[project] = registry
	p.mu.Unlock()
	return nil
}

// RegistryForProject maps a Harbor source project back to its upstream
// registry. The mapping is populated by Ensure, so the Harbor webhook can
// resolve projects when the allowlist is "*".
func (p *Provisioner) RegistryForProject(project string) string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.projects[project]
}

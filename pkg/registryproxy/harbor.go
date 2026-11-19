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

// registrySpec returns the Harbor endpoint for an upstream host. Harbor 2.15.4
// types github-ghcr, gitlab, and quay embed the same native docker adapter, so
// the generic docker-registry type is used for every host except Docker Hub.
// Docker Hub needs the docker-hub adapter (registry-1.docker.io plus library/
// path handling); a generic endpoint pointed at https://hub.docker.com fails
// its health check with 404.
func registrySpec(host string) HarborRegistry {
	if host == "docker.io" {
		return HarborRegistry{Name: host, Type: "docker-hub", URL: "https://hub.docker.com"}
	}
	// The generic adapter covers ghcr.io, quay.io, GitLab, GCR, public ECR,
	// MCR, registry.k8s.io, NVCR, Elastic, Red Hat, and SUSE OCI registries.
	// Private NVCR/Red Hat endpoints additionally need credentials.
	return HarborRegistry{Name: host, Type: "docker-registry", URL: "https://" + host}
}

// genericRegistryName derives a Harbor-safe deterministic endpoint name for a
// host whose existing endpoint has a different type.
func genericRegistryName(host string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			return r
		default:
			return '-'
		}
	}, host)
	return safe + "-generic"
}

// EnsureRegistry returns a Harbor endpoint usable for host. Harbor 2.15.4
// ignores `type` on PUT /registries/{id} and refuses to DELETE an endpoint a
// proxy-cache project still references, so a type mismatch is never patched or
// recreated in place: a separate deterministic "<host>-generic" endpoint is
// created and returned while the stale endpoint is left untouched. A same-type
// URL mismatch is updated in place; that reconcile is best-effort, so a failed
// PUT logs and keeps the existing endpoint instead of blocking pulls.
func (h *HarborClient) EnsureRegistry(ctx context.Context, host string) (HarborRegistry, error) {
	spec := registrySpec(host)
	existing, err := h.findRegistry(ctx, host)
	if err != nil {
		return HarborRegistry{}, err
	}
	if existing == nil {
		return h.createRegistry(ctx, spec, host)
	}
	if existing.Type == spec.Type {
		return h.reconcileRegistryURL(ctx, *existing, spec, host)
	}
	generic := spec
	generic.Name = genericRegistryName(host)
	alternate, err := h.findRegistry(ctx, generic.Name)
	if err != nil {
		return HarborRegistry{}, err
	}
	if alternate != nil {
		if alternate.Type != spec.Type {
			return HarborRegistry{}, fmt.Errorf(
				"harbor endpoint %q has type %q, want %q; remove or rename it manually",
				alternate.Name, alternate.Type, spec.Type)
		}
		return h.reconcileRegistryURL(ctx, *alternate, generic, host)
	}
	created, err := h.createRegistry(ctx, generic, host)
	if err != nil {
		return HarborRegistry{}, fmt.Errorf(
			"create generic harbor endpoint %q for stale type %q: %w",
			generic.Name, existing.Type, err)
	}
	return created, nil
}

// reconcileRegistryURL updates a same-type endpoint whose URL drifted. Failure
// is non-fatal: Harbor keeps serving the existing endpoint.
func (h *HarborClient) reconcileRegistryURL(ctx context.Context, existing, spec HarborRegistry, credentialsHost string) (HarborRegistry, error) {
	if existing.URL == spec.URL {
		return existing, nil
	}
	updated, err := h.updateRegistryURL(ctx, existing, spec, credentialsHost)
	if err != nil {
		slog.Warn("harbor registry URL reconcile failed; using the existing endpoint",
			"registry", existing.Name, "existing_url", existing.URL, "wanted_url", spec.URL, "error", err)
		return existing, nil
	}
	return updated, nil
}

// registryBody builds the Harbor create/update payload. Without configured
// credentials access_key/access_secret stay empty; Harbor keeps the stored
// credential in that case, so an update never drops upstream auth.
func (h *HarborClient) registryBody(spec HarborRegistry, credentialsHost string, insecure bool) map[string]any {
	body := map[string]any{
		"name": spec.Name, "type": spec.Type, "url": spec.URL, "insecure": insecure,
	}
	if credential, ok := h.Credentials[credentialsHost]; ok && credential.AccessKey != "" {
		body["access_key"] = credential.AccessKey
		body["access_secret"] = credential.AccessSecret
	}
	return body
}

func (h *HarborClient) updateRegistryURL(ctx context.Context, existing, spec HarborRegistry, credentialsHost string) (HarborRegistry, error) {
	path := "/api/v2.0/registries/" + strconv.Itoa(existing.ID)
	body := h.registryBody(HarborRegistry{
		Name: existing.Name, Type: existing.Type, URL: spec.URL,
	}, credentialsHost, existing.Insecure)
	status, err := h.do(ctx, http.MethodPut, path, body, nil)
	if err != nil {
		return HarborRegistry{}, err
	}
	if status != http.StatusOK && status != http.StatusNoContent {
		return HarborRegistry{}, fmt.Errorf("harbor PUT %s: status %d", path, status)
	}
	return h.getRegistry(ctx, existing.Name)
}

func (h *HarborClient) createRegistry(ctx context.Context, spec HarborRegistry, credentialsHost string) (HarborRegistry, error) {
	status, err := h.do(ctx, http.MethodPost, "/api/v2.0/registries", h.registryBody(spec, credentialsHost, false), nil)
	if err == nil && (status == http.StatusCreated || status == http.StatusOK) {
		return h.getRegistry(ctx, spec.Name)
	}
	if status != http.StatusConflict && err != nil {
		return HarborRegistry{}, err
	}
	return h.getRegistry(ctx, spec.Name)
}

func (h *HarborClient) findRegistry(ctx context.Context, name string) (*HarborRegistry, error) {
	var list []HarborRegistry
	if _, err := h.do(ctx, http.MethodGet, "/api/v2.0/registries?name="+url.QueryEscape(name), nil, &list); err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Name == name {
			return &list[i], nil
		}
	}
	return nil, nil
}

func (h *HarborClient) getRegistry(ctx context.Context, name string) (HarborRegistry, error) {
	registry, err := h.findRegistry(ctx, name)
	if err != nil {
		return HarborRegistry{}, err
	}
	if registry == nil {
		return HarborRegistry{}, fmt.Errorf("harbor registry %q not found after create", name)
	}
	return *registry, nil
}

// EnsureProject returns the project for registryID, creating it once. A
// registry_id mismatch on an existing proxy project is re-pointed with a
// project update; if Harbor refuses, a deterministic "<project>-generic"
// project is created and returned instead. Callers must use the returned name.
func (h *HarborClient) EnsureProject(ctx context.Context, name string, registryID int) (HarborProject, error) {
	project, err := h.findProject(ctx, name)
	if err != nil {
		return HarborProject{}, err
	}
	if project == nil {
		return h.createProject(ctx, name, registryID)
	}
	if registryID == 0 || project.RegistryID == registryID {
		return *project, nil
	}
	fallback := name + "-generic"
	// A previous Ensure may already have selected the fallback because Harbor
	// refuses to re-point this project; prefer it over repeating the failed
	// update on every call.
	if alternate, err := h.findProject(ctx, fallback); err != nil {
		return HarborProject{}, err
	} else if alternate != nil && alternate.RegistryID == registryID {
		return *alternate, nil
	}
	updated, err := h.updateProjectRegistry(ctx, name, registryID)
	if err == nil {
		return updated, nil
	}
	slog.Warn("harbor project registry_id cannot be updated; using a generic project",
		"project", name, "registry_id", registryID, "error", err)
	alternate, findErr := h.findProject(ctx, fallback)
	if findErr != nil {
		return HarborProject{}, findErr
	}
	if alternate != nil {
		if alternate.RegistryID == registryID {
			return *alternate, nil
		}
		return HarborProject{}, fmt.Errorf(
			"harbor project %q has registry_id %d, want %d; remove or rename it manually",
			fallback, alternate.RegistryID, registryID)
	}
	return h.createProject(ctx, fallback, registryID)
}

func (h *HarborClient) updateProjectRegistry(ctx context.Context, name string, registryID int) (HarborProject, error) {
	path := "/api/v2.0/projects/" + url.PathEscape(name)
	status, err := h.do(ctx, http.MethodPut, path, map[string]any{"registry_id": registryID}, nil)
	if err != nil {
		return HarborProject{}, err
	}
	if status != http.StatusOK && status != http.StatusNoContent {
		return HarborProject{}, fmt.Errorf("harbor PUT %s: status %d", path, status)
	}
	return h.getProject(ctx, name)
}

func (h *HarborClient) createProject(ctx context.Context, name string, registryID int) (HarborProject, error) {
	body := map[string]any{"project_name": name, "metadata": map[string]string{"public": "false"}}
	if registryID > 0 {
		body["registry_id"] = registryID
	}
	status, err := h.do(ctx, http.MethodPost, "/api/v2.0/projects", body, nil)
	if err != nil && status != http.StatusConflict {
		return HarborProject{}, err
	}
	project, err := h.findProject(ctx, name)
	if err != nil {
		return HarborProject{}, err
	}
	if project == nil {
		return HarborProject{}, fmt.Errorf("harbor project %q not found after create", name)
	}
	return *project, nil
}

func (h *HarborClient) getProject(ctx context.Context, name string) (HarborProject, error) {
	project, err := h.findProject(ctx, name)
	if err != nil {
		return HarborProject{}, err
	}
	if project == nil {
		return HarborProject{}, fmt.Errorf("harbor project %q not found", name)
	}
	return *project, nil
}

func (h *HarborClient) findProject(ctx context.Context, name string) (*HarborProject, error) {
	var list []HarborProject
	if _, err := h.do(ctx, http.MethodGet, "/api/v2.0/projects?name="+url.QueryEscape(name), nil, &list); err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Name == name {
			return &list[i], nil
		}
	}
	return nil, nil
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

	mu         sync.Mutex
	cache      map[string]bool
	projects   map[string]string
	projectFor map[string]string
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
	project, err := p.Harbor.EnsureProject(ctx, ProjectName(registry), endpoint.ID)
	if err != nil {
		return fmt.Errorf("ensure Harbor source project %q: %w", ProjectName(registry), err)
	}
	if _, err := p.Harbor.EnsureProject(ctx, p.MirrorProject, 0); err != nil {
		return fmt.Errorf("ensure Harbor mirror project %q: %w", p.MirrorProject, err)
	}
	// The webhook is an optional acceleration path: pull-triggered conversion
	// still enqueues jobs, so a broken Harbor webhook policy must not fail the
	// read path or bootstrap.
	if p.WebhookAddress != "" {
		if err := p.Harbor.EnsureWebhook(ctx, project.Name, p.WebhookAddress, p.WebhookSecret); err != nil {
			slog.Warn("harbor webhook policy is unavailable; pull-triggered conversion still works",
				"registry", registry, "project", project.Name, "error", err)
		}
	}
	p.mu.Lock()
	if p.cache == nil {
		p.cache = map[string]bool{}
	}
	if p.projects == nil {
		p.projects = map[string]string{}
	}
	if p.projectFor == nil {
		p.projectFor = map[string]string{}
	}
	p.cache[registry] = true
	p.projects[project.Name] = registry
	p.projectFor[registry] = project.Name
	p.mu.Unlock()
	return nil
}

// ProjectFor returns the Harbor source project used for registry. The
// "<project>-generic" fallback is selected only when Harbor cannot re-point an
// existing proxy-cache project, so read paths must use this instead of
// ProjectName directly.
func (p *Provisioner) ProjectFor(registry string) string {
	if p == nil {
		return ProjectName(registry)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if name := p.projectFor[registry]; name != "" {
		return name
	}
	return ProjectName(registry)
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

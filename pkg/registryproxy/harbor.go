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

// HarborPreheatInstance is a Harbor P2P preheat provider instance, exposed in
// Administration -> Distributions. Harbor has no Dragonfly registry adapter;
// Dragonfly is a preheat vendor only.
type HarborPreheatInstance struct {
	ID          int               `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Vendor      string            `json:"vendor"`
	Endpoint    string            `json:"endpoint"`
	AuthMode    string            `json:"auth_mode"`
	AuthInfo    map[string]string `json:"auth_info,omitempty"`
	Enabled     bool              `json:"enabled"`
	Default     bool              `json:"default"`
	Insecure    bool              `json:"insecure"`
}

const (
	harborPreheatInstancesPath = "/api/v2.0/p2p/preheat/instances"
	harborPreheatPingPath      = "/api/v2.0/p2p/preheat/instances/ping"
	harborPreheatVendor        = "dragonfly"
	harborPreheatAuthMode      = "OAUTH"
)

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
// the generic docker-registry type is used for new endpoints on every host
// except Docker Hub. Docker Hub needs the docker-hub adapter
// (registry-1.docker.io plus library/ path handling); a generic endpoint
// pointed at https://hub.docker.com fails its health check with 404. An
// existing github-ghcr endpoint is accepted for ghcr.io instead of creating an
// unused generic endpoint.
func registrySpec(host string) HarborRegistry {
	if host == "docker.io" {
		return HarborRegistry{Name: host, Type: "docker-hub", URL: "https://hub.docker.com"}
	}
	// The generic adapter covers ghcr.io, quay.io, GitLab, GCR, public ECR,
	// MCR, registry.k8s.io, NVCR, Elastic, Red Hat, and SUSE OCI registries.
	// Private NVCR/Red Hat endpoints additionally need credentials.
	return HarborRegistry{Name: host, Type: "docker-registry", URL: "https://" + host}
}

// compatibleRegistryType reports whether an existing endpoint type can serve
// host. Harbor's github-ghcr adapter is equivalent to docker-registry for
// ghcr.io and already carries the working proxy-cache project, so it is kept
// instead of being replaced by a "<host>-generic" endpoint. Quay's typed
// adapter is not accepted: the live stale quay endpoint used the broken quay
// path, so quay.io keeps the generic fallback.
func compatibleRegistryType(host, typ string) bool {
	if typ == registrySpec(host).Type {
		return true
	}
	return host == "ghcr.io" && typ == "github-ghcr"
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
		// A previous generic fallback is still the working endpoint for this
		// host; reuse it instead of recreating the canonical name and moving
		// the project again.
		alternate, err := h.findRegistry(ctx, genericRegistryName(host))
		if err != nil {
			return HarborRegistry{}, err
		}
		// Only reuse a generic endpoint that was created with this host's
		// adapter type. In particular, docker.io must not reuse a
		// docker-registry generic endpoint (the docker-hub adapter is
		// required for Docker Hub).
		if alternate != nil && alternate.Type == spec.Type {
			return h.reconcileRegistryURL(ctx, *alternate, spec, host)
		}
		return h.createRegistry(ctx, spec, host)
	}
	if compatibleRegistryType(host, existing.Type) {
		return h.reconcileRegistryURL(ctx, *existing, spec, host)
	}
	generic := spec
	generic.Name = genericRegistryName(host)
	alternate, err := h.findRegistry(ctx, generic.Name)
	if err != nil {
		return HarborRegistry{}, err
	}
	if alternate != nil {
		if !compatibleRegistryType(host, alternate.Type) {
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

// CleanupStaleRegistry removes the exact-name endpoint for host when its type
// no longer matches registrySpec. It is an explicit, bounded cleanup: one
// endpoint lookup and at most one DELETE. It never touches a "-generic"
// endpoint and never scans the endpoint list. Harbor answers 412 when a
// proxy-cache project still references the endpoint; that state is normal
// before EnsureProject re-points the project, so it is logged and ignored.
func (h *HarborClient) CleanupStaleRegistry(ctx context.Context, host string) error {
	stale, err := h.findRegistry(ctx, host)
	if err != nil {
		return err
	}
	if stale == nil || stale.Name == genericRegistryName(host) || compatibleRegistryType(host, stale.Type) {
		return nil
	}
	path := "/api/v2.0/registries/" + strconv.Itoa(stale.ID)
	status, err := h.do(ctx, http.MethodDelete, path, nil, nil)
	if err != nil {
		if status == http.StatusPreconditionFailed {
			slog.Info("harbor stale registry endpoint is still referenced; leaving it",
				"registry", stale.Name, "type", stale.Type, "id", stale.ID)
			return nil
		}
		slog.Warn("harbor stale registry endpoint cleanup failed; leaving it",
			"registry", stale.Name, "type", stale.Type, "id", stale.ID, "error", err)
		return nil
	}
	slog.Info("harbor stale registry endpoint removed",
		"registry", stale.Name, "type", stale.Type, "id", stale.ID)
	return nil
}

// CleanupUnusedGenericRegistry removes a leftover "<host>-generic" endpoint
// when the canonical endpoint is compatible and used for the host. It is
// explicit and bounded (two lookups and at most one DELETE) and never touches
// the canonical endpoint. A generic endpoint that still has a project answers
// 412; it is logged and kept. Hosts that actually use their generic endpoint
// (canonical missing or stale, for example quay.io) are left untouched.
func (h *HarborClient) CleanupUnusedGenericRegistry(ctx context.Context, host string) error {
	canonical, err := h.findRegistry(ctx, host)
	if err != nil {
		return err
	}
	if canonical == nil || !compatibleRegistryType(host, canonical.Type) {
		return nil
	}
	generic, err := h.findRegistry(ctx, genericRegistryName(host))
	if err != nil {
		return err
	}
	if generic == nil || generic.Type != registrySpec(host).Type {
		return nil
	}
	status, err := h.do(ctx, http.MethodDelete, "/api/v2.0/registries/"+strconv.Itoa(generic.ID), nil, nil)
	if err != nil {
		if status == http.StatusPreconditionFailed {
			slog.Info("harbor unused generic endpoint is still referenced; leaving it",
				"registry", generic.Name, "type", generic.Type, "id", generic.ID)
			return nil
		}
		slog.Warn("harbor unused generic endpoint cleanup failed; leaving it",
			"registry", generic.Name, "type", generic.Type, "id", generic.ID, "error", err)
		return nil
	}
	slog.Info("harbor unused generic endpoint removed",
		"registry", generic.Name, "type", generic.Type, "id", generic.ID)
	return nil
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

// EnsurePreheatInstance creates or updates the Dragonfly P2P preheat provider
// instance ("Distribution") and then pings it. Harbor answers 409 when the name
// already exists; that path re-lists and updates. A ping failure is returned
// after the instance was written, so the caller retries without losing it. The
// token is only sent to Harbor and never appears in errors.
func (h *HarborClient) EnsurePreheatInstance(ctx context.Context, name, endpoint, token string, insecure bool) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("harbor preheat instance: name is required")
	}
	if err := validatePreheatEndpoint(endpoint); err != nil {
		return fmt.Errorf("harbor preheat instance %q: %w", name, err)
	}
	if token == "" {
		return fmt.Errorf("harbor preheat instance %q: token is required", name)
	}

	body := HarborPreheatInstance{
		Name:        name,
		Description: "Dragonfly P2P preheat",
		Vendor:      harborPreheatVendor,
		Endpoint:    endpoint,
		AuthMode:    harborPreheatAuthMode,
		AuthInfo:    map[string]string{"token": token},
		Enabled:     true,
		Default:     false,
		Insecure:    insecure,
	}

	existing, err := h.findPreheatInstance(ctx, name)
	if err != nil {
		return err
	}
	if existing != nil && existing.Vendor != harborPreheatVendor {
		return fmt.Errorf("harbor preheat instance %q exists with vendor %q; remove or rename it manually", name, existing.Vendor)
	}

	payload := preheatInstanceBody(body)
	if existing != nil {
		payload["id"] = existing.ID
		if err := h.putPreheatInstance(ctx, name, existing.ID, payload); err != nil {
			return err
		}
	} else {
		status, err := h.do(ctx, http.MethodPost, harborPreheatInstancesPath, payload, nil)
		switch {
		case err == nil && status < http.StatusMultipleChoices:
			// created
		case status == http.StatusConflict:
			existing, findErr := h.findPreheatInstance(ctx, name)
			if findErr != nil {
				return findErr
			}
			if existing == nil {
				return fmt.Errorf("harbor preheat instance %q: conflict after create", name)
			}
			payload["id"] = existing.ID
			if err := h.putPreheatInstance(ctx, name, existing.ID, payload); err != nil {
				return err
			}
		case err != nil:
			return fmt.Errorf("harbor create preheat instance %q: %w", name, err)
		default:
			return fmt.Errorf("harbor create preheat instance %q: status %d", name, status)
		}
	}

	return h.pingPreheatInstance(ctx, name, body)
}

func (h *HarborClient) findPreheatInstance(ctx context.Context, name string) (*HarborPreheatInstance, error) {
	var list []HarborPreheatInstance
	path := harborPreheatInstancesPath + "?q=" + url.QueryEscape("name="+name)
	if _, err := h.do(ctx, http.MethodGet, path, nil, &list); err != nil {
		return nil, fmt.Errorf("harbor list preheat instances for %q: %w", name, err)
	}
	for i := range list {
		if list[i].Name == name {
			return &list[i], nil
		}
	}
	return nil, nil
}

func (h *HarborClient) putPreheatInstance(ctx context.Context, name string, id int, payload map[string]any) error {
	// Harbor addresses preheat instances by name; the numeric id belongs in the
	// body because the controller updates the instance named there.
	status, err := h.do(ctx, http.MethodPut, harborPreheatInstancesPath+"/"+url.PathEscape(name), payload, nil)
	if err != nil {
		return fmt.Errorf("harbor update preheat instance %q: %w", name, err)
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return fmt.Errorf("harbor update preheat instance %q: status %d", name, status)
	}
	return nil
}

func (h *HarborClient) pingPreheatInstance(ctx context.Context, name string, instance HarborPreheatInstance) error {
	status, err := h.do(ctx, http.MethodPost, harborPreheatPingPath, preheatInstanceBody(instance), nil)
	if err != nil {
		return fmt.Errorf("harbor ping preheat instance %q: %w", name, err)
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return fmt.Errorf("harbor ping preheat instance %q: status %d", name, status)
	}
	return nil
}

// preheatInstanceBody renders the create/update payload. The update body must
// carry the existing id; Harbor refuses vendor changes.
func preheatInstanceBody(instance HarborPreheatInstance) map[string]any {
	return map[string]any{
		"name":        instance.Name,
		"description": instance.Description,
		"vendor":      instance.Vendor,
		"endpoint":    instance.Endpoint,
		"auth_mode":   instance.AuthMode,
		"auth_info":   instance.AuthInfo,
		"enabled":     instance.Enabled,
		"default":     instance.Default,
		"insecure":    instance.Insecure,
	}
}

func validatePreheatEndpoint(endpoint string) error {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("endpoint must be an absolute http(s) URL")
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
	// Now that the project has been re-pointed (or a generic project was
	// selected), the stale-type endpoint can be unreferenced. Best-effort:
	// a referenced endpoint answers 412 and stays.
	if err := p.Harbor.CleanupStaleRegistry(ctx, registry); err != nil {
		slog.Warn("harbor stale registry endpoint cleanup skipped", "registry", registry, "error", err)
	}
	// A previous rollout may have created "<host>-generic" before the
	// canonical endpoint was recognized as compatible (ghcr.io); remove it
	// once the canonical endpoint serves the project again.
	if err := p.Harbor.CleanupUnusedGenericRegistry(ctx, registry); err != nil {
		slog.Warn("harbor unused generic endpoint cleanup skipped", "registry", registry, "error", err)
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

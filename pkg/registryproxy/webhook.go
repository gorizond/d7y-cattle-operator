package registryproxy

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// HarborEvent is the Default Harbor webhook payload subset.
type HarborEvent struct {
	Type      string `json:"type"`
	EventData struct {
		Repository struct {
			RepoFullName string `json:"repo_full_name"`
		} `json:"repository"`
		Resources []struct {
			Digest      string `json:"digest"`
			Tag         string `json:"tag"`
			ResourceURL string `json:"resource_url"`
		} `json:"resources"`
	} `json:"event_data"`
}

// Webhook accepts Harbor PUSH_ARTIFACT events, authenticates the shared
// secret, and enqueues a conversion/copy job without blocking Harbor.
type Webhook struct {
	Secret      string
	Manager     *Manager
	Config      *Config
	Provisioner *Provisioner
}

func (h *Webhook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !validBearer(r.Header.Get("Authorization"), h.Secret) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	var event HarborEvent
	if err := json.Unmarshal(body, &event); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	if event.Type != "" && event.Type != "PUSH_ARTIFACT" {
		http.Error(w, "unsupported event", http.StatusBadRequest)
		return
	}
	project, repo, ok := splitRepo(event.EventData.Repository.RepoFullName)
	if !ok {
		project, repo, ok = projectFromResourceURL(event.EventData.Resources)
	}
	if !ok {
		http.Error(w, "repository is missing", http.StatusBadRequest)
		return
	}
	registry := h.registryForProject(project)
	if registry == "" {
		http.Error(w, "registry project is not allowlisted", http.StatusForbidden)
		return
	}
	accepted := false
	for _, resource := range event.EventData.Resources {
		ref := strings.TrimSpace(resource.Digest)
		if ref == "" {
			ref = strings.TrimSpace(resource.Tag)
		}
		if ref == "" {
			continue
		}
		if h.Manager != nil {
			h.Manager.Enqueue(Job{
				Registry:   registry,
				Repository: repo,
				Reference:  ref,
				Tag:        strings.TrimSpace(resource.Tag),
				Digest:     strings.TrimSpace(resource.Digest),
				Trigger:    "webhook",
			})
		}
		accepted = true
	}
	if !accepted {
		http.Error(w, "event has no resources", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte("accepted\n"))
}

func validBearer(header, secret string) bool {
	if secret == "" {
		return false
	}
	expected := "Bearer " + secret
	return subtle.ConstantTimeCompare([]byte(header), []byte(expected)) == 1
}

func splitRepo(full string) (project, repo string, ok bool) {
	parts := strings.SplitN(strings.Trim(full, "/"), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func projectFromResourceURL(resources []struct {
	Digest      string `json:"digest"`
	Tag         string `json:"tag"`
	ResourceURL string `json:"resource_url"`
}) (string, string, bool) {
	for _, resource := range resources {
		raw := strings.TrimSpace(resource.ResourceURL)
		if raw == "" {
			continue
		}
		if parsed := strings.SplitN(raw, "://", 2); len(parsed) == 2 {
			raw = parsed[1]
		}
		if idx := strings.Index(raw, "/"); idx >= 0 {
			raw = raw[idx+1:]
		}
		raw = strings.SplitN(raw, "@", 2)[0]
		parts := strings.SplitN(raw, ":", 2)
		project, repo, ok := splitRepo(parts[0])
		if ok {
			return project, repo, true
		}
	}
	return "", "", false
}

func (h *Webhook) registryForProject(project string) string {
	if h.Config == nil {
		return ""
	}
	for _, registry := range h.Config.Registry.Allowlist {
		if registry == "*" {
			continue
		}
		if ProjectName(registry) == project {
			return registry
		}
	}
	return h.Provisioner.RegistryForProject(project)
}

func (h *Webhook) String() string {
	return fmt.Sprintf("registryproxy.Webhook{manager=%v}", h.Manager)
}

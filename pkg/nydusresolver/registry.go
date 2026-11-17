package nydusresolver

import (
	"fmt"
	"net"
	"strings"
)

// DefaultRegistryMap maps registry hosts that nydus-snapshotter may contact to
// the first path segment understood by the upstream registry proxy. Hosts that
// are not in the map are rejected.
func DefaultRegistryMap() map[string]string {
	return map[string]string{
		"registry-1.docker.io": "docker.io",
		"index.docker.io":      "docker.io",
		"docker.io":            "docker.io",
		"ghcr.io":              "ghcr.io",
		"quay.io":              "quay.io",
		"registry.k8s.io":      "registry.k8s.io",
		"gcr.io":               "gcr.io",
		"mcr.microsoft.com":    "mcr.microsoft.com",
	}
}

// ParseRegistryMap parses comma-separated host=path entries. It is meant for
// overrides on top of DefaultRegistryMap.
func ParseRegistryMap(spec string) (map[string]string, error) {
	result := make(map[string]string)
	if strings.TrimSpace(spec) == "" {
		return result, nil
	}
	for _, entry := range strings.Split(spec, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			return nil, fmt.Errorf("empty registry map entry")
		}
		host, mapped, ok := strings.Cut(entry, "=")
		if !ok {
			return nil, fmt.Errorf("registry map entry %q must be host=path", entry)
		}
		host = NormalizeRegistryHost(host)
		if err := validateRegistryHost(host); err != nil {
			return nil, fmt.Errorf("registry map entry %q: %w", entry, err)
		}
		mapped = strings.Trim(strings.TrimSpace(mapped), "/")
		if err := validateMappedPath(mapped); err != nil {
			return nil, fmt.Errorf("registry map entry %q: %w", entry, err)
		}
		result[host] = mapped
	}
	return result, nil
}

// MergeRegistryMaps returns base with overrides applied.
func MergeRegistryMaps(base, overrides map[string]string) map[string]string {
	result := make(map[string]string, len(base)+len(overrides))
	for host, mapped := range base {
		result[host] = mapped
	}
	for host, mapped := range overrides {
		result[host] = mapped
	}
	return result
}

// NormalizeRegistryHost lowercases a registry host and removes a port and a
// trailing dot.
func NormalizeRegistryHost(raw string) string {
	host := strings.ToLower(strings.TrimSpace(raw))
	if host == "" {
		return ""
	}
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	} else if strings.HasSuffix(host, ":") {
		host = strings.TrimSuffix(host, ":")
	}
	return strings.TrimSuffix(host, ".")
}

// RewriteRegistryPath turns a registry API path into the upstream proxy path
// /<mapped>/v2/... while preserving the rest of the path exactly.
func RewriteRegistryPath(requestPath, mapped string) (string, bool) {
	if !strings.HasPrefix(requestPath, "/v2") {
		return "", false
	}
	rest := strings.TrimPrefix(requestPath, "/v2")
	if rest != "" && !strings.HasPrefix(rest, "/") {
		return "", false
	}
	mapped = strings.Trim(strings.TrimSpace(mapped), "/")
	if mapped == "" {
		return "", false
	}
	return "/" + mapped + "/v2" + rest, true
}

func validateRegistryHost(host string) error {
	if host == "" {
		return fmt.Errorf("host is empty")
	}
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '.', r == '-', r == '_':
		default:
			return fmt.Errorf("invalid host %q", host)
		}
	}
	if strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") || strings.Contains(host, "..") {
		return fmt.Errorf("invalid host %q", host)
	}
	return nil
}

func validateMappedPath(mapped string) error {
	if mapped == "" {
		return fmt.Errorf("mapped path is empty")
	}
	for _, segment := range strings.Split(mapped, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("invalid mapped path %q", mapped)
		}
	}
	for _, r := range mapped {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '.', r == '-', r == '_', r == '/':
		default:
			return fmt.Errorf("invalid mapped path %q", mapped)
		}
	}
	return nil
}

func normalizeRegistryMap(input map[string]string) (map[string]string, error) {
	result := make(map[string]string, len(input))
	for rawHost, mapped := range input {
		host := NormalizeRegistryHost(rawHost)
		if err := validateRegistryHost(host); err != nil {
			return nil, fmt.Errorf("registry map: %w", err)
		}
		mapped = strings.Trim(strings.TrimSpace(mapped), "/")
		if err := validateMappedPath(mapped); err != nil {
			return nil, fmt.Errorf("registry map host %s: %w", host, err)
		}
		result[host] = mapped
	}
	return result, nil
}

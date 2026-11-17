package nydusresolver

import (
	"fmt"
	"net"
	"strings"
)

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

// RegistryPath builds the upstream registry-proxy path from the CONNECT host
// and the original OCI path: /<connect-host>/v2/... . The registry proxy owns
// registry alias normalization and allowlisting.
func RegistryPath(requestPath, connectHost string) (string, bool) {
	if !strings.HasPrefix(requestPath, "/v2") {
		return "", false
	}
	rest := strings.TrimPrefix(requestPath, "/v2")
	if rest != "" && !strings.HasPrefix(rest, "/") {
		return "", false
	}
	host := NormalizeRegistryHost(connectHost)
	if validateRegistryHost(host) != nil {
		return "", false
	}
	return "/" + host + "/v2" + rest, true
}

func validateRegistryHost(host string) error {
	if host == "" {
		return fmt.Errorf("host is empty")
	}
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '.', r == '-':
		default:
			return fmt.Errorf("invalid host %q", host)
		}
	}
	if strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") || strings.Contains(host, "..") {
		return fmt.Errorf("invalid host %q", host)
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return fmt.Errorf("invalid host %q", host)
		}
	}
	return nil
}

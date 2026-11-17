// Package bootstrap builds the node bootstrap payload and userData injection.
package bootstrap

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// MaxEncodedSize is the maximum URL-safe payload accepted by the webhook.
const MaxEncodedSize = 8 * 1024

// Payload carries the values encoded into the bootstrap env-file.
type Payload struct {
	ManagerGRPC        string
	StaticURL          string
	SmartProxyURL      string
	ResolverUpstream   string
	ResolverListen     string
	SchedulerHost      string
	SchedulerClusterID string
	CACert             []byte
	ClientCert         []byte
	ClientKey          []byte
}

// Encode validates the payload and returns base64url(env-file).
func Encode(p Payload) (string, error) {
	env, err := Env(p)
	if err != nil {
		return "", err
	}
	encoded := base64.URLEncoding.EncodeToString([]byte(env))
	if len(encoded) > MaxEncodedSize {
		return "", fmt.Errorf("bootstrap: encoded payload is %d bytes, limit is %d", len(encoded), MaxEncodedSize)
	}
	return encoded, nil
}

// Env returns the single-line env-file representation.
func validDNSName(host string) bool {
	if len(host) > 253 || strings.HasSuffix(host, ".") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 ||
			label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func validLoopbackListen(raw string) error {
	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		return fmt.Errorf("must be an IP loopback address and port")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("must bind to a loopback address")
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	return nil
}

func Env(p Payload) (string, error) {
	manager, err := url.Parse(strings.TrimSpace(p.ManagerGRPC))
	if err != nil || manager.Scheme != "https" || manager.Host == "" {
		return "", fmt.Errorf("bootstrap: manager_grpc must be an absolute https URL")
	}
	static, err := url.Parse(strings.TrimSpace(p.StaticURL))
	if err != nil || (static.Scheme != "http" && static.Scheme != "https") || static.Host == "" {
		return "", fmt.Errorf("bootstrap: static_url must be an absolute http(s) URL")
	}
	smartProxy := strings.TrimSpace(p.SmartProxyURL)
	if smartProxy != "" {
		proxy, err := url.Parse(smartProxy)
		if err != nil || (proxy.Scheme != "http" && proxy.Scheme != "https") || proxy.Host == "" {
			return "", fmt.Errorf("bootstrap: smart_proxy_url must be an absolute http(s) URL")
		}
	}
	resolverUpstream := strings.TrimSpace(p.ResolverUpstream)
	resolverListen := strings.TrimSpace(p.ResolverListen)
	if resolverUpstream != "" {
		if resolverListen == "" {
			return "", fmt.Errorf("bootstrap: resolver_listen is required with resolver_upstream")
		}
		resolver, err := url.Parse(resolverUpstream)
		if err != nil || resolver.Scheme != "https" || resolver.Host == "" ||
			resolver.User != nil || resolver.RawQuery != "" || resolver.Fragment != "" {
			return "", fmt.Errorf("bootstrap: resolver_upstream must be an absolute https URL without query")
		}
		if err := validLoopbackListen(resolverListen); err != nil {
			return "", fmt.Errorf("bootstrap: resolver_listen: %w", err)
		}
		for _, value := range []string{resolverUpstream, resolverListen} {
			if strings.ContainsAny(value, "\"'\\") {
				return "", fmt.Errorf("bootstrap: resolver values must not contain quotes or backslashes")
			}
		}
	}

	host := strings.TrimSpace(p.SchedulerHost)
	if host == "" || !validDNSName(host) {
		return "", fmt.Errorf("bootstrap: scheduler_host must be a DNS name")
	}
	id, err := strconv.ParseInt(strings.TrimSpace(p.SchedulerClusterID), 10, 64)
	if err != nil || id < 1 {
		return "", fmt.Errorf("bootstrap: scheduler_cluster_id must be a positive integer")
	}
	if len(p.CACert) == 0 || len(p.ClientCert) == 0 || len(p.ClientKey) == 0 {
		return "", fmt.Errorf("bootstrap: CA, client certificate, and client key are required")
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "MANAGER_GRPC=%s\n", strings.TrimSpace(p.ManagerGRPC))
	fmt.Fprintf(&builder, "STATIC_URL=%s\n", strings.TrimRight(strings.TrimSpace(p.StaticURL), "/"))
	if smartProxy != "" {
		fmt.Fprintf(&builder, "SMART_PROXY_URL=%s\n", strings.TrimRight(smartProxy, "/"))
	}
	if resolverUpstream != "" {
		fmt.Fprintf(&builder, "RESOLVER_UPSTREAM=%s\n", resolverUpstream)
		fmt.Fprintf(&builder, "RESOLVER_LISTEN=%s\n", resolverListen)
	}
	fmt.Fprintf(&builder, "SCHEDULER_HOST=%s\n", host)
	fmt.Fprintf(&builder, "SCHEDULER_CLUSTER_ID=%d\n", id)
	fmt.Fprintf(&builder, "CA_CRT_B64=%s\n", base64.StdEncoding.EncodeToString(p.CACert))
	fmt.Fprintf(&builder, "CLIENT_CRT_B64=%s\n", base64.StdEncoding.EncodeToString(p.ClientCert))
	fmt.Fprintf(&builder, "CLIENT_KEY_B64=%s\n", base64.StdEncoding.EncodeToString(p.ClientKey))
	return builder.String(), nil
}

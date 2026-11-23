// Package bootstrap builds the node bootstrap payload and userData injection.
package bootstrap

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Payload carries the values encoded into the bootstrap env-file.
type Payload struct {
	ManagerGRPC        string
	StaticURL          string
	SmartProxyURL      string
	NydusMirrorURL     string
	SchedulerClusterID string
	ClientVersion      string
	NydusVersion       string
	SnapshotterVersion string
	CACert             []byte
	ClientCert         []byte
	ClientKey          []byte
}

// Env returns the single-line env-file representation.
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
	mirror := strings.TrimSpace(p.NydusMirrorURL)
	if mirror != "" {
		parsed, err := url.Parse(mirror)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
			parsed.Path == "" || parsed.Path == "/" || parsed.User != nil ||
			parsed.RawQuery != "" || parsed.Fragment != "" {
			return "", fmt.Errorf("bootstrap: nydus_mirror_url must be an absolute http(s) URL with a mirror path")
		}
		mirror = strings.TrimRight(mirror, "/")
	}
	id, err := strconv.ParseInt(strings.TrimSpace(p.SchedulerClusterID), 10, 64)
	if err != nil || id < 0 {
		return "", fmt.Errorf("bootstrap: scheduler_cluster_id must be a non-negative integer")
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
	if mirror != "" {
		fmt.Fprintf(&builder, "NYDUS_MIRROR_URL=%s\n", mirror)
	}
	fmt.Fprintf(&builder, "SCHEDULER_CLUSTER_ID=%d\n", id)
	if version := strings.TrimSpace(p.ClientVersion); version != "" {
		fmt.Fprintf(&builder, "CLIENT_VERSION=%s\n", version)
	}
	if version := strings.TrimSpace(p.NydusVersion); version != "" {
		fmt.Fprintf(&builder, "NYDUS_VERSION=%s\n", version)
	}
	if version := strings.TrimSpace(p.SnapshotterVersion); version != "" {
		fmt.Fprintf(&builder, "NYDUS_SNAPSHOTTER_VERSION=%s\n", version)
	}
	fmt.Fprintf(&builder, "CA_CRT_B64=%s\n", base64.StdEncoding.EncodeToString(p.CACert))
	fmt.Fprintf(&builder, "CLIENT_CRT_B64=%s\n", base64.StdEncoding.EncodeToString(p.ClientCert))
	fmt.Fprintf(&builder, "CLIENT_KEY_B64=%s\n", base64.StdEncoding.EncodeToString(p.ClientKey))
	return builder.String(), nil
}

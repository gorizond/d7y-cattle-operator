// Package bootstrap builds the node bootstrap payload and userData injection.
package bootstrap

import (
	"encoding/base64"
	"fmt"
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
func Env(p Payload) (string, error) {
	manager, err := url.Parse(strings.TrimSpace(p.ManagerGRPC))
	if err != nil || manager.Scheme != "https" || manager.Host == "" {
		return "", fmt.Errorf("bootstrap: manager_grpc must be an absolute https URL")
	}
	static, err := url.Parse(strings.TrimSpace(p.StaticURL))
	if err != nil || (static.Scheme != "http" && static.Scheme != "https") || static.Host == "" {
		return "", fmt.Errorf("bootstrap: static_url must be an absolute http(s) URL")
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
	fmt.Fprintf(&builder, "SCHEDULER_CLUSTER_ID=%d\n", id)
	fmt.Fprintf(&builder, "CA_CRT_B64=%s\n", base64.StdEncoding.EncodeToString(p.CACert))
	fmt.Fprintf(&builder, "CLIENT_CRT_B64=%s\n", base64.StdEncoding.EncodeToString(p.ClientCert))
	fmt.Fprintf(&builder, "CLIENT_KEY_B64=%s\n", base64.StdEncoding.EncodeToString(p.ClientKey))
	return builder.String(), nil
}

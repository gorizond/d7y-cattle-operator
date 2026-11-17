package bootstrap

import (
	"encoding/base64"
	"strings"
	"testing"
)

func testPayload() Payload {
	return Payload{
		ManagerGRPC:        "https://manager-grpc.example.com:65003",
		StaticURL:          "http://static.example.com/",
		SchedulerHost:      "d7y-scheduler.c-m-test.example.com",
		SchedulerClusterID: "2",
		CACert:             []byte("ca"),
		ClientCert:         []byte("cert"),
		ClientKey:          []byte("key"),
	}
}

func TestEncodeEnvPayload(t *testing.T) {
	encoded, err := Encode(testPayload())
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	if strings.ContainsAny(encoded, "+/") {
		t.Errorf("encoded payload is not URL-safe: %q", encoded)
	}

	raw, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 7 {
		t.Fatalf("env lines = %d, want 7: %q", len(lines), string(raw))
	}
	if lines[0] != "MANAGER_GRPC=https://manager-grpc.example.com:65003" {
		t.Errorf("MANAGER_GRPC line = %q", lines[0])
	}
	if lines[1] != "STATIC_URL=http://static.example.com" {
		t.Errorf("STATIC_URL line = %q", lines[1])
	}
	if lines[2] != "SCHEDULER_HOST=d7y-scheduler.c-m-test.example.com" {
		t.Errorf("SCHEDULER_HOST line = %q", lines[2])
	}
	if lines[3] != "SCHEDULER_CLUSTER_ID=2" {
		t.Errorf("SCHEDULER_CLUSTER_ID line = %q", lines[3])
	}
	for _, line := range lines[4:] {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("invalid env line %q", line)
		}
		decoded, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			t.Fatalf("decode %s: %v", key, err)
		}
		switch key {
		case "CA_CRT_B64":
			if string(decoded) != "ca" {
				t.Errorf("CA = %q", decoded)
			}
		case "CLIENT_CRT_B64":
			if string(decoded) != "cert" {
				t.Errorf("client cert = %q", decoded)
			}
		case "CLIENT_KEY_B64":
			if string(decoded) != "key" {
				t.Errorf("client key = %q", decoded)
			}
		default:
			t.Errorf("unexpected key %q", key)
		}
	}
}

func TestEncodeSmartProxyURL(t *testing.T) {
	payload := testPayload()
	payload.SmartProxyURL = "https://proxy.example.com/"
	encoded, err := Encode(payload)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	raw, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if !strings.Contains(string(raw), "SMART_PROXY_URL=https://proxy.example.com\n") {
		t.Errorf("payload = %q, want SMART_PROXY_URL without trailing slash", raw)
	}
}

func TestEncodeResolverConfig(t *testing.T) {
	payload := testPayload()
	payload.ResolverUpstream = "https://proxy.example.test"
	payload.ResolverListen = "127.0.0.1:3999"
	encoded, err := Encode(payload)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	raw, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	env := string(raw)
	for _, line := range []string{
		"RESOLVER_UPSTREAM=https://proxy.example.test\n",
		"RESOLVER_LISTEN=127.0.0.1:3999\n",
	} {
		if !strings.Contains(env, line) {
			t.Errorf("payload = %q, want %q", env, line)
		}
	}
}

func TestEncodeResolverRequiresListen(t *testing.T) {
	payload := testPayload()
	payload.ResolverUpstream = "https://proxy.example.test"
	if _, err := Encode(payload); err == nil {
		t.Fatal("Encode() error = nil, want resolver_listen error")
	}
}

func TestEncodeResolverDisabledWithoutUpstream(t *testing.T) {
	payload := testPayload()
	payload.ResolverListen = "127.0.0.1:3999"
	encoded, err := Encode(payload)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	raw, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if strings.Contains(string(raw), "RESOLVER_") {
		t.Errorf("resolver fields emitted without upstream: %q", raw)
	}
}

func TestEncodeRejectsInvalid(t *testing.T) {
	cases := map[string]Payload{
		"manager without https":  {ManagerGRPC: "http://manager", StaticURL: "http://static", SchedulerClusterID: "2", CACert: []byte("ca"), ClientCert: []byte("cert"), ClientKey: []byte("key")},
		"static without scheme":  {ManagerGRPC: "https://manager", StaticURL: "static", SchedulerClusterID: "2", CACert: []byte("ca"), ClientCert: []byte("cert"), ClientKey: []byte("key")},
		"missing ID":             {ManagerGRPC: "https://manager", StaticURL: "http://static", SchedulerHost: "scheduler.example.com", CACert: []byte("ca"), ClientCert: []byte("cert"), ClientKey: []byte("key")},
		"zero ID":                {ManagerGRPC: "https://manager", StaticURL: "http://static", SchedulerHost: "scheduler.example.com", SchedulerClusterID: "0", CACert: []byte("ca"), ClientCert: []byte("cert"), ClientKey: []byte("key")},
		"missing cert":           {ManagerGRPC: "https://manager", StaticURL: "http://static", SchedulerHost: "scheduler.example.com", SchedulerClusterID: "2", CACert: []byte("ca"), ClientCert: []byte("cert")},
		"missing scheduler host": {ManagerGRPC: "https://manager", StaticURL: "http://static", SchedulerClusterID: "2", CACert: []byte("ca"), ClientCert: []byte("cert"), ClientKey: []byte("key")},
		"invalid scheduler host": {ManagerGRPC: "https://manager", StaticURL: "http://static", SchedulerHost: "bad_host", SchedulerClusterID: "2", CACert: []byte("ca"), ClientCert: []byte("cert"), ClientKey: []byte("key")},
		"invalid smart proxy":    {ManagerGRPC: "https://manager", StaticURL: "http://static", SmartProxyURL: "ftp://proxy", SchedulerHost: "scheduler.example.com", SchedulerClusterID: "2", CACert: []byte("ca"), ClientCert: []byte("cert"), ClientKey: []byte("key")},
		"resolver http":          {ManagerGRPC: "https://manager", StaticURL: "http://static", ResolverUpstream: "http://proxy.example.test", ResolverListen: "127.0.0.1:3999", SchedulerHost: "scheduler.example.com", SchedulerClusterID: "2", CACert: []byte("ca"), ClientCert: []byte("cert"), ClientKey: []byte("key")},
		"resolver public listen": {ManagerGRPC: "https://manager", StaticURL: "http://static", ResolverUpstream: "https://proxy.example.test", ResolverListen: "0.0.0.0:3999", SchedulerHost: "scheduler.example.com", SchedulerClusterID: "2", CACert: []byte("ca"), ClientCert: []byte("cert"), ClientKey: []byte("key")},
		"resolver quoted":        {ManagerGRPC: "https://manager", StaticURL: "http://static", ResolverUpstream: "https://proxy.example.test/?q=\"x\"", ResolverListen: "127.0.0.1:3999", SchedulerHost: "scheduler.example.com", SchedulerClusterID: "2", CACert: []byte("ca"), ClientCert: []byte("cert"), ClientKey: []byte("key")},
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Encode(payload); err == nil {
				t.Fatal("Encode() error = nil, want an error")
			}
		})
	}
}

func TestEncodeSizeLimit(t *testing.T) {
	payload := testPayload()
	payload.CACert = []byte(strings.Repeat("ca", MaxEncodedSize))
	if _, err := Encode(payload); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("Encode() error = %v, want size limit error", err)
	}
}

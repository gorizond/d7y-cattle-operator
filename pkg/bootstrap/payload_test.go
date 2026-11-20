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

func TestEnvPayload(t *testing.T) {
	raw, err := Env(testPayload())
	if err != nil {
		t.Fatalf("Env() error = %v", err)
	}
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	if len(lines) != 7 {
		t.Fatalf("env lines = %d, want 7: %q", len(lines), raw)
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

func TestEnvSmartProxyURL(t *testing.T) {
	payload := testPayload()
	payload.SmartProxyURL = "https://proxy.example.com/"
	raw, err := Env(payload)
	if err != nil {
		t.Fatalf("Env() error = %v", err)
	}
	if !strings.Contains(raw, "SMART_PROXY_URL=https://proxy.example.com\n") {
		t.Errorf("payload = %q, want SMART_PROXY_URL without trailing slash", raw)
	}
}

func TestEnvNydusMirrorURL(t *testing.T) {
	payload := testPayload()
	payload.NydusMirrorURL = "https://proxy.example.com/docker.io/"
	raw, err := Env(payload)
	if err != nil {
		t.Fatalf("Env() error = %v", err)
	}
	if !strings.Contains(raw, "NYDUS_MIRROR_URL=https://proxy.example.com/docker.io\n") {
		t.Errorf("payload = %q, want NYDUS_MIRROR_URL with the mirror path", raw)
	}
}

func TestEnvRejectsInvalid(t *testing.T) {
	cases := map[string]Payload{
		"manager without https":  {ManagerGRPC: "http://manager", StaticURL: "http://static", SchedulerClusterID: "2", CACert: []byte("ca"), ClientCert: []byte("cert"), ClientKey: []byte("key")},
		"static without scheme":  {ManagerGRPC: "https://manager", StaticURL: "static", SchedulerClusterID: "2", CACert: []byte("ca"), ClientCert: []byte("cert"), ClientKey: []byte("key")},
		"missing ID":             {ManagerGRPC: "https://manager", StaticURL: "http://static", SchedulerHost: "scheduler.example.com", CACert: []byte("ca"), ClientCert: []byte("cert"), ClientKey: []byte("key")},
		"zero ID":                {ManagerGRPC: "https://manager", StaticURL: "http://static", SchedulerHost: "scheduler.example.com", SchedulerClusterID: "0", CACert: []byte("ca"), ClientCert: []byte("cert"), ClientKey: []byte("key")},
		"missing cert":           {ManagerGRPC: "https://manager", StaticURL: "http://static", SchedulerHost: "scheduler.example.com", SchedulerClusterID: "2", CACert: []byte("ca"), ClientCert: []byte("cert")},
		"missing scheduler host": {ManagerGRPC: "https://manager", StaticURL: "http://static", SchedulerClusterID: "2", CACert: []byte("ca"), ClientCert: []byte("cert"), ClientKey: []byte("key")},
		"invalid scheduler host": {ManagerGRPC: "https://manager", StaticURL: "http://static", SchedulerHost: "bad_host", SchedulerClusterID: "2", CACert: []byte("ca"), ClientCert: []byte("cert"), ClientKey: []byte("key")},
		"invalid smart proxy":    {ManagerGRPC: "https://manager", StaticURL: "http://static", SmartProxyURL: "ftp://proxy", SchedulerHost: "scheduler.example.com", SchedulerClusterID: "2", CACert: []byte("ca"), ClientCert: []byte("cert"), ClientKey: []byte("key")},
		"invalid nydus mirror":   {ManagerGRPC: "https://manager", StaticURL: "http://static", NydusMirrorURL: "https://proxy.example.com", SchedulerHost: "scheduler.example.com", SchedulerClusterID: "2", CACert: []byte("ca"), ClientCert: []byte("cert"), ClientKey: []byte("key")},
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Env(payload); err == nil {
				t.Fatal("Env() error = nil, want an error")
			}
		})
	}
}

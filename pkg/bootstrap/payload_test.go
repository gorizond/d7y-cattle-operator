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
	if len(lines) != 6 {
		t.Fatalf("env lines = %d, want 6: %q", len(lines), raw)
	}
	if strings.Contains(raw, "SCHEDULER_HOST=") {
		t.Errorf("Env() must not emit the legacy SCHEDULER_HOST: %q", raw)
	}
	if lines[0] != "MANAGER_GRPC=https://manager-grpc.example.com:65003" {
		t.Errorf("MANAGER_GRPC line = %q", lines[0])
	}
	if lines[1] != "STATIC_URL=http://static.example.com" {
		t.Errorf("STATIC_URL line = %q", lines[1])
	}
	if lines[2] != "SCHEDULER_CLUSTER_ID=2" {
		t.Errorf("SCHEDULER_CLUSTER_ID line = %q", lines[2])
	}
	for _, line := range lines[3:] {
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
		"manager without https": {ManagerGRPC: "http://manager", StaticURL: "http://static", SchedulerClusterID: "2", CACert: []byte("ca"), ClientCert: []byte("cert"), ClientKey: []byte("key")},
		"static without scheme": {ManagerGRPC: "https://manager", StaticURL: "static", SchedulerClusterID: "2", CACert: []byte("ca"), ClientCert: []byte("cert"), ClientKey: []byte("key")},
		"missing ID":            {ManagerGRPC: "https://manager", StaticURL: "http://static", CACert: []byte("ca"), ClientCert: []byte("cert"), ClientKey: []byte("key")},
		"missing cert":          {ManagerGRPC: "https://manager", StaticURL: "http://static", SchedulerClusterID: "2", CACert: []byte("ca"), ClientCert: []byte("cert")},
		"invalid smart proxy":   {ManagerGRPC: "https://manager", StaticURL: "http://static", SmartProxyURL: "ftp://proxy", SchedulerClusterID: "2", CACert: []byte("ca"), ClientCert: []byte("cert"), ClientKey: []byte("key")},
		"invalid nydus mirror":  {ManagerGRPC: "https://manager", StaticURL: "http://static", NydusMirrorURL: "https://proxy.example.com", SchedulerClusterID: "2", CACert: []byte("ca"), ClientCert: []byte("cert"), ClientKey: []byte("key")},
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Env(payload); err == nil {
				t.Fatal("Env() error = nil, want an error")
			}
		})
	}
}

func TestEnvAllowsZeroSchedulerIDPlaceholder(t *testing.T) {
	payload := testPayload()
	payload.SchedulerClusterID = "0"
	raw, err := Env(payload)
	if err != nil {
		t.Fatalf("Env() error = %v", err)
	}
	if !strings.Contains(raw, "SCHEDULER_CLUSTER_ID=0\n") {
		t.Errorf("payload = %q, want the zero scheduler ID placeholder", raw)
	}
}

func TestEnvVersions(t *testing.T) {
	payload := testPayload()
	payload.ClientVersion = "v1.5.14"
	payload.NydusVersion = "v2.4.5"
	payload.SnapshotterVersion = "v0.16.0"
	raw, err := Env(payload)
	if err != nil {
		t.Fatalf("Env() error = %v", err)
	}
	for _, want := range []string{
		"CLIENT_VERSION=v1.5.14\n",
		"NYDUS_VERSION=v2.4.5\n",
		"NYDUS_SNAPSHOTTER_VERSION=v0.16.0\n",
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("payload missing %q: %q", want, raw)
		}
	}
}

package nydusresolver

import (
	"reflect"
	"testing"
)

func TestDefaultRegistryMap(t *testing.T) {
	want := map[string]string{
		"registry-1.docker.io": "docker.io",
		"index.docker.io":      "docker.io",
		"docker.io":            "docker.io",
		"ghcr.io":              "ghcr.io",
		"quay.io":              "quay.io",
		"registry.k8s.io":      "registry.k8s.io",
		"gcr.io":               "gcr.io",
		"mcr.microsoft.com":    "mcr.microsoft.com",
	}
	if got := DefaultRegistryMap(); !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultRegistryMap() = %#v, want %#v", got, want)
	}
}

func TestParseRegistryMap(t *testing.T) {
	got, err := ParseRegistryMap(" Registry.Example.COM:443 = mirror/registry.example.com , ghcr.io=ghcr ")
	if err != nil {
		t.Fatalf("ParseRegistryMap: %v", err)
	}
	want := map[string]string{
		"registry.example.com": "mirror/registry.example.com",
		"ghcr.io":              "ghcr",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseRegistryMap() = %#v, want %#v", got, want)
	}
	if empty, err := ParseRegistryMap("  "); err != nil || len(empty) != 0 {
		t.Fatalf("empty ParseRegistryMap = %#v, %v", empty, err)
	}
	for _, invalid := range []string{
		"ghcr.io",
		"=docker.io",
		"ghcr.io=",
		"ghcr.io=../escape",
		"bad host=docker.io",
		"ghcr.io=a?b",
		"ghcr.io=a,,b=c",
	} {
		if _, err := ParseRegistryMap(invalid); err == nil {
			t.Errorf("ParseRegistryMap(%q) succeeded, want error", invalid)
		}
	}
}

func TestMergeRegistryMaps(t *testing.T) {
	base := DefaultRegistryMap()
	merged := MergeRegistryMaps(base, map[string]string{
		"registry-1.docker.io": "mirror/docker.io",
		"registry.example.com": "mirror/registry.example.com",
	})
	if merged["registry-1.docker.io"] != "mirror/docker.io" {
		t.Fatalf("override not applied: %q", merged["registry-1.docker.io"])
	}
	if merged["registry.example.com"] != "mirror/registry.example.com" {
		t.Fatalf("new mapping missing: %q", merged["registry.example.com"])
	}
	if base["registry-1.docker.io"] != "docker.io" {
		t.Fatalf("MergeRegistryMaps mutated its base map")
	}
}

func TestNormalizeRegistryHost(t *testing.T) {
	for input, want := range map[string]string{
		"registry-1.docker.io":       "registry-1.docker.io",
		" Registry-1.Docker.IO:443 ": "registry-1.docker.io",
		"ghcr.io.":                   "ghcr.io",
		"ghcr.io:":                   "ghcr.io",
		"":                           "",
	} {
		if got := NormalizeRegistryHost(input); got != want {
			t.Errorf("NormalizeRegistryHost(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRewriteRegistryPath(t *testing.T) {
	tests := []struct {
		path   string
		mapped string
		want   string
		ok     bool
	}{
		{"/v2/", "docker.io", "/docker.io/v2/", true},
		{"/v2", "docker.io", "/docker.io/v2", true},
		{"/v2/library/alpine/manifests/latest", "docker.io", "/docker.io/v2/library/alpine/manifests/latest", true},
		{"/v2/foo/blobs/sha256:abc", "mirror/ghcr.io", "/mirror/ghcr.io/v2/foo/blobs/sha256:abc", true},
		{"/v2evil", "docker.io", "", false},
		{"/v1/", "docker.io", "", false},
		{"/", "docker.io", "", false},
		{"/v2/", "", "", false},
		{"/v2/", "/", "", false},
	}
	for _, test := range tests {
		got, ok := RewriteRegistryPath(test.path, test.mapped)
		if got != test.want || ok != test.ok {
			t.Errorf("RewriteRegistryPath(%q, %q) = %q, %t; want %q, %t",
				test.path, test.mapped, got, ok, test.want, test.ok)
		}
	}
}

package nydusresolver

import "testing"

func TestNormalizeRegistryHost(t *testing.T) {
	for input, want := range map[string]string{
		"registry.example.test":       "registry.example.test",
		" Registry.Example.Test:443 ": "registry.example.test",
		"mirror.example.test.":        "mirror.example.test",
		"mirror.example.test:":        "mirror.example.test",
		"":                            "",
	} {
		if got := NormalizeRegistryHost(input); got != want {
			t.Errorf("NormalizeRegistryHost(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRegistryPath(t *testing.T) {
	tests := []struct {
		path string
		host string
		want string
		ok   bool
	}{
		{"/v2/", "registry.example.test", "/registry.example.test/v2/", true},
		{"/v2", "registry.example.test", "/registry.example.test/v2", true},
		{"/v2/library/alpine/manifests/latest", "registry.example.test", "/registry.example.test/v2/library/alpine/manifests/latest", true},
		{"/v2/foo/blobs/sha256:abc", "mirror.example.test", "/mirror.example.test/v2/foo/blobs/sha256:abc", true},
		{"/v2/foo", "Registry.Example.Test:443", "/registry.example.test/v2/foo", true},
		{"/v2evil", "registry.example.test", "", false},
		{"/v1/", "registry.example.test", "", false},
		{"/", "registry.example.test", "", false},
		{"/v2/", "", "", false},
		{"/v2/", "bad_host", "", false},
	}
	for _, test := range tests {
		got, ok := RegistryPath(test.path, test.host)
		if got != test.want || ok != test.ok {
			t.Errorf("RegistryPath(%q, %q) = %q, %t; want %q, %t",
				test.path, test.host, got, ok, test.want, test.ok)
		}
	}
}

func TestValidateRegistryHost(t *testing.T) {
	valid := []string{
		"registry.example.test",
		"mirror.example.test",
		"a-b.example.test",
		"127.0.0.1",
	}
	for _, host := range valid {
		if err := validateRegistryHost(host); err != nil {
			t.Errorf("validateRegistryHost(%q) = %v", host, err)
		}
	}
	invalid := []string{
		"",
		"bad_host",
		"-bad.example.test",
		"bad-.example.test",
		".example.test",
		"example.test.",
		"a..b",
		"host:443",
	}
	for _, host := range invalid {
		if err := validateRegistryHost(host); err == nil {
			t.Errorf("validateRegistryHost(%q) succeeded, want error", host)
		}
	}
}

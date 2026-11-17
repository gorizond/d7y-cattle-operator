package nydusresolver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validConfig() Config {
	return Config{
		Listen:       "127.0.0.1:3999",
		Upstream:     "https://proxy.example.test",
		CAKey:        "/var/lib/nydus-resolver/ca.key",
		CABundle:     "/var/lib/nydus-resolver/ca-bundle.pem",
		SystemCAFile: "/etc/ssl/certs/ca-certificates.crt",
	}
}

func TestConfigValidate(t *testing.T) {
	cfg := validConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid config: %v", err)
	}
}

func TestConfigRequiresEveryField(t *testing.T) {
	for name, clear := range map[string]func(*Config){
		"listen":         func(c *Config) { c.Listen = "" },
		"upstream":       func(c *Config) { c.Upstream = "" },
		"ca_key":         func(c *Config) { c.CAKey = "" },
		"ca_bundle":      func(c *Config) { c.CABundle = "" },
		"system_ca_file": func(c *Config) { c.SystemCAFile = "" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := validConfig()
			clear(&cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate() error = nil, want an error")
			}
			if !strings.Contains(err.Error(), name) {
				t.Fatalf("Validate() error = %v, want %s", err, name)
			}
		})
	}
}

func TestConfigHasNoDefaults(t *testing.T) {
	var cfg Config
	err := cfg.Validate()
	if err == nil {
		t.Fatal("empty config passed validation, want required-field errors")
	}
	for _, field := range []string{"listen", "upstream", "ca_key", "ca_bundle", "system_ca_file"} {
		if !strings.Contains(err.Error(), field) {
			t.Errorf("empty config error = %v, want %s", err, field)
		}
	}
}

func TestConfigInvalidValues(t *testing.T) {
	cases := map[string]func(*Config){
		"non-loopback listen": func(c *Config) { c.Listen = "0.0.0.0:3999" },
		"missing port":        func(c *Config) { c.Listen = "127.0.0.1" },
		"zero port":           func(c *Config) { c.Listen = "127.0.0.1:0" },
		"http upstream":       func(c *Config) { c.Upstream = "http://proxy.example.test" },
		"upstream query":      func(c *Config) { c.Upstream = "https://proxy.example.test?x=1" },
		"upstream creds":      func(c *Config) { c.Upstream = "https://user:pass@proxy.example.test" },
		"relative ca key":     func(c *Config) { c.CAKey = "ca.key" },
		"relative system CA":  func(c *Config) { c.SystemCAFile = "ca-certificates.crt" },
		"same key and bundle": func(c *Config) { c.CABundle = c.CAKey },
		"key is system CA":    func(c *Config) { c.CAKey = c.SystemCAFile },
		"bundle is system CA": func(c *Config) { c.CABundle = c.SystemCAFile },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := validConfig()
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want an error")
			}
		})
	}
}

func TestLoadConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `
listen: 127.0.0.1:3999
upstream: https://proxy.example.test
ca_key: /var/lib/nydus-resolver/ca.key
ca_bundle: /var/lib/nydus-resolver/ca-bundle.pem
system_ca_file: /etc/ssl/certs/ca-certificates.crt
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Upstream != "https://proxy.example.test" || cfg.Listen != "127.0.0.1:3999" {
		t.Fatalf("config = %+v", cfg)
	}

	if _, err := LoadConfig(""); err == nil {
		t.Fatal("LoadConfig(\"\") error = nil, want an error")
	}
	if _, err := LoadConfig(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("LoadConfig(missing) error = nil, want an error")
	}

	unknown := filepath.Join(t.TempDir(), "unknown.yaml")
	if err := os.WriteFile(unknown, []byte(content+"unknown_key: value\n"), 0o600); err != nil {
		t.Fatalf("write unknown config: %v", err)
	}
	if _, err := LoadConfig(unknown); err == nil {
		t.Fatal("LoadConfig(unknown field) error = nil, want an error")
	}
}

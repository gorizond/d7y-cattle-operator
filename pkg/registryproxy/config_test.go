package registryproxy

import (
	"os"
	"path/filepath"
	"testing"
)

const testConfigYAML = `
listen_addr: ":8080"
harbor:
  url: https://harbor.example.com
  admin_secret: harbor-admin
  robot_secret: registry-proxy-robot
mirror:
  project: mirror
registry:
  allowlist:
    - docker.io
proxy:
  public_url: https://proxy.example.com
webhook:
  secret_name: registry-proxy-webhook
conversion:
  platform: linux/amd64
  nydusify: /usr/local/bin/nydusify
  nydus_image: /usr/local/bin/nydus-image
  work_dir: /tmp/nydusify
  timeout: 10m
service:
  port: 8080
`

func testConfig(t *testing.T) *Config {
	t.Helper()
	cfg, err := Load(writeConfig(t, testConfigYAML))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	return cfg
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfig(t *testing.T) {
	cfg := testConfig(t)
	if cfg.RegistryURL() != "https://harbor.example.com" || cfg.Mirror.Project != "mirror" {
		t.Errorf("config = %+v", cfg)
	}
	if !cfg.Allowlisted("docker.io") || !cfg.Allowlisted("registry-1.docker.io") || cfg.Allowlisted("ghcr.io") {
		t.Error("allowlist check failed")
	}
	if cfg.ProjectName("docker.io") != "dockerio" {
		t.Errorf("project mapping = %q", cfg.ProjectName("docker.io"))
	}
	if cfg.ProjectName("registry-1.docker.io") != "dockerio" {
		t.Errorf("alias project mapping = %q", cfg.ProjectName("registry-1.docker.io"))
	}
	if cfg.Webhook.Address != "https://proxy.example.com/api/convert" {
		t.Errorf("webhook.address = %q, want the public URL default", cfg.Webhook.Address)
	}
}

func TestLoadConfigEnvOverride(t *testing.T) {
	t.Setenv("REGISTRY_ALLOWLIST", "registry-1.docker.io,ghcr.io")
	t.Setenv("PROXY_PUBLIC_URL", "https://proxy.env.example.com")
	t.Setenv("WEBHOOK_ADDRESS", "http://c1-registry-proxy.dragonfly-system.svc:8080/api/convert")
	cfg := testConfig(t)
	if len(cfg.Registry.Allowlist) != 2 || cfg.Proxy.PublicURL != "https://proxy.env.example.com" {
		t.Errorf("env override = %+v", cfg)
	}
	if cfg.Registry.Allowlist[0] != "docker.io" || !cfg.Allowlisted("index.docker.io") {
		t.Errorf("allowlist was not canonicalized: %+v", cfg.Registry.Allowlist)
	}
	if cfg.Webhook.Address != "http://c1-registry-proxy.dragonfly-system.svc:8080/api/convert" {
		t.Errorf("webhook.address = %q", cfg.Webhook.Address)
	}
}

func TestConfigRejectsInvalid(t *testing.T) {
	cases := map[string]string{
		"missing harbor":      "registry: {allowlist: [docker.io]}\nmirror: {project: mirror}\nproxy: {public_url: https://proxy.example.com}\nwebhook: {secret_name: s}\nconversion: {nydusify: /bin/nydusify, nydus_image: /bin/nydus-image}\n",
		"empty allowlist":     "harbor: {url: https://harbor.example.com}\nmirror: {project: mirror}\nproxy: {public_url: https://proxy.example.com}\nwebhook: {secret_name: s}\nconversion: {nydusify: /bin/nydusify, nydus_image: /bin/nydus-image}\n",
		"bad nydus path":      "harbor: {url: https://harbor.example.com}\nmirror: {project: mirror}\nregistry: {allowlist: [docker.io]}\nproxy: {public_url: https://proxy.example.com}\nwebhook: {secret_name: s}\nconversion: {nydusify: /bin/nydusify, nydus_image: nydus-image}\n",
		"bad webhook address": "harbor: {url: https://harbor.example.com}\nmirror: {project: mirror}\nregistry: {allowlist: [docker.io]}\nproxy: {public_url: https://proxy.example.com}\nwebhook: {secret_name: s, address: not-a-url}\nconversion: {nydusify: /bin/nydusify, nydus_image: /bin/nydus-image}\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, content)); err == nil {
				t.Fatal("Load() error = nil, want error")
			}
		})
	}
}

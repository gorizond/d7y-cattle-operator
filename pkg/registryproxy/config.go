package registryproxy

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the non-secret registry-proxy configuration loaded from the chart
// ConfigMap. Secret values are read from mounted Secret files by the command.
type Config struct {
	ListenAddr string `yaml:"listen_addr"`
	Harbor     struct {
		URL         string `yaml:"url"`
		AdminSecret string `yaml:"admin_secret"`
	} `yaml:"harbor"`
	Mirror struct {
		Project string `yaml:"project"`
	} `yaml:"mirror"`
	Registry struct {
		Allowlist []string `yaml:"allowlist"`
	} `yaml:"registry"`
	Proxy struct {
		PublicURL string `yaml:"public_url"`
		BasePath  string `yaml:"base_path"`
	} `yaml:"proxy"`
	Webhook struct {
		SecretName string `yaml:"secret_name"`
		Address    string `yaml:"address"`
	} `yaml:"webhook"`
	Auth struct {
		// Mode is "basic" for client Basic auth or "" / "network-only" to rely
		// on the network boundary (NetworkPolicy/TLS).
		Mode         string `yaml:"mode"`
		Username     string `yaml:"username"`
		PasswordFile string `yaml:"password_file"`
	} `yaml:"auth"`
	Upstream struct {
		// CredentialsFile is an optional YAML map of registry host to
		// {access_key, access_secret} for private upstreams. It is mounted
		// from a Secret; the credentials never enter this ConfigMap.
		CredentialsFile string `yaml:"credentials_file"`
	} `yaml:"upstream"`
	Conversion struct {
		Platform   string `yaml:"platform"`
		Nydusify   string `yaml:"nydusify"`
		NydusImage string `yaml:"nydus_image"`
		WorkDir    string `yaml:"work_dir"`
		Timeout    string `yaml:"timeout"`
		// JobTimeout bounds one copy/conversion attempt. Empty uses the
		// manager default (30m).
		JobTimeout string `yaml:"job_timeout"`
	} `yaml:"conversion"`
	Service struct {
		Port int `yaml:"port"`
	} `yaml:"service"`
}

// Load reads a YAML config file and applies environment overrides.
func Load(path string) (*Config, error) {
	cfg := &Config{}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	applyEnv(cfg)
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func applyEnv(cfg *Config) {
	if value := os.Getenv("LISTEN_ADDR"); value != "" {
		cfg.ListenAddr = value
	}
	if value := os.Getenv("HARBOR_URL"); value != "" {
		cfg.Harbor.URL = value
	}
	if value := os.Getenv("MIRROR_PROJECT"); value != "" {
		cfg.Mirror.Project = value
	}
	if value := os.Getenv("PROXY_PUBLIC_URL"); value != "" {
		cfg.Proxy.PublicURL = value
	}
	if value := os.Getenv("PROXY_BASE_PATH"); value != "" {
		cfg.Proxy.BasePath = value
	}
	if value := os.Getenv("WEBHOOK_SECRET_NAME"); value != "" {
		cfg.Webhook.SecretName = value
	}
	if value := os.Getenv("WEBHOOK_ADDRESS"); value != "" {
		cfg.Webhook.Address = value
	}
	if value := os.Getenv("REGISTRY_ALLOWLIST"); value != "" {
		cfg.Registry.Allowlist = splitCSV(value)
	}
	if value := os.Getenv("PROXY_AUTH_MODE"); value != "" {
		cfg.Auth.Mode = value
	}
	if value := os.Getenv("PROXY_AUTH_USERNAME"); value != "" {
		cfg.Auth.Username = value
	}
	if value := os.Getenv("PROXY_AUTH_PASSWORD_FILE"); value != "" {
		cfg.Auth.PasswordFile = value
	}
	if value := os.Getenv("UPSTREAM_CREDENTIALS_FILE"); value != "" {
		cfg.Upstream.CredentialsFile = value
	}
	if value := os.Getenv("CONVERSION_NYDUSIFY"); value != "" {
		cfg.Conversion.Nydusify = value
	}
	if value := os.Getenv("CONVERSION_NYDUS_IMAGE"); value != "" {
		cfg.Conversion.NydusImage = value
	}
	if value := os.Getenv("CONVERSION_WORK_DIR"); value != "" {
		cfg.Conversion.WorkDir = value
	}
	if value := os.Getenv("CONVERSION_TIMEOUT"); value != "" {
		cfg.Conversion.Timeout = value
	}
	if value := os.Getenv("CONVERSION_JOB_TIMEOUT"); value != "" {
		cfg.Conversion.JobTimeout = value
	}
}

func splitCSV(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}

// Validate checks configuration values that do not require Secret contents.
func (c *Config) Validate() error {
	if c.ListenAddr == "" && c.Service.Port > 0 {
		c.ListenAddr = ":" + strconv.Itoa(c.Service.Port)
	}
	if c.ListenAddr == "" {
		return fmt.Errorf("listen_addr or service.port is required")
	}
	if c.Harbor.URL == "" {
		return fmt.Errorf("harbor.url is required")
	}
	u, err := url.Parse(c.Harbor.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("harbor.url must be an absolute https URL (nydusify requires HTTPS)")
	}
	if c.Mirror.Project == "" {
		return fmt.Errorf("mirror.project is required")
	}
	if len(c.Registry.Allowlist) == 0 {
		return fmt.Errorf("registry.allowlist must not be empty")
	}
	for i, registry := range c.Registry.Allowlist {
		registry = normalizeRegistryAlias(strings.TrimSpace(registry))
		c.Registry.Allowlist[i] = registry
		if registry == "" || strings.ContainsAny(registry, "/:") {
			return fmt.Errorf("registry.allowlist contains invalid registry %q", registry)
		}
	}
	if c.Proxy.BasePath != "" {
		if !strings.HasPrefix(c.Proxy.BasePath, "/") || strings.ContainsAny(c.Proxy.BasePath, "?#") ||
			strings.Contains(c.Proxy.BasePath, "..") || strings.HasSuffix(c.Proxy.BasePath, "/") {
			return fmt.Errorf("proxy.base_path must start with /, have no query, no trailing slash")
		}
	}
	if c.Proxy.PublicURL == "" {
		return fmt.Errorf("proxy.public_url is required")
	}
	p, err := url.Parse(c.Proxy.PublicURL)
	if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
		return fmt.Errorf("proxy.public_url must be an absolute http(s) URL")
	}
	if c.Webhook.Address == "" {
		c.Webhook.Address = strings.TrimRight(c.Proxy.PublicURL, "/") + "/api/convert"
	} else if u, err := url.Parse(c.Webhook.Address); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("webhook.address must be an absolute http(s) URL")
	}
	if c.Webhook.SecretName == "" {
		return fmt.Errorf("webhook.secret_name is required")
	}
	switch c.Auth.Mode {
	case "", "network-only":
	case "basic":
		if c.Auth.Username == "" || c.Auth.PasswordFile == "" {
			return fmt.Errorf("auth.username and auth.password_file are required for basic auth")
		}
	default:
		return fmt.Errorf("auth.mode must be empty, network-only, or basic")
	}
	if c.Upstream.CredentialsFile != "" && !strings.HasPrefix(c.Upstream.CredentialsFile, "/") {
		return fmt.Errorf("upstream.credentials_file must be an absolute path")
	}
	if c.Conversion.Nydusify == "" || !strings.HasPrefix(c.Conversion.Nydusify, "/") {
		return fmt.Errorf("conversion.nydusify must be an absolute binary path")
	}
	if c.Conversion.NydusImage == "" || !strings.HasPrefix(c.Conversion.NydusImage, "/") {
		return fmt.Errorf("conversion.nydus_image must be an absolute binary path")
	}
	if c.Conversion.Platform == "" {
		c.Conversion.Platform = "linux/amd64"
	}
	if c.Conversion.Timeout != "" {
		if _, err := time.ParseDuration(c.Conversion.Timeout); err != nil {
			return fmt.Errorf("conversion.timeout: %w", err)
		}
	}
	if c.Conversion.JobTimeout != "" {
		if _, err := time.ParseDuration(c.Conversion.JobTimeout); err != nil {
			return fmt.Errorf("conversion.job_timeout: %w", err)
		}
	}
	return nil
}

// Allowlisted reports whether the registry may create Harbor projects.
// "*" allows any explicit registry; an empty registry is never allowed.
func (c *Config) Allowlisted(registry string) bool {
	registry = normalizeRegistryAlias(registry)
	if registry == "" || registry == "*" {
		return false
	}
	for _, allowed := range c.Registry.Allowlist {
		if normalizeRegistryAlias(allowed) == "*" {
			return true
		}
		if normalizeRegistryAlias(allowed) == registry {
			return true
		}
	}
	return false
}

// ProjectName maps an upstream registry to a Harbor-safe project name.
func (c *Config) ProjectName(registry string) string {
	return ProjectName(registry)
}

// ProjectName maps registry host names to Harbor project names (docker.io ->
// dockerio). Dots are rejected by Harbor project naming in the pilot.
func ProjectName(registry string) string {
	return strings.NewReplacer(".", "", ":", "", "/", "").Replace(normalizeRegistryAlias(registry))
}

// BasePath returns the normalized public path prefix.
func (c *Config) BasePath() string {
	return strings.TrimRight(c.Proxy.BasePath, "/")
}

// SelfRegistry reports whether registry names the proxy's own public host.
// dfdaemon can add ?ns=<proxy host> for admission-rewritten refs; that hint
// must not shadow the direct-ref registry segment.
func (c *Config) SelfRegistry(registry string) bool {
	if c == nil || c.Proxy.PublicURL == "" || registry == "" {
		return false
	}
	parsed, err := url.Parse(c.Proxy.PublicURL)
	if err != nil || parsed.Hostname() == "" {
		return false
	}
	return normalizeRegistryAlias(registry) == normalizeRegistryAlias(parsed.Hostname())
}

// AuthPassword reads the mounted Basic-auth password file.
func (c *Config) AuthPassword() (string, error) {
	if c.Auth.PasswordFile == "" {
		return "", nil
	}
	raw, err := os.ReadFile(c.Auth.PasswordFile)
	if err != nil {
		return "", fmt.Errorf("read auth.password_file: %w", err)
	}
	return strings.TrimSpace(string(raw)), nil
}

// LoadUpstreamCredentials reads the optional Secret-mounted private upstream
// credentials. The file is a YAML map of registry host to access_key /
// access_secret; empty path means no private upstreams.
func LoadUpstreamCredentials(path string) (map[string]HarborCredential, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read upstream.credentials_file: %w", err)
	}
	var file map[string]HarborCredential
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("parse upstream.credentials_file: %w", err)
	}
	credentials := make(map[string]HarborCredential, len(file))
	for host, credential := range file {
		host = normalizeRegistryAlias(host)
		if host == "" || credential.AccessKey == "" {
			continue
		}
		credentials[host] = credential
	}
	return credentials, nil
}

// RegistryURL returns the configured Harbor base URL without a trailing slash.
func (c *Config) RegistryURL() string {
	return strings.TrimRight(c.Harbor.URL, "/")
}

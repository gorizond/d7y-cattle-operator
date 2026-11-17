package nydusresolver

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the non-secret resolver configuration. Every field is required;
// there are no built-in upstream or listen defaults.
type Config struct {
	Listen       string `yaml:"listen"`
	Upstream     string `yaml:"upstream"`
	CAKey        string `yaml:"ca_key"`
	CABundle     string `yaml:"ca_bundle"`
	SystemCAFile string `yaml:"system_ca_file"`
}

// LoadConfig reads and validates a YAML config. Unknown fields are rejected so
// a misspelled key cannot silently fall back to a zero value.
func LoadConfig(path string) (*Config, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("config path is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open config: %w", err)
	}
	defer file.Close()

	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate checks required fields and the external configuration contract.
func (c *Config) Validate() error {
	var errs []string
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Sprintf(format, args...))
	}

	if strings.TrimSpace(c.Listen) == "" {
		add("listen is required")
	} else if err := validateListen(c.Listen); err != nil {
		add("listen: %v", err)
	}
	if strings.TrimSpace(c.Upstream) == "" {
		add("upstream is required")
	} else if err := validateUpstream(c.Upstream); err != nil {
		add("upstream: %v", err)
	}
	for name, value := range map[string]string{
		"ca_key":         c.CAKey,
		"ca_bundle":      c.CABundle,
		"system_ca_file": c.SystemCAFile,
	} {
		if strings.TrimSpace(value) == "" {
			add("%s is required", name)
			continue
		}
		if !filepath.IsAbs(value) {
			add("%s must be an absolute path", name)
		}
	}
	if c.CAKey != "" && c.CAKey == c.CABundle {
		add("ca_key and ca_bundle must be different files")
	}
	if c.CAKey != "" && c.CAKey == c.SystemCAFile {
		add("ca_key must not overwrite system_ca_file")
	}
	if c.CABundle != "" && c.CABundle == c.SystemCAFile {
		add("ca_bundle must not overwrite system_ca_file")
	}

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func validateListen(raw string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(raw))
	if err != nil {
		return errors.New("must be an IP loopback address and port")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("must bind to a loopback address")
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	return nil
}

func validateUpstream(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return errors.New("must be an absolute https URL")
	}
	if parsed.Scheme != "https" {
		return errors.New("scheme must be https")
	}
	if parsed.User != nil {
		return errors.New("must not contain credentials")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("must not contain a query or fragment")
	}
	return nil
}

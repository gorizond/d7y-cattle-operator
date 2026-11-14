// Package config parses and validates the operator configuration.
package config

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// Defaults shared with the Helm chart.
const (
	DefaultMTLSSecretName                = "dragonfly-p2p-mtls"
	DefaultManagerPublicTLSSecret        = ""
	DefaultDragonflyChartName            = "dragonfly"
	DefaultDragonflyChartVersion         = "1.10.1"
	DefaultExtrasChartRepo               = "https://gorizond.github.io/d7y-cattle-operator"
	DefaultExtrasChartName               = "dragonfly-extras"
	DefaultExtrasChartVersion            = "0.1.2"
	DefaultExtrasIngressController       = "nginx"
	DefaultExtrasIngressClassName        = ""
	DefaultExtrasIngressGRPCEnabled      = true
	DefaultExtrasIngressRESTEnabled      = true
	DefaultExtrasIngressTLSSecret        = ""
	DefaultExtrasTraefikTransportEnabled = true
	DefaultNamespace                     = "dragonfly-system"
	DefaultWebhookPort                   = 9443
	DefaultWebhookCertDir                = "/etc/webhook/certs"
	DefaultWebhookServiceName            = "d7y-cattle-operator-webhook"
)

var chartVersionPattern = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)

// Config is the operator configuration derived from flags and environment variables.
type Config struct {
	ManagerGRPCDomain           string
	ManagerAPIURL               string
	NginxStaticURL              string
	MTLSSecretName              string
	ManagerAPICredentialsSecret string
	ManagerPublicTLSSecret      string
	DragonflyChartRepo          string
	DragonflyChartName          string
	DragonflyChartVersion       string
	ExtrasChartRepo             string
	ExtrasChartName             string
	ExtrasChartVersion          string
	ExtrasIngressController     string
	ExtrasIngressClassName      string
	ExtrasIngressGRPCEnabled    bool
	ExtrasIngressRESTEnabled    bool
	ExtrasIngressTLSSecret      string
	ExtrasTraefikTransport      bool
	Namespace                   string
	WebhookPort                 int
	WebhookCertDir              string
	WebhookServiceName          string
	LeaderElect                 bool
	Kubeconfig                  string
}

// Load parses args (without the program name) and validates the result.
// Each flag falls back to its environment variable when the flag is not set.
func Load(args []string) (*Config, error) {
	defaultWebhookPort, err := envInt("WEBHOOK_PORT", DefaultWebhookPort)
	if err != nil {
		return nil, err
	}
	defaultGRPCEnabled, err := envBool("EXTRAS_INGRESS_GRPC_ENABLED", DefaultExtrasIngressGRPCEnabled)
	if err != nil {
		return nil, err
	}
	defaultRESTEnabled, err := envBool("EXTRAS_INGRESS_REST_ENABLED", DefaultExtrasIngressRESTEnabled)
	if err != nil {
		return nil, err
	}
	defaultTraefikTransport, err := envBool("EXTRAS_TRAEFIK_SERVERS_TRANSPORT_ENABLED", DefaultExtrasTraefikTransportEnabled)
	if err != nil {
		return nil, err
	}

	cfg := &Config{}
	fs := flag.NewFlagSet("d7y-cattle-operator", flag.ContinueOnError)
	fs.StringVar(&cfg.ManagerGRPCDomain, "manager-grpc-domain", envOr("MANAGER_GRPC_DOMAIN", ""), "public hostname of the manager gRPC endpoint (no scheme)")
	fs.StringVar(&cfg.ManagerAPIURL, "manager-api-url", envOr("MANAGER_API_URL", ""), "public HTTPS URL of the manager REST API")
	fs.StringVar(&cfg.NginxStaticURL, "nginx-static-url", envOr("NGINX_STATIC_URL", ""), "base URL of the static asset server (http or https)")
	fs.StringVar(&cfg.MTLSSecretName, "mtls-secret-name", envOr("MTLS_SECRET_NAME", DefaultMTLSSecretName), "name of the mTLS Secret")
	fs.StringVar(&cfg.ManagerAPICredentialsSecret, "manager-api-credentials-secret", envOr("MANAGER_API_CREDENTIALS_SECRET", ""), "name of the Secret holding manager API credentials")
	fs.StringVar(&cfg.ManagerPublicTLSSecret, "manager-public-tls-secret", envOr("MANAGER_PUBLIC_TLS_SECRET", DefaultManagerPublicTLSSecret), "name of the Secret holding the manager public TLS certificate (empty keeps the internal mTLS certificate)")
	fs.StringVar(&cfg.DragonflyChartRepo, "dragonfly-chart-repo", envOr("DRAGONFLY_CHART_REPO", ""), "Helm repository URL of the Dragonfly chart")
	fs.StringVar(&cfg.DragonflyChartName, "dragonfly-chart-name", envOr("DRAGONFLY_CHART_NAME", DefaultDragonflyChartName), "Dragonfly chart name")
	fs.StringVar(&cfg.DragonflyChartVersion, "dragonfly-chart-version", envOr("DRAGONFLY_CHART_VERSION", DefaultDragonflyChartVersion), "Dragonfly chart version")
	fs.StringVar(&cfg.ExtrasChartRepo, "extras-chart-repo", envOr("EXTRAS_CHART_REPO", DefaultExtrasChartRepo), "Helm repository URL of the dragonfly-extras chart")
	fs.StringVar(&cfg.ExtrasChartName, "extras-chart-name", envOr("EXTRAS_CHART_NAME", DefaultExtrasChartName), "dragonfly-extras chart name")
	fs.StringVar(&cfg.ExtrasChartVersion, "extras-chart-version", envOr("EXTRAS_CHART_VERSION", DefaultExtrasChartVersion), "dragonfly-extras chart version")
	fs.StringVar(&cfg.ExtrasIngressController, "extras-ingress-controller", envOr("EXTRAS_INGRESS_CONTROLLER", DefaultExtrasIngressController), "extras Ingress controller: nginx or traefik")
	fs.StringVar(&cfg.ExtrasIngressClassName, "extras-ingress-class-name", envOr("EXTRAS_INGRESS_CLASS_NAME", DefaultExtrasIngressClassName), "extras Ingress class name (empty uses the cluster default)")
	fs.BoolVar(&cfg.ExtrasIngressGRPCEnabled, "extras-ingress-grpc-enabled", defaultGRPCEnabled, "render the extras gRPC Ingress")
	fs.BoolVar(&cfg.ExtrasIngressRESTEnabled, "extras-ingress-rest-enabled", defaultRESTEnabled, "render the extras REST Ingress")
	fs.StringVar(&cfg.ExtrasIngressTLSSecret, "extras-ingress-tls-secret", envOr("EXTRAS_INGRESS_TLS_SECRET", DefaultExtrasIngressTLSSecret), "extras Ingress TLS Secret name (empty disables TLS)")
	fs.BoolVar(&cfg.ExtrasTraefikTransport, "extras-traefik-servers-transport-enabled", defaultTraefikTransport, "render the Traefik ServersTransport for the REST Ingress")
	fs.StringVar(&cfg.Namespace, "namespace", envOr("POD_NAMESPACE", DefaultNamespace), "namespace where the operator stores its Secret")
	fs.IntVar(&cfg.WebhookPort, "webhook-port", defaultWebhookPort, "admission webhook port")
	fs.StringVar(&cfg.WebhookCertDir, "webhook-cert-dir", envOr("WEBHOOK_CERT_DIR", DefaultWebhookCertDir), "directory containing the webhook tls.crt and tls.key")
	fs.StringVar(&cfg.WebhookServiceName, "webhook-service-name", envOr("WEBHOOK_SERVICE_NAME", DefaultWebhookServiceName), "webhook Service name used for the serving certificate SANs")
	fs.BoolVar(&cfg.LeaderElect, "leader-elect", false, "enable leader election")
	fs.StringVar(&cfg.Kubeconfig, "kubeconfig", envOr("KUBECONFIG", ""), "path to a kubeconfig (in-cluster config when empty)")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate reports every invalid or missing setting in a single error.
func (c *Config) Validate() error {
	var errs []string
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Sprintf(format, args...))
	}

	if c.ManagerGRPCDomain == "" {
		add("--manager-grpc-domain is required")
	} else if err := validateHost(c.ManagerGRPCDomain); err != nil {
		add("--manager-grpc-domain: %v", err)
	}
	if c.ManagerAPIURL == "" {
		add("--manager-api-url is required")
	} else if err := validateURL(c.ManagerAPIURL, "https"); err != nil {
		add("--manager-api-url: %v", err)
	}
	if c.NginxStaticURL == "" {
		add("--nginx-static-url is required")
	} else if err := validateURL(c.NginxStaticURL, "http", "https"); err != nil {
		add("--nginx-static-url: %v", err)
	}
	if c.MTLSSecretName == "" {
		add("--mtls-secret-name is required")
	} else if msgs := validation.IsDNS1123Subdomain(c.MTLSSecretName); len(msgs) > 0 {
		add("--mtls-secret-name: %s", strings.Join(msgs, "; "))
	}
	if c.ManagerAPICredentialsSecret == "" {
		add("--manager-api-credentials-secret is required")
	} else if msgs := validation.IsDNS1123Subdomain(c.ManagerAPICredentialsSecret); len(msgs) > 0 {
		add("--manager-api-credentials-secret: %s", strings.Join(msgs, "; "))
	}
	if c.ManagerPublicTLSSecret != "" {
		if msgs := validation.IsDNS1123Subdomain(c.ManagerPublicTLSSecret); len(msgs) > 0 {
			add("--manager-public-tls-secret: %s", strings.Join(msgs, "; "))
		}
	}
	if c.DragonflyChartRepo == "" {
		add("--dragonfly-chart-repo is required")
	} else if err := validateURL(c.DragonflyChartRepo, "http", "https"); err != nil {
		add("--dragonfly-chart-repo: %v", err)
	}
	if c.DragonflyChartName == "" {
		add("--dragonfly-chart-name is required")
	} else if msgs := validation.IsDNS1123Label(c.DragonflyChartName); len(msgs) > 0 {
		add("--dragonfly-chart-name: %s", strings.Join(msgs, "; "))
	}
	if !chartVersionPattern.MatchString(c.DragonflyChartVersion) {
		add("--dragonfly-chart-version: %q is not a valid chart version", c.DragonflyChartVersion)
	}
	if c.ExtrasChartRepo == "" {
		add("--extras-chart-repo is required")
	} else if err := validateURL(c.ExtrasChartRepo, "http", "https"); err != nil {
		add("--extras-chart-repo: %v", err)
	}
	if c.ExtrasChartName == "" {
		add("--extras-chart-name is required")
	} else if msgs := validation.IsDNS1123Label(c.ExtrasChartName); len(msgs) > 0 {
		add("--extras-chart-name: %s", strings.Join(msgs, "; "))
	}
	if !chartVersionPattern.MatchString(c.ExtrasChartVersion) {
		add("--extras-chart-version: %q is not a valid chart version", c.ExtrasChartVersion)
	}
	switch c.ExtrasIngressController {
	case "nginx", "traefik":
	default:
		add("--extras-ingress-controller: must be nginx or traefik")
	}
	if c.ExtrasIngressClassName != "" {
		if msgs := validation.IsDNS1123Subdomain(c.ExtrasIngressClassName); len(msgs) > 0 {
			add("--extras-ingress-class-name: %s", strings.Join(msgs, "; "))
		}
	}
	if c.ExtrasIngressTLSSecret != "" {
		if msgs := validation.IsDNS1123Subdomain(c.ExtrasIngressTLSSecret); len(msgs) > 0 {
			add("--extras-ingress-tls-secret: %s", strings.Join(msgs, "; "))
		}
	}
	if c.Namespace == "" {
		add("--namespace is required")
	} else if msgs := validation.IsDNS1123Label(c.Namespace); len(msgs) > 0 {
		add("--namespace: %s", strings.Join(msgs, "; "))
	}
	if c.WebhookPort < 1 || c.WebhookPort > 65535 {
		add("--webhook-port: %d is outside 1-65535", c.WebhookPort)
	}
	if c.WebhookCertDir == "" {
		add("--webhook-cert-dir is required")
	}
	if c.WebhookServiceName == "" {
		add("--webhook-service-name is required")
	} else if msgs := validation.IsDNS1123Label(c.WebhookServiceName); len(msgs) > 0 {
		add("--webhook-service-name: %s", strings.Join(msgs, "; "))
	}

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func validateHost(host string) error {
	if strings.Contains(host, "://") {
		return errors.New("must be a host name without scheme")
	}
	if strings.ContainsAny(host, "/? @") {
		return errors.New("must be a host name without path or spaces")
	}
	if strings.Contains(host, ":") {
		return errors.New("must not include a port")
	}
	return nil
}

func validateURL(raw string, allowedSchemes ...string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return errors.New("must be an absolute URL with scheme and host")
	}
	for _, scheme := range allowedSchemes {
		if u.Scheme == scheme {
			return nil
		}
	}
	return fmt.Errorf("unsupported scheme %q (allowed: %s)", u.Scheme, strings.Join(allowedSchemes, ", "))
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) (bool, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("environment variable %s: %w", key, err)
	}
	return value, nil
}

func envInt(key string, fallback int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("environment variable %s: %w", key, err)
	}
	return value, nil
}

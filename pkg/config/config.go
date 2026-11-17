// Package config parses and validates the operator configuration.
package config

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
)

// Defaults shared with the Helm chart.
const (
	DefaultMTLSSecretName                = "dragonfly-p2p-mtls"
	DefaultManagerGRPCPort               = 443
	DefaultDragonflyChartRepo            = "https://gorizond.github.io/dragonflyoss-helm-charts"
	DefaultDragonflyChartName            = "dragonfly"
	DefaultDragonflyChartVersion         = "1.10.1-patch.2"
	DefaultExtrasChartRepo               = "https://gorizond.github.io/d7y-cattle-operator"
	DefaultExtrasChartName               = "dragonfly-extras"
	DefaultExtrasChartVersion            = "0.1.8"
	DefaultMirrorChartRepo               = "https://gorizond.github.io/d7y-cattle-operator"
	DefaultMirrorChartName               = "containerd-mirror"
	DefaultMirrorChartVersion            = "0.1.0"
	DefaultHarborChartRepo               = "https://helm.goharbor.io"
	DefaultHarborChartName               = "harbor"
	DefaultHarborChartVersion            = "1.19.4"
	DefaultRegistryProxyChartRepo        = "https://gorizond.github.io/d7y-cattle-operator"
	DefaultRegistryProxyChartName        = "registry-proxy"
	DefaultRegistryProxyChartVersion     = "0.1.7"
	DefaultProxyHarborAdminSecret        = "harbor-admin"
	DefaultProxyRegistries               = "docker.io"
	DefaultProxyBasePath                 = "/proxy"
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
	ManagerGRPCPort             int
	ManagerAPIURL               string
	P2PDomain                   string
	ClusterIDFallback           string
	NginxStaticURL              string
	MTLSSecretName              string
	ManagerAPICredentialsSecret string
	DragonflyChartRepo          string
	DragonflyChartName          string
	DragonflyChartVersion       string
	ExtrasChartRepo             string
	ExtrasChartName             string
	ExtrasChartVersion          string
	MirrorChartRepo             string
	MirrorChartName             string
	MirrorChartVersion          string
	HarborChartRepo             string
	HarborChartName             string
	HarborChartVersion          string
	RegistryProxyChartRepo      string
	RegistryProxyChartName      string
	RegistryProxyChartVersion   string
	ProxyDomain                 string
	ProxyHarborURL              string
	ProxyHarborAdminSecret      string
	ProxyRegistries             string
	ProxyBasePath               string
	ProxyTLSSecret              string
	ProxyHostAliases            []helmop.HostAlias
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
	defaultManagerGRPCPort, err := envInt("MANAGER_GRPC_PORT", DefaultManagerGRPCPort)
	if err != nil {
		return nil, err
	}

	cfg := &Config{}
	proxyHostAliases := envOr("PROXY_HOST_ALIASES", "")
	fs := flag.NewFlagSet("d7y-cattle-operator", flag.ContinueOnError)
	fs.StringVar(&cfg.ManagerGRPCDomain, "manager-grpc-domain", envOr("MANAGER_GRPC_DOMAIN", ""), "public hostname of the manager gRPC endpoint (no scheme)")
	fs.IntVar(&cfg.ManagerGRPCPort, "manager-grpc-port", defaultManagerGRPCPort, "public port of the manager gRPC endpoint")
	fs.StringVar(&cfg.ManagerAPIURL, "manager-api-url", envOr("MANAGER_API_URL", ""), "public HTTPS URL of the manager REST API")
	fs.StringVar(&cfg.P2PDomain, "p2p-domain", envOr("P2P_DOMAIN", ""), "base domain for the per-cluster scheduler alias d7y-scheduler.<cluster>.<domain>")
	fs.StringVar(&cfg.ClusterIDFallback, "p2p-cluster-id", envOr("P2P_CLUSTER_ID", ""), "fallback cluster ID when provisioning Cluster status.clusterName is empty")
	fs.StringVar(&cfg.NginxStaticURL, "nginx-static-url", envOr("NGINX_STATIC_URL", ""), "base URL of the static asset server (http or https)")
	fs.StringVar(&cfg.MTLSSecretName, "mtls-secret-name", envOr("MTLS_SECRET_NAME", DefaultMTLSSecretName), "name of the mTLS Secret")
	fs.StringVar(&cfg.ManagerAPICredentialsSecret, "manager-api-credentials-secret", envOr("MANAGER_API_CREDENTIALS_SECRET", ""), "name of the Secret holding manager API credentials")
	fs.StringVar(&cfg.DragonflyChartRepo, "dragonfly-chart-repo", envOr("DRAGONFLY_CHART_REPO", DefaultDragonflyChartRepo), "Helm repository URL of the Dragonfly chart")
	fs.StringVar(&cfg.DragonflyChartName, "dragonfly-chart-name", envOr("DRAGONFLY_CHART_NAME", DefaultDragonflyChartName), "Dragonfly chart name")
	fs.StringVar(&cfg.DragonflyChartVersion, "dragonfly-chart-version", envOr("DRAGONFLY_CHART_VERSION", DefaultDragonflyChartVersion), "Dragonfly chart version")
	fs.StringVar(&cfg.ExtrasChartRepo, "extras-chart-repo", envOr("EXTRAS_CHART_REPO", DefaultExtrasChartRepo), "Helm repository URL of the dragonfly-extras chart")
	fs.StringVar(&cfg.ExtrasChartName, "extras-chart-name", envOr("EXTRAS_CHART_NAME", DefaultExtrasChartName), "dragonfly-extras chart name")
	fs.StringVar(&cfg.ExtrasChartVersion, "extras-chart-version", envOr("EXTRAS_CHART_VERSION", DefaultExtrasChartVersion), "dragonfly-extras chart version")
	fs.StringVar(&cfg.MirrorChartRepo, "containerd-mirror-chart-repo", envOr("CONTAINERD_MIRROR_CHART_REPO", DefaultMirrorChartRepo), "Helm repository URL of the containerd-mirror chart")
	fs.StringVar(&cfg.MirrorChartName, "containerd-mirror-chart-name", envOr("CONTAINERD_MIRROR_CHART_NAME", DefaultMirrorChartName), "containerd-mirror chart name")
	fs.StringVar(&cfg.MirrorChartVersion, "containerd-mirror-chart-version", envOr("CONTAINERD_MIRROR_CHART_VERSION", DefaultMirrorChartVersion), "containerd-mirror chart version")
	fs.StringVar(&cfg.HarborChartRepo, "harbor-chart-repo", envOr("HARBOR_CHART_REPO", DefaultHarborChartRepo), "Helm repository URL of the Harbor chart")
	fs.StringVar(&cfg.HarborChartName, "harbor-chart-name", envOr("HARBOR_CHART_NAME", DefaultHarborChartName), "Harbor chart name")
	fs.StringVar(&cfg.HarborChartVersion, "harbor-chart-version", envOr("HARBOR_CHART_VERSION", DefaultHarborChartVersion), "Harbor chart version")
	fs.StringVar(&cfg.RegistryProxyChartRepo, "registry-proxy-chart-repo", envOr("REGISTRY_PROXY_CHART_REPO", DefaultRegistryProxyChartRepo), "Helm repository URL of the registry-proxy chart")
	fs.StringVar(&cfg.RegistryProxyChartName, "registry-proxy-chart-name", envOr("REGISTRY_PROXY_CHART_NAME", DefaultRegistryProxyChartName), "registry-proxy chart name")
	fs.StringVar(&cfg.RegistryProxyChartVersion, "registry-proxy-chart-version", envOr("REGISTRY_PROXY_CHART_VERSION", DefaultRegistryProxyChartVersion), "registry-proxy chart version")
	fs.StringVar(&cfg.ProxyDomain, "proxy-domain", envOr("PROXY_DOMAIN", ""), "public registry proxy domain (DNS name, no scheme)")
	fs.StringVar(&cfg.ProxyHarborURL, "proxy-harbor-url", envOr("PROXY_HARBOR_URL", ""), "existing/external Harbor base URL for the proxy")
	fs.StringVar(&cfg.ProxyHarborAdminSecret, "proxy-harbor-admin-secret", envOr("PROXY_HARBOR_ADMIN_SECRET", DefaultProxyHarborAdminSecret), "name of the Harbor admin Secret in the operator namespace")
	fs.StringVar(&cfg.ProxyRegistries, "proxy-registries", envOr("PROXY_REGISTRIES", DefaultProxyRegistries), "comma-separated upstream registry allowlist")
	fs.StringVar(&cfg.ProxyBasePath, "proxy-base-path", envOr("PROXY_BASE_PATH", DefaultProxyBasePath), "public proxy base path (default /proxy; empty disables the path prefix)")
	fs.StringVar(&cfg.ProxyTLSSecret, "proxy-tls-secret", envOr("PROXY_TLS_SECRET", ""), "TLS Secret name for the proxy ingress (empty uses the ingress controller default)")
	fs.StringVar(&proxyHostAliases, "proxy-host-aliases", proxyHostAliases, "JSON list of {ip, hostnames} entries added to the registry proxy pod /etc/hosts")
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
	if raw := strings.TrimSpace(proxyHostAliases); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg.ProxyHostAliases); err != nil {
			return nil, fmt.Errorf("--proxy-host-aliases: %w", err)
		}
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
	if c.P2PDomain == "" {
		add("--p2p-domain is required")
	} else if msgs := validation.IsDNS1123Subdomain(c.P2PDomain); len(msgs) > 0 {
		add("--p2p-domain: %s", strings.Join(msgs, "; "))
	}
	if c.ClusterIDFallback != "" {
		if msgs := validation.IsDNS1123Label(c.ClusterIDFallback); len(msgs) > 0 {
			add("--p2p-cluster-id: %s", strings.Join(msgs, "; "))
		}
	}
	if c.NginxStaticURL == "" {
		add("--nginx-static-url is required")
	} else if err := validateURL(c.NginxStaticURL, "http", "https"); err != nil {
		add("--nginx-static-url: %v", err)
	}
	if c.ManagerGRPCPort < 1 || c.ManagerGRPCPort > 65535 {
		add("--manager-grpc-port: %d is outside 1-65535", c.ManagerGRPCPort)
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
	if c.MirrorChartRepo == "" {
		add("--containerd-mirror-chart-repo is required")
	} else if err := validateURL(c.MirrorChartRepo, "http", "https"); err != nil {
		add("--containerd-mirror-chart-repo: %v", err)
	}
	if c.MirrorChartName == "" {
		add("--containerd-mirror-chart-name is required")
	} else if msgs := validation.IsDNS1123Label(c.MirrorChartName); len(msgs) > 0 {
		add("--containerd-mirror-chart-name: %s", strings.Join(msgs, "; "))
	}
	if !chartVersionPattern.MatchString(c.MirrorChartVersion) {
		add("--containerd-mirror-chart-version: %q is not a valid chart version", c.MirrorChartVersion)
	}
	if c.HarborChartRepo == "" {
		add("--harbor-chart-repo is required")
	} else if err := validateURL(c.HarborChartRepo, "http", "https"); err != nil {
		add("--harbor-chart-repo: %v", err)
	}
	if c.HarborChartName == "" {
		add("--harbor-chart-name is required")
	} else if msgs := validation.IsDNS1123Label(c.HarborChartName); len(msgs) > 0 {
		add("--harbor-chart-name: %s", strings.Join(msgs, "; "))
	}
	if !chartVersionPattern.MatchString(c.HarborChartVersion) {
		add("--harbor-chart-version: %q is not a valid chart version", c.HarborChartVersion)
	}
	if c.RegistryProxyChartRepo == "" {
		add("--registry-proxy-chart-repo is required")
	} else if err := validateURL(c.RegistryProxyChartRepo, "http", "https"); err != nil {
		add("--registry-proxy-chart-repo: %v", err)
	}
	if c.RegistryProxyChartName == "" {
		add("--registry-proxy-chart-name is required")
	} else if msgs := validation.IsDNS1123Label(c.RegistryProxyChartName); len(msgs) > 0 {
		add("--registry-proxy-chart-name: %s", strings.Join(msgs, "; "))
	}
	if !chartVersionPattern.MatchString(c.RegistryProxyChartVersion) {
		add("--registry-proxy-chart-version: %q is not a valid chart version", c.RegistryProxyChartVersion)
	}
	if c.ProxyDomain != "" {
		if msgs := validation.IsDNS1123Subdomain(c.ProxyDomain); len(msgs) > 0 {
			add("--proxy-domain: %s", strings.Join(msgs, "; "))
		}
	}
	if c.ProxyHarborURL != "" {
		if err := validateURL(c.ProxyHarborURL, "https"); err != nil {
			add("--proxy-harbor-url: %v", err)
		}
	}
	if c.ProxyHarborAdminSecret == "" {
		add("--proxy-harbor-admin-secret is required")
	} else if msgs := validation.IsDNS1123Subdomain(c.ProxyHarborAdminSecret); len(msgs) > 0 {
		add("--proxy-harbor-admin-secret: %s", strings.Join(msgs, "; "))
	}
	if c.ProxyBasePath != "" {
		if !strings.HasPrefix(c.ProxyBasePath, "/") || strings.ContainsAny(c.ProxyBasePath, "?#") ||
			strings.Contains(c.ProxyBasePath, "..") || strings.HasSuffix(c.ProxyBasePath, "/") && c.ProxyBasePath != "/" {
			add("--proxy-base-path: must start with /, have no query, and no trailing slash")
		}
	}
	if c.ProxyTLSSecret != "" {
		if msgs := validation.IsDNS1123Subdomain(c.ProxyTLSSecret); len(msgs) > 0 {
			add("--proxy-tls-secret: %s", strings.Join(msgs, "; "))
		}
	}
	for i, alias := range c.ProxyHostAliases {
		if net.ParseIP(alias.IP) == nil {
			add("--proxy-host-aliases[%d].ip: %q is not a valid IP address", i, alias.IP)
		}
		if len(alias.Hostnames) == 0 {
			add("--proxy-host-aliases[%d].hostnames: at least one hostname is required", i)
		}
		for _, hostname := range alias.Hostnames {
			if msgs := validation.IsDNS1123Subdomain(hostname); len(msgs) > 0 {
				add("--proxy-host-aliases[%d].hostnames: %s: %s", i, hostname, strings.Join(msgs, "; "))
			}
		}
	}
	for _, registry := range strings.Split(c.ProxyRegistries, ",") {
		registry = strings.TrimSpace(registry)
		if registry == "" {
			add("--proxy-registries: contains an empty registry")
			continue
		}
		if msgs := validation.IsDNS1123Subdomain(registry); len(msgs) > 0 {
			add("--proxy-registries: %s: %s", registry, strings.Join(msgs, "; "))
		}
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

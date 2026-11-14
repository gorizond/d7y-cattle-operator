package config

import (
	"strings"
	"testing"
)

var envKeys = []string{
	"MANAGER_GRPC_DOMAIN",
	"MANAGER_API_URL",
	"NGINX_STATIC_URL",
	"MTLS_SECRET_NAME",
	"MANAGER_API_CREDENTIALS_SECRET",
	"MANAGER_PUBLIC_TLS_SECRET",
	"DRAGONFLY_CHART_REPO",
	"DRAGONFLY_CHART_NAME",
	"DRAGONFLY_CHART_VERSION",
	"EXTRAS_CHART_REPO",
	"EXTRAS_CHART_NAME",
	"EXTRAS_CHART_VERSION",
	"EXTRAS_INGRESS_CONTROLLER",
	"EXTRAS_INGRESS_CLASS_NAME",
	"EXTRAS_INGRESS_GRPC_ENABLED",
	"EXTRAS_INGRESS_REST_ENABLED",
	"EXTRAS_INGRESS_TLS_SECRET",
	"EXTRAS_TRAEFIK_SERVERS_TRANSPORT_ENABLED",
	"POD_NAMESPACE",
	"WEBHOOK_PORT",
	"WEBHOOK_CERT_DIR",
	"WEBHOOK_SERVICE_NAME",
	"KUBECONFIG",
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, key := range envKeys {
		t.Setenv(key, "")
	}
}

func requiredArgs() []string {
	return []string{
		"--manager-grpc-domain=manager-grpc.example.com",
		"--manager-api-url=https://manager-api.example.com",
		"--nginx-static-url=https://static.example.com",
		"--manager-api-credentials-secret=dragonfly-manager-api",
		"--dragonfly-chart-repo=https://dragonflyoss.github.io/helm-charts",
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)

	cfg, err := Load(requiredArgs())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.MTLSSecretName != DefaultMTLSSecretName {
		t.Errorf("MTLSSecretName = %q, want %q", cfg.MTLSSecretName, DefaultMTLSSecretName)
	}
	if cfg.DragonflyChartName != DefaultDragonflyChartName {
		t.Errorf("DragonflyChartName = %q, want %q", cfg.DragonflyChartName, DefaultDragonflyChartName)
	}
	if cfg.DragonflyChartVersion != DefaultDragonflyChartVersion {
		t.Errorf("DragonflyChartVersion = %q, want %q", cfg.DragonflyChartVersion, DefaultDragonflyChartVersion)
	}
	if cfg.ExtrasChartRepo != DefaultExtrasChartRepo {
		t.Errorf("ExtrasChartRepo = %q, want %q", cfg.ExtrasChartRepo, DefaultExtrasChartRepo)
	}
	if cfg.ExtrasChartName != DefaultExtrasChartName {
		t.Errorf("ExtrasChartName = %q, want %q", cfg.ExtrasChartName, DefaultExtrasChartName)
	}
	if cfg.ExtrasChartVersion != DefaultExtrasChartVersion {
		t.Errorf("ExtrasChartVersion = %q, want %q", cfg.ExtrasChartVersion, DefaultExtrasChartVersion)
	}
	if cfg.ExtrasIngressController != DefaultExtrasIngressController {
		t.Errorf("ExtrasIngressController = %q, want %q", cfg.ExtrasIngressController, DefaultExtrasIngressController)
	}
	if cfg.ExtrasIngressClassName != "" {
		t.Errorf("ExtrasIngressClassName = %q, want empty", cfg.ExtrasIngressClassName)
	}
	if !cfg.ExtrasIngressGRPCEnabled || !cfg.ExtrasIngressRESTEnabled {
		t.Errorf("extras ingress toggles = grpc:%v rest:%v, want both true", cfg.ExtrasIngressGRPCEnabled, cfg.ExtrasIngressRESTEnabled)
	}
	if cfg.ExtrasIngressTLSSecret != "" {
		t.Errorf("ExtrasIngressTLSSecret = %q, want empty", cfg.ExtrasIngressTLSSecret)
	}
	if !cfg.ExtrasTraefikTransport {
		t.Error("ExtrasTraefikTransport = false, want true")
	}
	if cfg.ManagerPublicTLSSecret != DefaultManagerPublicTLSSecret {
		t.Errorf("ManagerPublicTLSSecret = %q, want %q", cfg.ManagerPublicTLSSecret, DefaultManagerPublicTLSSecret)
	}
	if cfg.Namespace != DefaultNamespace {
		t.Errorf("Namespace = %q, want %q", cfg.Namespace, DefaultNamespace)
	}
	if cfg.WebhookPort != DefaultWebhookPort {
		t.Errorf("WebhookPort = %d, want %d", cfg.WebhookPort, DefaultWebhookPort)
	}
	if cfg.WebhookCertDir != DefaultWebhookCertDir {
		t.Errorf("WebhookCertDir = %q, want %q", cfg.WebhookCertDir, DefaultWebhookCertDir)
	}
	if cfg.WebhookServiceName != DefaultWebhookServiceName {
		t.Errorf("WebhookServiceName = %q, want %q", cfg.WebhookServiceName, DefaultWebhookServiceName)
	}
	if cfg.LeaderElect {
		t.Error("LeaderElect = true, want false")
	}
	if cfg.Kubeconfig != "" {
		t.Errorf("Kubeconfig = %q, want empty", cfg.Kubeconfig)
	}
}

func TestLoadRequiredFlags(t *testing.T) {
	for _, required := range []string{
		"--manager-grpc-domain",
		"--manager-api-url",
		"--nginx-static-url",
		"--manager-api-credentials-secret",
		"--dragonfly-chart-repo",
	} {
		t.Run(required, func(t *testing.T) {
			clearEnv(t)

			var args []string
			for _, arg := range requiredArgs() {
				if !strings.HasPrefix(arg, required+"=") {
					args = append(args, arg)
				}
			}

			_, err := Load(args)
			if err == nil {
				t.Fatalf("Load() error = nil, want an error for missing %s", required)
			}
			if !strings.Contains(err.Error(), required) {
				t.Errorf("error %q does not mention %s", err, required)
			}
		})
	}
}

func TestLoadInvalidValues(t *testing.T) {
	tests := map[string]string{
		"manager API without https":         "--manager-api-url=http://manager-api.example.com",
		"manager API with invalid URL":      "--manager-api-url=not-a-url",
		"nginx static with unsupported URL": "--nginx-static-url=ftp://static.example.com",
		"gRPC domain with scheme":           "--manager-grpc-domain=https://manager-grpc.example.com",
		"gRPC domain with port":             "--manager-grpc-domain=manager-grpc.example.com:443",
		"invalid Secret name":               "--mtls-secret-name=Bad_Name",
		"invalid namespace":                 "--namespace=Bad_Name",
		"invalid chart version":             "--dragonfly-chart-version=latest",
		"invalid extras chart version":      "--extras-chart-version=latest",
		"invalid extras chart repo":         "--extras-chart-repo=not-a-url",
		"invalid extras ingress controller": "--extras-ingress-controller=haproxy",
		"invalid extras ingress class":      "--extras-ingress-class-name=Bad_Name",
		"invalid extras ingress TLS Secret": "--extras-ingress-tls-secret=Bad_Name",
		"invalid manager public TLS Secret": "--manager-public-tls-secret=Bad_Name",
		"webhook port zero":                 "--webhook-port=0",
		"webhook port too large":            "--webhook-port=70000",
		"empty webhook cert dir":            "--webhook-cert-dir=",
		"invalid webhook service name":      "--webhook-service-name=Bad_Name",
	}

	for name, override := range tests {
		t.Run(name, func(t *testing.T) {
			clearEnv(t)

			args := append(requiredArgs(), override)
			if _, err := Load(args); err == nil {
				t.Fatalf("Load(%q) error = nil, want an error", override)
			}
		})
	}
}

func TestLoadEnvOverride(t *testing.T) {
	clearEnv(t)
	t.Setenv("MANAGER_GRPC_DOMAIN", "grpc.env.example.com")
	t.Setenv("MANAGER_API_URL", "https://api.env.example.com")
	t.Setenv("NGINX_STATIC_URL", "http://static.env.example.com")
	t.Setenv("MTLS_SECRET_NAME", "env-mtls")
	t.Setenv("MANAGER_API_CREDENTIALS_SECRET", "env-credentials")
	t.Setenv("MANAGER_PUBLIC_TLS_SECRET", "env-public-tls")
	t.Setenv("DRAGONFLY_CHART_REPO", "https://charts.env.example.com")
	t.Setenv("DRAGONFLY_CHART_NAME", "dragonfly-env")
	t.Setenv("DRAGONFLY_CHART_VERSION", "2.0.0")
	t.Setenv("EXTRAS_CHART_REPO", "https://extras.env.example.com")
	t.Setenv("EXTRAS_CHART_NAME", "dragonfly-extras-env")
	t.Setenv("EXTRAS_CHART_VERSION", "1.2.3")
	t.Setenv("EXTRAS_INGRESS_CONTROLLER", "traefik")
	t.Setenv("EXTRAS_INGRESS_CLASS_NAME", "traefik")
	t.Setenv("EXTRAS_INGRESS_GRPC_ENABLED", "false")
	t.Setenv("EXTRAS_INGRESS_REST_ENABLED", "true")
	t.Setenv("EXTRAS_INGRESS_TLS_SECRET", "dragonfly-p2p-mtls")
	t.Setenv("EXTRAS_TRAEFIK_SERVERS_TRANSPORT_ENABLED", "false")
	t.Setenv("POD_NAMESPACE", "env-namespace")
	t.Setenv("WEBHOOK_PORT", "10443")
	t.Setenv("WEBHOOK_CERT_DIR", "/tmp/webhook-certs")
	t.Setenv("WEBHOOK_SERVICE_NAME", "env-webhook")
	t.Setenv("KUBECONFIG", "/tmp/kubeconfig")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.ManagerGRPCDomain != "grpc.env.example.com" {
		t.Errorf("ManagerGRPCDomain = %q", cfg.ManagerGRPCDomain)
	}
	if cfg.ManagerAPIURL != "https://api.env.example.com" {
		t.Errorf("ManagerAPIURL = %q", cfg.ManagerAPIURL)
	}
	if cfg.NginxStaticURL != "http://static.env.example.com" {
		t.Errorf("NginxStaticURL = %q", cfg.NginxStaticURL)
	}
	if cfg.MTLSSecretName != "env-mtls" {
		t.Errorf("MTLSSecretName = %q", cfg.MTLSSecretName)
	}
	if cfg.ManagerAPICredentialsSecret != "env-credentials" {
		t.Errorf("ManagerAPICredentialsSecret = %q", cfg.ManagerAPICredentialsSecret)
	}
	if cfg.ManagerPublicTLSSecret != "env-public-tls" {
		t.Errorf("ManagerPublicTLSSecret = %q", cfg.ManagerPublicTLSSecret)
	}
	if cfg.DragonflyChartRepo != "https://charts.env.example.com" {
		t.Errorf("DragonflyChartRepo = %q", cfg.DragonflyChartRepo)
	}
	if cfg.DragonflyChartName != "dragonfly-env" {
		t.Errorf("DragonflyChartName = %q", cfg.DragonflyChartName)
	}
	if cfg.DragonflyChartVersion != "2.0.0" {
		t.Errorf("DragonflyChartVersion = %q", cfg.DragonflyChartVersion)
	}
	if cfg.ExtrasChartRepo != "https://extras.env.example.com" {
		t.Errorf("ExtrasChartRepo = %q", cfg.ExtrasChartRepo)
	}
	if cfg.ExtrasChartName != "dragonfly-extras-env" {
		t.Errorf("ExtrasChartName = %q", cfg.ExtrasChartName)
	}
	if cfg.ExtrasChartVersion != "1.2.3" {
		t.Errorf("ExtrasChartVersion = %q", cfg.ExtrasChartVersion)
	}
	if cfg.ExtrasIngressController != "traefik" {
		t.Errorf("ExtrasIngressController = %q", cfg.ExtrasIngressController)
	}
	if cfg.ExtrasIngressClassName != "traefik" {
		t.Errorf("ExtrasIngressClassName = %q", cfg.ExtrasIngressClassName)
	}
	if cfg.ExtrasIngressGRPCEnabled {
		t.Error("ExtrasIngressGRPCEnabled = true, want false")
	}
	if !cfg.ExtrasIngressRESTEnabled {
		t.Error("ExtrasIngressRESTEnabled = false, want true")
	}
	if cfg.ExtrasIngressTLSSecret != "dragonfly-p2p-mtls" {
		t.Errorf("ExtrasIngressTLSSecret = %q", cfg.ExtrasIngressTLSSecret)
	}
	if cfg.ExtrasTraefikTransport {
		t.Error("ExtrasTraefikTransport = true, want false")
	}
	if cfg.Namespace != "env-namespace" {
		t.Errorf("Namespace = %q", cfg.Namespace)
	}
	if cfg.WebhookPort != 10443 {
		t.Errorf("WebhookPort = %d", cfg.WebhookPort)
	}
	if cfg.WebhookCertDir != "/tmp/webhook-certs" {
		t.Errorf("WebhookCertDir = %q", cfg.WebhookCertDir)
	}
	if cfg.WebhookServiceName != "env-webhook" {
		t.Errorf("WebhookServiceName = %q", cfg.WebhookServiceName)
	}
	if cfg.Kubeconfig != "/tmp/kubeconfig" {
		t.Errorf("Kubeconfig = %q", cfg.Kubeconfig)
	}
}

func TestLoadFlagOverridesEnv(t *testing.T) {
	clearEnv(t)
	t.Setenv("MANAGER_GRPC_DOMAIN", "grpc.env.example.com")

	args := append(requiredArgs(),
		"--manager-grpc-domain=grpc.flag.example.com",
		"--manager-public-tls-secret=flag-public-tls",
	)
	cfg, err := Load(args)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ManagerGRPCDomain != "grpc.flag.example.com" {
		t.Errorf("ManagerGRPCDomain = %q, want flag value", cfg.ManagerGRPCDomain)
	}
	if cfg.ManagerPublicTLSSecret != "flag-public-tls" {
		t.Errorf("ManagerPublicTLSSecret = %q, want flag value", cfg.ManagerPublicTLSSecret)
	}
}

func TestLoadInvalidEnvPort(t *testing.T) {
	clearEnv(t)
	t.Setenv("MANAGER_GRPC_DOMAIN", "manager-grpc.example.com")
	t.Setenv("MANAGER_API_URL", "https://manager-api.example.com")
	t.Setenv("NGINX_STATIC_URL", "https://static.example.com")
	t.Setenv("MANAGER_API_CREDENTIALS_SECRET", "dragonfly-manager-api")
	t.Setenv("DRAGONFLY_CHART_REPO", "https://dragonflyoss.github.io/helm-charts")
	t.Setenv("WEBHOOK_PORT", "not-a-number")

	_, err := Load(nil)
	if err == nil || !strings.Contains(err.Error(), "WEBHOOK_PORT") {
		t.Fatalf("Load() error = %v, want an error mentioning WEBHOOK_PORT", err)
	}
}

func TestLoadInvalidExtrasIngressBoolEnv(t *testing.T) {
	clearEnv(t)
	t.Setenv("EXTRAS_INGRESS_GRPC_ENABLED", "not-a-bool")

	_, err := Load(requiredArgs())
	if err == nil || !strings.Contains(err.Error(), "EXTRAS_INGRESS_GRPC_ENABLED") {
		t.Fatalf("Load() error = %v, want an error mentioning EXTRAS_INGRESS_GRPC_ENABLED", err)
	}
}

package config

import (
	"strings"
	"testing"
)

var envKeys = []string{
	"MANAGER_GRPC_DOMAIN",
	"MANAGER_GRPC_PORT",
	"CONTAINERD_MIRROR_PORT",
	"MANAGER_API_URL",
	"P2P_CLUSTER_ID",
	"MTLS_SECRET_NAME",
	"MANAGER_API_CREDENTIALS_SECRET",
	"DRAGONFLY_CHART_REPO",
	"DRAGONFLY_CHART_NAME",
	"DRAGONFLY_CHART_VERSION",
	"EXTRAS_CHART_REPO",
	"EXTRAS_CHART_NAME",
	"EXTRAS_CHART_VERSION",
	"CONTAINERD_MIRROR_CHART_REPO",
	"CONTAINERD_MIRROR_CHART_NAME",
	"CONTAINERD_MIRROR_CHART_VERSION",
	"SCHEDULER_CERT_CHART_REPO",
	"SCHEDULER_CERT_CHART_NAME",
	"SCHEDULER_CERT_CHART_VERSION",
	"EXTRAS_INGRESS_CONTROLLER",
	"EXTRAS_INGRESS_CLASS_NAME",
	"EXTRAS_INGRESS_GRPC_ENABLED",
	"EXTRAS_INGRESS_REST_ENABLED",
	"EXTRAS_INGRESS_TLS_SECRET",
	"EXTRAS_TRAEFIK_SERVERS_TRANSPORT_ENABLED",
	"POD_NAMESPACE",
	"NGINX_STATIC_URL",
	"NYDUS_MIRROR_URL",
	"CLIENT_VERSION",
	"NYDUS_VERSION",
	"NYDUS_SNAPSHOTTER_VERSION",
	"STATIC_CHART_REPO",
	"STATIC_CHART_NAME",
	"STATIC_CHART_VERSION",
	"STATIC_RELEASE_NAME",
	"CLIENT_RELEASE_REPO",
	"NYDUS_RELEASE_REPO",
	"NYDUS_SNAPSHOTTER_RELEASE_REPO",
	"CLIENT_SHA256_AMD64",
	"CLIENT_SHA256_ARM64",
	"NYDUS_SHA256_AMD64",
	"NYDUS_SHA256_ARM64",
	"NYDUS_SNAPSHOTTER_SHA256_AMD64",
	"NYDUS_SNAPSHOTTER_SHA256_ARM64",
	"NYDUS_SNAPSHOTTER_BINARY_SHA256_AMD64",
	"NYDUS_SNAPSHOTTER_BINARY_SHA256_ARM64",
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
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)

	cfg, err := Load(requiredArgs())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.ManagerGRPCPort != DefaultManagerGRPCPort {
		t.Errorf("ManagerGRPCPort = %d, want %d", cfg.ManagerGRPCPort, DefaultManagerGRPCPort)
	}
	if cfg.ContainerdMirrorPort != DefaultContainerdMirrorPort {
		t.Errorf("ContainerdMirrorPort = %d, want %d", cfg.ContainerdMirrorPort, DefaultContainerdMirrorPort)
	}
	if cfg.ClusterIDFallback != "" {
		t.Errorf("ClusterIDFallback = %q, want empty", cfg.ClusterIDFallback)
	}
	if cfg.MTLSSecretName != DefaultMTLSSecretName {
		t.Errorf("MTLSSecretName = %q, want %q", cfg.MTLSSecretName, DefaultMTLSSecretName)
	}
	if cfg.ClientVersion != DefaultClientVersion {
		t.Errorf("ClientVersion = %q, want %q", cfg.ClientVersion, DefaultClientVersion)
	}
	if cfg.NydusVersion != DefaultNydusVersion {
		t.Errorf("NydusVersion = %q, want %q", cfg.NydusVersion, DefaultNydusVersion)
	}
	if cfg.SnapshotterVersion != DefaultSnapshotterVersion {
		t.Errorf("SnapshotterVersion = %q, want %q", cfg.SnapshotterVersion, DefaultSnapshotterVersion)
	}
	if cfg.StaticChartName != DefaultStaticChartName || cfg.StaticReleaseName != DefaultStaticReleaseName {
		t.Errorf("static chart/release = %q/%q", cfg.StaticChartName, cfg.StaticReleaseName)
	}
	if cfg.SchedulerCertChartName != DefaultSchedulerCertChartName || cfg.SchedulerCertChartVersion != DefaultSchedulerCertChartVersion {
		t.Errorf("scheduler cert chart = %q@%q", cfg.SchedulerCertChartName, cfg.SchedulerCertChartVersion)
	}
	if cfg.ClientSHA256AMD64 != DefaultClientSHA256AMD64 || cfg.SnapshotterSHA256ARM64 != DefaultSnapshotterSHA256ARM64 {
		t.Errorf("checksum defaults = %q/%q", cfg.ClientSHA256AMD64, cfg.SnapshotterSHA256ARM64)
	}
	if cfg.SnapshotterBinarySHA256AMD64 != "9bd24308ef51139310bf58927a97eec49567d0154606bc5774ad69d2ee492ff0" ||
		cfg.SnapshotterBinarySHA256ARM64 != "399f7a5b800bb6d05c63bbb340e7fa1820b84609b882589a6a95dd8796d03859" {
		t.Errorf("snapshotter binary defaults = %q/%q", cfg.SnapshotterBinarySHA256AMD64, cfg.SnapshotterBinarySHA256ARM64)
	}
	if cfg.DragonflyChartRepo != DefaultDragonflyChartRepo {
		t.Errorf("DragonflyChartRepo = %q, want %q", cfg.DragonflyChartRepo, DefaultDragonflyChartRepo)
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
	if cfg.MirrorChartRepo != DefaultMirrorChartRepo {
		t.Errorf("MirrorChartRepo = %q, want %q", cfg.MirrorChartRepo, DefaultMirrorChartRepo)
	}
	if cfg.MirrorChartName != DefaultMirrorChartName {
		t.Errorf("MirrorChartName = %q, want %q", cfg.MirrorChartName, DefaultMirrorChartName)
	}
	if cfg.MirrorChartVersion != DefaultMirrorChartVersion {
		t.Errorf("MirrorChartVersion = %q, want %q", cfg.MirrorChartVersion, DefaultMirrorChartVersion)
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
	if cfg.Namespace != DefaultNamespace {
		t.Errorf("Namespace = %q, want %q", cfg.Namespace, DefaultNamespace)
	}
	if cfg.LeaderElect {
		t.Error("LeaderElect = true, want false")
	}
	if cfg.Kubeconfig != "" {
		t.Errorf("Kubeconfig = %q, want empty", cfg.Kubeconfig)
	}
}

func TestForkChartDefaults(t *testing.T) {
	if DefaultDragonflyChartRepo != "https://gorizond.github.io/dragonflyoss-helm-charts" {
		t.Errorf("DefaultDragonflyChartRepo = %q", DefaultDragonflyChartRepo)
	}
	if DefaultDragonflyChartVersion != "1.10.1-patch.3" {
		t.Errorf("DefaultDragonflyChartVersion = %q", DefaultDragonflyChartVersion)
	}
	if DefaultExtrasChartVersion != "0.1.12" {
		t.Errorf("DefaultExtrasChartVersion = %q", DefaultExtrasChartVersion)
	}
	if DefaultMirrorChartVersion != "0.1.0" {
		t.Errorf("DefaultMirrorChartVersion = %q", DefaultMirrorChartVersion)
	}
}

func TestLoadRequiredFlags(t *testing.T) {
	for _, required := range []string{
		"--manager-grpc-domain",
		"--manager-api-url",
		"--manager-api-credentials-secret",
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
		"gRPC domain with scheme":           "--manager-grpc-domain=https://manager-grpc.example.com",
		"gRPC domain with port":             "--manager-grpc-domain=manager-grpc.example.com:443",
		"invalid Secret name":               "--mtls-secret-name=Bad_Name",
		"manager gRPC port zero":            "--manager-grpc-port=0",
		"manager gRPC port too large":       "--manager-grpc-port=70000",
		"invalid namespace":                 "--namespace=Bad_Name",
		"invalid chart version":             "--dragonfly-chart-version=latest",
		"invalid dragonfly chart repo":      "--dragonfly-chart-repo=not-a-url",
		"invalid extras chart version":      "--extras-chart-version=latest",
		"invalid extras chart repo":         "--extras-chart-repo=not-a-url",
		"invalid extras ingress controller": "--extras-ingress-controller=haproxy",
		"invalid extras ingress class":      "--extras-ingress-class-name=Bad_Name",
		"invalid extras ingress TLS Secret": "--extras-ingress-tls-secret=Bad_Name",
		"invalid p2p cluster id":            "--p2p-cluster-id=Bad_Cluster",
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
	t.Setenv("MANAGER_GRPC_PORT", "8443")
	t.Setenv("MANAGER_API_URL", "https://api.env.example.com")
	t.Setenv("P2P_CLUSTER_ID", "c-m-env")
	t.Setenv("MTLS_SECRET_NAME", "env-mtls")
	t.Setenv("MANAGER_API_CREDENTIALS_SECRET", "env-credentials")
	t.Setenv("DRAGONFLY_CHART_REPO", "https://charts.env.example.com")
	t.Setenv("DRAGONFLY_CHART_NAME", "dragonfly-env")
	t.Setenv("DRAGONFLY_CHART_VERSION", "2.0.0")
	t.Setenv("EXTRAS_CHART_REPO", "https://extras.env.example.com")
	t.Setenv("EXTRAS_CHART_NAME", "dragonfly-extras-env")
	t.Setenv("EXTRAS_CHART_VERSION", "1.2.3")
	t.Setenv("CONTAINERD_MIRROR_CHART_REPO", "https://mirror.env.example.com")
	t.Setenv("CONTAINERD_MIRROR_CHART_NAME", "containerd-mirror-env")
	t.Setenv("CONTAINERD_MIRROR_CHART_VERSION", "2.3.4")
	t.Setenv("CONTAINERD_MIRROR_PORT", "4101")
	t.Setenv("EXTRAS_INGRESS_CONTROLLER", "traefik")
	t.Setenv("EXTRAS_INGRESS_CLASS_NAME", "traefik")
	t.Setenv("EXTRAS_INGRESS_GRPC_ENABLED", "false")
	t.Setenv("EXTRAS_INGRESS_REST_ENABLED", "true")
	t.Setenv("EXTRAS_INGRESS_TLS_SECRET", "dragonfly-p2p-mtls")
	t.Setenv("EXTRAS_TRAEFIK_SERVERS_TRANSPORT_ENABLED", "false")
	t.Setenv("POD_NAMESPACE", "env-namespace")
	t.Setenv("NGINX_STATIC_URL", "http://static.env.example.com")
	t.Setenv("CLIENT_VERSION", "v1.5.15")
	t.Setenv("NYDUS_VERSION", "v2.4.6")
	t.Setenv("NYDUS_SNAPSHOTTER_VERSION", "v0.16.1")
	t.Setenv("KUBECONFIG", "/tmp/kubeconfig")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.ManagerGRPCDomain != "grpc.env.example.com" {
		t.Errorf("ManagerGRPCDomain = %q", cfg.ManagerGRPCDomain)
	}
	if cfg.ManagerGRPCPort != 8443 {
		t.Errorf("ManagerGRPCPort = %d", cfg.ManagerGRPCPort)
	}
	if cfg.ContainerdMirrorPort != 4101 {
		t.Errorf("ContainerdMirrorPort = %d, want 4101", cfg.ContainerdMirrorPort)
	}
	if cfg.ManagerAPIURL != "https://api.env.example.com" {
		t.Errorf("ManagerAPIURL = %q", cfg.ManagerAPIURL)
	}
	if cfg.ClusterIDFallback != "c-m-env" {
		t.Errorf("ClusterIDFallback = %q", cfg.ClusterIDFallback)
	}

	if cfg.MTLSSecretName != "env-mtls" {
		t.Errorf("MTLSSecretName = %q", cfg.MTLSSecretName)
	}
	if cfg.ManagerAPICredentialsSecret != "env-credentials" {
		t.Errorf("ManagerAPICredentialsSecret = %q", cfg.ManagerAPICredentialsSecret)
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
	if cfg.MirrorChartRepo != "https://mirror.env.example.com" {
		t.Errorf("MirrorChartRepo = %q", cfg.MirrorChartRepo)
	}
	if cfg.MirrorChartName != "containerd-mirror-env" {
		t.Errorf("MirrorChartName = %q", cfg.MirrorChartName)
	}
	if cfg.MirrorChartVersion != "2.3.4" {
		t.Errorf("MirrorChartVersion = %q", cfg.MirrorChartVersion)
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
	if cfg.ClientVersion != "v1.5.15" || cfg.NydusVersion != "v2.4.6" || cfg.SnapshotterVersion != "v0.16.1" {
		t.Errorf("versions = %q/%q/%q", cfg.ClientVersion, cfg.NydusVersion, cfg.SnapshotterVersion)
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
		"--manager-grpc-port=9443",
	)
	cfg, err := Load(args)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ManagerGRPCDomain != "grpc.flag.example.com" {
		t.Errorf("ManagerGRPCDomain = %q, want flag value", cfg.ManagerGRPCDomain)
	}
	if cfg.ManagerGRPCPort != 9443 {
		t.Errorf("ManagerGRPCPort = %d, want flag value", cfg.ManagerGRPCPort)
	}
}

func TestLoadClusterIDFallbackFlag(t *testing.T) {
	clearEnv(t)
	args := append(requiredArgs(), "--p2p-cluster-id=c-m-fallback")
	cfg, err := Load(args)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ClusterIDFallback != "c-m-fallback" {
		t.Errorf("ClusterIDFallback = %q", cfg.ClusterIDFallback)
	}
}

func TestLoadExtrasIngressAuto(t *testing.T) {
	clearEnv(t)
	cfg, err := Load(append(requiredArgs(), "--extras-ingress-controller=auto"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ExtrasIngressController != "auto" {
		t.Errorf("ExtrasIngressController = %q, want auto", cfg.ExtrasIngressController)
	}
	if cfg.ExtrasIngressClassName != "" {
		t.Errorf("ExtrasIngressClassName = %q, want empty", cfg.ExtrasIngressClassName)
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

func TestLoadInvalidManagerGRPCPortEnv(t *testing.T) {
	clearEnv(t)
	t.Setenv("MANAGER_GRPC_PORT", "not-a-number")

	_, err := Load(requiredArgs())
	if err == nil || !strings.Contains(err.Error(), "MANAGER_GRPC_PORT") {
		t.Fatalf("Load() error = %v, want an error mentioning MANAGER_GRPC_PORT", err)
	}
}

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
	DefaultManagerGRPCPort               = 443
	DefaultContainerdMirrorPort          = 4001
	DefaultDragonflyChartRepo            = "https://gorizond.github.io/dragonflyoss-helm-charts"
	DefaultDragonflyChartName            = "dragonfly"
	DefaultDragonflyChartVersion         = "1.10.1-patch.2"
	DefaultExtrasChartRepo               = "https://gorizond.github.io/d7y-cattle-operator"
	DefaultExtrasChartName               = "dragonfly-extras"
	DefaultExtrasChartVersion            = "0.1.12"
	DefaultMirrorChartRepo               = "https://gorizond.github.io/d7y-cattle-operator"
	DefaultMirrorChartName               = "containerd-mirror"
	DefaultMirrorChartVersion            = "0.1.0"
	DefaultHostSyncChartRepo             = "https://gorizond.github.io/d7y-cattle-operator"
	DefaultHostSyncChartName             = "p2p-host-sync"
	DefaultHostSyncChartVersion          = "0.1.5"
	DefaultSchedulerCertChartRepo        = "https://gorizond.github.io/d7y-cattle-operator"
	DefaultSchedulerCertChartName        = "p2p-scheduler-cert"
	DefaultSchedulerCertChartVersion     = "0.1.0"
	DefaultStaticChartRepo               = "https://gorizond.github.io/d7y-cattle-operator"
	DefaultStaticChartName               = "p2p-static"
	DefaultStaticChartVersion            = "0.1.14"
	DefaultStaticReleaseName             = "p2p-static"
	DefaultClientReleaseRepo             = "dragonflyoss/client"
	DefaultNydusReleaseRepo              = "dragonflyoss/nydus"
	DefaultSnapshotterReleaseRepo        = "containerd/nydus-snapshotter"
	DefaultClientSHA256AMD64             = "ba080e62f1626ab2fc2a5a0629a5cd912cc15099508820403108abbe2f75b1f7"
	DefaultClientSHA256ARM64             = "370ff792a1c6957a7244b770d6cd12792668e326b2282b3017fea2cbfa3510af"
	DefaultNydusSHA256AMD64              = "1ad7b793a1996577e462b0bf5c1a22e4450b9c04f7e9e99d4b274ccc92f19072"
	DefaultNydusSHA256ARM64              = "85925af702d79fc3a8b9d501a5267ddc9f81449ff9fe94841845eb98eda84593"
	DefaultSnapshotterSHA256AMD64        = "24e4ceaae9c87e396e71e89bd3eb074f2cc6a5a14b1a2081c8d890481857a0e4"
	DefaultSnapshotterSHA256ARM64        = "136e9c649a63c16b3d3322a15fcdf918e7dda9a4cbb043e2923451e87a8ba08f"
	DefaultSnapshotterBinarySHA256AMD64  = "9bd24308ef51139310bf58927a97eec49567d0154606bc5774ad69d2ee492ff0"
	DefaultSnapshotterBinarySHA256ARM64  = "399f7a5b800bb6d05c63bbb340e7fa1820b84609b882589a6a95dd8796d03859"
	DefaultClientVersion                 = "v1.5.14"
	DefaultNydusVersion                  = "v2.4.5"
	DefaultSnapshotterVersion            = "v0.16.0"
	DefaultExtrasIngressController       = "auto"
	DefaultExtrasIngressClassName        = ""
	DefaultExtrasIngressGRPCEnabled      = true
	DefaultExtrasIngressRESTEnabled      = true
	DefaultExtrasIngressTLSSecret        = ""
	DefaultExtrasTraefikTransportEnabled = true
	DefaultNamespace                     = "dragonfly-system"
)

var chartVersionPattern = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)

// Config is the operator configuration derived from flags and environment variables.
type Config struct {
	ManagerGRPCDomain            string
	ManagerGRPCPort              int
	ContainerdMirrorPort         int
	ManagerAPIURL                string
	NginxStaticURL               string
	NydusMirrorURL               string
	ClusterIDFallback            string
	MTLSSecretName               string
	ManagerAPICredentialsSecret  string
	DragonflyChartRepo           string
	DragonflyChartName           string
	DragonflyChartVersion        string
	ExtrasChartRepo              string
	ExtrasChartName              string
	ExtrasChartVersion           string
	MirrorChartRepo              string
	MirrorChartName              string
	MirrorChartVersion           string
	HostSyncChartRepo            string
	HostSyncChartName            string
	HostSyncChartVersion         string
	SchedulerCertChartRepo       string
	SchedulerCertChartName       string
	SchedulerCertChartVersion    string
	StaticChartRepo              string
	StaticChartName              string
	StaticChartVersion           string
	StaticReleaseName            string
	ClientReleaseRepo            string
	NydusReleaseRepo             string
	SnapshotterReleaseRepo       string
	ClientSHA256AMD64            string
	ClientSHA256ARM64            string
	NydusSHA256AMD64             string
	NydusSHA256ARM64             string
	SnapshotterSHA256AMD64       string
	SnapshotterSHA256ARM64       string
	SnapshotterBinarySHA256AMD64 string
	SnapshotterBinarySHA256ARM64 string
	ClientVersion                string
	NydusVersion                 string
	SnapshotterVersion           string
	ExtrasIngressController      string
	ExtrasIngressClassName       string
	ExtrasIngressGRPCEnabled     bool
	ExtrasIngressRESTEnabled     bool
	ExtrasIngressTLSSecret       string
	ExtrasTraefikTransport       bool
	Namespace                    string
	LeaderElect                  bool
	Kubeconfig                   string
}

// Load parses args (without the program name) and validates the result.
// Each flag falls back to its environment variable when the flag is not set.
func Load(args []string) (*Config, error) {
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
	defaultContainerdMirrorPort, err := envInt("CONTAINERD_MIRROR_PORT", DefaultContainerdMirrorPort)
	if err != nil {
		return nil, err
	}

	cfg := &Config{}
	fs := flag.NewFlagSet("d7y-cattle-operator", flag.ContinueOnError)
	fs.StringVar(&cfg.ManagerGRPCDomain, "manager-grpc-domain", envOr("MANAGER_GRPC_DOMAIN", ""), "public hostname of the manager gRPC endpoint (no scheme)")
	fs.IntVar(&cfg.ManagerGRPCPort, "manager-grpc-port", defaultManagerGRPCPort, "public port of the manager gRPC endpoint")
	fs.IntVar(&cfg.ContainerdMirrorPort, "containerd-mirror-port", defaultContainerdMirrorPort, "node-local containerd mirror port")
	fs.StringVar(&cfg.ManagerAPIURL, "manager-api-url", envOr("MANAGER_API_URL", ""), "public HTTPS URL of the manager REST API")
	fs.StringVar(&cfg.NginxStaticURL, "nginx-static-url", envOr("NGINX_STATIC_URL", ""), "base URL of the static asset server (http or https)")
	fs.StringVar(&cfg.NydusMirrorURL, "nydus-mirror-url", envOr("NYDUS_MIRROR_URL", ""), "optional HTTPS mirror path for nydus blobs")
	fs.StringVar(&cfg.ClusterIDFallback, "p2p-cluster-id", envOr("P2P_CLUSTER_ID", ""), "fallback cluster ID when provisioning Cluster status.clusterName is empty")
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
	fs.StringVar(&cfg.HostSyncChartRepo, "host-sync-chart-repo", envOr("HOST_SYNC_CHART_REPO", DefaultHostSyncChartRepo), "Helm repository URL of the p2p-host-sync chart")
	fs.StringVar(&cfg.HostSyncChartName, "host-sync-chart-name", envOr("HOST_SYNC_CHART_NAME", DefaultHostSyncChartName), "p2p-host-sync chart name")
	fs.StringVar(&cfg.HostSyncChartVersion, "host-sync-chart-version", envOr("HOST_SYNC_CHART_VERSION", DefaultHostSyncChartVersion), "p2p-host-sync chart version")
	fs.StringVar(&cfg.SchedulerCertChartRepo, "scheduler-cert-chart-repo", envOr("SCHEDULER_CERT_CHART_REPO", DefaultSchedulerCertChartRepo), "Helm repository URL of the p2p-scheduler-cert chart")
	fs.StringVar(&cfg.SchedulerCertChartName, "scheduler-cert-chart-name", envOr("SCHEDULER_CERT_CHART_NAME", DefaultSchedulerCertChartName), "p2p-scheduler-cert chart name")
	fs.StringVar(&cfg.SchedulerCertChartVersion, "scheduler-cert-chart-version", envOr("SCHEDULER_CERT_CHART_VERSION", DefaultSchedulerCertChartVersion), "p2p-scheduler-cert chart version")
	fs.StringVar(&cfg.StaticChartRepo, "static-chart-repo", envOr("STATIC_CHART_REPO", DefaultStaticChartRepo), "Helm repository URL of the p2p-static chart")
	fs.StringVar(&cfg.StaticChartName, "static-chart-name", envOr("STATIC_CHART_NAME", DefaultStaticChartName), "p2p-static chart name")
	fs.StringVar(&cfg.StaticChartVersion, "static-chart-version", envOr("STATIC_CHART_VERSION", DefaultStaticChartVersion), "p2p-static chart version")
	fs.StringVar(&cfg.StaticReleaseName, "static-release-name", envOr("STATIC_RELEASE_NAME", DefaultStaticReleaseName), "Helm release name for p2p-static")
	fs.StringVar(&cfg.ClientReleaseRepo, "client-release-repo", envOr("CLIENT_RELEASE_REPO", DefaultClientReleaseRepo), "GitHub repository for Dragonfly client releases")
	fs.StringVar(&cfg.NydusReleaseRepo, "nydus-release-repo", envOr("NYDUS_RELEASE_REPO", DefaultNydusReleaseRepo), "GitHub repository for nydus releases")
	fs.StringVar(&cfg.SnapshotterReleaseRepo, "snapshotter-release-repo", envOr("NYDUS_SNAPSHOTTER_RELEASE_REPO", DefaultSnapshotterReleaseRepo), "GitHub repository for nydus-snapshotter releases")
	fs.StringVar(&cfg.ClientSHA256AMD64, "client-sha256-amd64", envOr("CLIENT_SHA256_AMD64", DefaultClientSHA256AMD64), "fallback amd64 Dragonfly client tarball SHA-256")
	fs.StringVar(&cfg.ClientSHA256ARM64, "client-sha256-arm64", envOr("CLIENT_SHA256_ARM64", DefaultClientSHA256ARM64), "fallback arm64 Dragonfly client tarball SHA-256")
	fs.StringVar(&cfg.NydusSHA256AMD64, "nydus-sha256-amd64", envOr("NYDUS_SHA256_AMD64", DefaultNydusSHA256AMD64), "fallback amd64 nydus tarball SHA-256")
	fs.StringVar(&cfg.NydusSHA256ARM64, "nydus-sha256-arm64", envOr("NYDUS_SHA256_ARM64", DefaultNydusSHA256ARM64), "fallback arm64 nydus tarball SHA-256")
	fs.StringVar(&cfg.SnapshotterSHA256AMD64, "snapshotter-sha256-amd64", envOr("NYDUS_SNAPSHOTTER_SHA256_AMD64", DefaultSnapshotterSHA256AMD64), "fallback amd64 nydus-snapshotter tarball SHA-256")
	fs.StringVar(&cfg.SnapshotterSHA256ARM64, "snapshotter-sha256-arm64", envOr("NYDUS_SNAPSHOTTER_SHA256_ARM64", DefaultSnapshotterSHA256ARM64), "fallback arm64 nydus-snapshotter tarball SHA-256")
	fs.StringVar(&cfg.SnapshotterBinarySHA256AMD64, "snapshotter-binary-sha256-amd64", envOr("NYDUS_SNAPSHOTTER_BINARY_SHA256_AMD64", DefaultSnapshotterBinarySHA256AMD64), "fallback amd64 containerd-nydus-grpc SHA-256")
	fs.StringVar(&cfg.SnapshotterBinarySHA256ARM64, "snapshotter-binary-sha256-arm64", envOr("NYDUS_SNAPSHOTTER_BINARY_SHA256_ARM64", DefaultSnapshotterBinarySHA256ARM64), "fallback arm64 containerd-nydus-grpc SHA-256")
	fs.StringVar(&cfg.ClientVersion, "client-version", envOr("CLIENT_VERSION", DefaultClientVersion), "Dragonfly client release used by the host bootstrap")
	fs.StringVar(&cfg.NydusVersion, "nydus-version", envOr("NYDUS_VERSION", DefaultNydusVersion), "nydus static release used by the host bootstrap")
	fs.StringVar(&cfg.SnapshotterVersion, "nydus-snapshotter-version", envOr("NYDUS_SNAPSHOTTER_VERSION", DefaultSnapshotterVersion), "upstream nydus-snapshotter release used by the host bootstrap")
	fs.StringVar(&cfg.ExtrasIngressController, "extras-ingress-controller", envOr("EXTRAS_INGRESS_CONTROLLER", DefaultExtrasIngressController), "extras Ingress controller: auto, nginx, or traefik")
	fs.StringVar(&cfg.ExtrasIngressClassName, "extras-ingress-class-name", envOr("EXTRAS_INGRESS_CLASS_NAME", DefaultExtrasIngressClassName), "extras Ingress class name (empty uses the cluster default)")
	fs.BoolVar(&cfg.ExtrasIngressGRPCEnabled, "extras-ingress-grpc-enabled", defaultGRPCEnabled, "render the extras gRPC Ingress")
	fs.BoolVar(&cfg.ExtrasIngressRESTEnabled, "extras-ingress-rest-enabled", defaultRESTEnabled, "render the extras REST Ingress")
	fs.StringVar(&cfg.ExtrasIngressTLSSecret, "extras-ingress-tls-secret", envOr("EXTRAS_INGRESS_TLS_SECRET", DefaultExtrasIngressTLSSecret), "extras Ingress TLS Secret name (empty disables TLS)")
	fs.BoolVar(&cfg.ExtrasTraefikTransport, "extras-traefik-servers-transport-enabled", defaultTraefikTransport, "render the Traefik ServersTransport for the REST Ingress")
	fs.StringVar(&cfg.Namespace, "namespace", envOr("POD_NAMESPACE", DefaultNamespace), "namespace where the operator stores its Secret")
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
	if c.ManagerGRPCPort < 1 || c.ManagerGRPCPort > 65535 {
		add("--manager-grpc-port: %d is outside 1-65535", c.ManagerGRPCPort)
	}
	if c.ContainerdMirrorPort < 1 || c.ContainerdMirrorPort > 65535 {
		add("--containerd-mirror-port: %d is outside 1-65535", c.ContainerdMirrorPort)
	}
	if c.NginxStaticURL == "" {
		add("--nginx-static-url is required")
	} else if err := validateURL(c.NginxStaticURL, "http", "https"); err != nil {
		add("--nginx-static-url: %v", err)
	}
	for _, chart := range []struct {
		name  string
		value string
	}{
		{"--static-chart-repo", c.StaticChartRepo},
		{"--static-chart-name", c.StaticChartName},
	} {
		if chart.value == "" {
			add("%s is required", chart.name)
		}
	}
	if !chartVersionPattern.MatchString(c.StaticChartVersion) {
		add("--static-chart-version: %q is not a release version", c.StaticChartVersion)
	}
	if c.StaticReleaseName == "" {
		add("--static-release-name is required")
	}
	for _, repo := range []struct {
		name  string
		value string
	}{
		{"--client-release-repo", c.ClientReleaseRepo},
		{"--nydus-release-repo", c.NydusReleaseRepo},
		{"--snapshotter-release-repo", c.SnapshotterReleaseRepo},
	} {
		if !regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).MatchString(repo.value) {
			add("%s must be owner/repo", repo.name)
		}
	}
	for _, sum := range []struct {
		name  string
		value string
	}{
		{"--client-sha256-amd64", c.ClientSHA256AMD64},
		{"--client-sha256-arm64", c.ClientSHA256ARM64},
		{"--nydus-sha256-amd64", c.NydusSHA256AMD64},
		{"--nydus-sha256-arm64", c.NydusSHA256ARM64},
		{"--snapshotter-sha256-amd64", c.SnapshotterSHA256AMD64},
		{"--snapshotter-sha256-arm64", c.SnapshotterSHA256ARM64},
		{"--snapshotter-binary-sha256-amd64", c.SnapshotterBinarySHA256AMD64},
		{"--snapshotter-binary-sha256-arm64", c.SnapshotterBinarySHA256ARM64},
	} {
		if sum.value != "" && !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(sum.value) {
			add("%s must be a 64-character lowercase hex digest", sum.name)
		}
	}
	for _, version := range []struct {
		name  string
		value string
	}{
		{"--client-version", c.ClientVersion},
		{"--nydus-version", c.NydusVersion},
		{"--nydus-snapshotter-version", c.SnapshotterVersion},
	} {
		if version.value == "" {
			add("%s is required", version.name)
		} else if !chartVersionPattern.MatchString(version.value) {
			add("%s: %q is not a release version", version.name, version.value)
		}
	}
	if c.NydusMirrorURL != "" {
		if err := validateNydusMirrorURL(c.NydusMirrorURL); err != nil {
			add("--nydus-mirror-url: %v", err)
		}
	}
	if c.ClusterIDFallback != "" {
		if msgs := validation.IsDNS1123Label(c.ClusterIDFallback); len(msgs) > 0 {
			add("--p2p-cluster-id: %s", strings.Join(msgs, "; "))
		}
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
	if c.HostSyncChartRepo == "" {
		add("--host-sync-chart-repo is required")
	} else if err := validateURL(c.HostSyncChartRepo, "http", "https"); err != nil {
		add("--host-sync-chart-repo: %v", err)
	}
	if c.HostSyncChartName == "" {
		add("--host-sync-chart-name is required")
	} else if msgs := validation.IsDNS1123Label(c.HostSyncChartName); len(msgs) > 0 {
		add("--host-sync-chart-name: %s", strings.Join(msgs, "; "))
	}
	if !chartVersionPattern.MatchString(c.HostSyncChartVersion) {
		add("--host-sync-chart-version: %q is not a valid chart version", c.HostSyncChartVersion)
	}
	if c.SchedulerCertChartRepo == "" {
		add("--scheduler-cert-chart-repo is required")
	} else if err := validateURL(c.SchedulerCertChartRepo, "http", "https"); err != nil {
		add("--scheduler-cert-chart-repo: %v", err)
	}
	if c.SchedulerCertChartName == "" {
		add("--scheduler-cert-chart-name is required")
	} else if msgs := validation.IsDNS1123Label(c.SchedulerCertChartName); len(msgs) > 0 {
		add("--scheduler-cert-chart-name: %s", strings.Join(msgs, "; "))
	}
	if !chartVersionPattern.MatchString(c.SchedulerCertChartVersion) {
		add("--scheduler-cert-chart-version: %q is not a valid chart version", c.SchedulerCertChartVersion)
	}
	switch c.ExtrasIngressController {
	case "auto", "nginx", "traefik":
	default:
		add("--extras-ingress-controller: must be auto, nginx, or traefik")
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

func validateNydusMirrorURL(raw string) error {
	if err := validateURL(raw, "http", "https"); err != nil {
		return err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("must not contain credentials, a query, or a fragment")
	}
	if u.Path == "" || u.Path == "/" {
		return errors.New("must include the registry mirror path (for example /docker.io)")
	}
	return nil
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

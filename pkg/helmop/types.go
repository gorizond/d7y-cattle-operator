// Package helmop builds Fleet HelmOp resources for the Dragonfly stack.
package helmop

import (
	"strings"

	fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	APIVersion = "fleet.cattle.io/v1alpha1"
	Kind       = "HelmOp"

	// TargetNamespace is where Fleet installs most charts on downstream clusters.
	TargetNamespace = "dragonfly-system"

	// MirrorTargetNamespace is where the node-local containerd mirror runs.
	MirrorTargetNamespace = "kube-system"
	MirrorPort            = 4001
	MirrorCertsDir        = "/var/lib/rancher/rke2/agent/etc/containerd/certs.d"

	// HarborTargetNamespace is where the Harbor HelmOp is installed.
	HarborTargetNamespace = "harbor"
	// RegistryProxyPort is the proxy Service/container port.
	RegistryProxyPort = 8080

	ManagerGRPCPort = 65003
	// ManagerGRPCEdgePort is the public gRPC port used by schedulers to reach
	// the manager through the edge.
	ManagerGRPCEdgePort = 443
	ManagerRESTPort     = 8080
	// SchedulerPort is the scheduler gRPC container port exposed by the
	// headless peer Service behind the public Traefik front.
	SchedulerPort = 8002
	// SchedulerPublicPort is the public Traefik websecure port used by
	// bootstrap clients and DaemonSets.
	SchedulerPublicPort = 443

	// CertDir is where the mTLS Secret is mounted in Dragonfly pods.
	CertDir = "/etc/dragonfly/certs"

	// VerifiedDragonflyChartVersion is the Dragonfly chart version this
	// operator is tested against. Other versions are allowed but warn.
	VerifiedDragonflyChartVersion = "1.10.1-patch.2"

	KeyDBChartRepo    = "https://enapter.github.io/charts/"
	KeyDBChartName    = "keydb"
	KeyDBChartVersion = "0.48.0"

	HarborChartRepo    = "https://helm.goharbor.io"
	HarborChartName    = "harbor"
	HarborChartVersion = "1.19.4"

	RegistryProxyChartRepo    = "https://gorizond.github.io/d7y-cattle-operator"
	RegistryProxyChartName    = "registry-proxy"
	RegistryProxyChartVersion = "0.1.4"
)

// HelmOpGVR identifies Fleet HelmOps for the resource client.
var HelmOpGVR = schema.GroupVersionResource{
	Group: "fleet.cattle.io", Version: "v1alpha1", Resource: "helmops",
}

// Chart identifies a pinned Helm chart release.
type Chart struct {
	Repo    string
	Name    string
	Version string
}

// HostAlias is a pod /etc/hosts entry, rendered as a Kubernetes HostAlias.
type HostAlias struct {
	IP        string   `json:"ip"`
	Hostnames []string `json:"hostnames"`
}

// Cluster carries the per-cluster inputs shared by the HelmOp builders.
type Cluster struct {
	Name               string
	Namespace          string
	MTLSSecretName     string
	Chart              Chart
	ExtrasChart        Chart
	MirrorChart        Chart
	HarborChart        Chart
	RegistryProxyChart Chart

	// HarborDomain is the Harbor ingress/externalURL host. HarborURL selects
	// an existing/external Harbor; HarborManaged is true only when the operator
	// owns the Harbor HelmOp. HarborAdminSecret is the downstream Secret name.
	HarborDomain       string
	HarborURL          string
	HarborManaged      bool
	HarborAdminSecret  string
	HarborDBPassword   string
	HarborSecretKey    string
	HarborIngressClass string

	// ProxyEnabled is the independent p2p.dragonfly.io/proxy role.
	ProxyEnabled           bool
	ProxyPublicURL         string
	ProxyBasePath          string
	ProxyTLSSecret         string
	ProxyHostAliases       []HostAlias
	ProxyRegistries        []string
	ProxyWebhookSecretName string
	ProxyRobotSecretName   string

	ManagerGRPCDomain  string
	ManagerGRPCPort    int
	ManagerAPIHost     string
	SchedulerClusterID int
	SchedulerReplicas  string
	SeedClientReplicas string
	// SchedulerPublicIP is the currently resolved public scheduler address
	// used as the scheduler advertiseIP and the external-dns target.
	SchedulerPublicIP string
	// SchedulerHost is the per-cluster public scheduler host used by local
	// dynconfig (DaemonSets) and bootstrap clients.
	SchedulerHost string
	// MTLSCertRevision changes only when the P2P leaf certificate changes; it
	// is stamped on pod templates to trigger a controlled rollout.
	MTLSCertRevision string
	// Monitoring enables Dragonfly metrics and ServiceMonitors for the
	// manager, scheduler, seedClient, and client components.
	Monitoring bool
	// P2PDataTLS keeps TLS on the P2P data path (dfdaemon upload and scheduler
	// seedPeer). Scheduler server and manager TLS are independent and remain on.
	P2PDataTLS bool
	Labels     map[string]string
}

// KeyDBName returns the KeyDB HelmOp and release name.
func KeyDBName(cluster string) string { return objectName(cluster, "keydb") }

// ManagerName returns the manager HelmOp and release name.
func ManagerName(cluster string) string { return objectName(cluster, "manager") }

// ControlName returns the control (scheduler) HelmOp and release name.
func ControlName(cluster string) string { return objectName(cluster, "control") }

// DataName returns the data (seedClient/client) HelmOp and release name.
func DataName(cluster string) string { return objectName(cluster, "data") }

// LegacyNydusName returns the release name of the removed in-cluster nydus
// HelmOp. It is only used to delete stale releases on upgrade.
func LegacyNydusName(cluster string) string { return objectName(cluster, "nydus-snapshotter") }

// ExtrasName returns the dragonfly-extras HelmOp and release name.
func ExtrasName(cluster string) string { return objectName(cluster, "extras") }

// MirrorName returns the containerd-mirror HelmOp and release name.
func MirrorName(cluster string) string { return objectName(cluster, "containerd-mirror") }

// HarborName returns the Harbor HelmOp and release name.
func HarborName(cluster string) string { return objectName(cluster, "harbor") }

// RegistryProxyName returns the registry-proxy HelmOp and release name.
func RegistryProxyName(cluster string) string { return objectName(cluster, "registry-proxy") }

// HarborAdminSecretName is the per-cluster downstream admin Secret.
func HarborAdminSecretName(cluster string) string { return objectName(cluster, "harbor-admin") }

// HarborTLSSecretName is the TLS Secret expected by the Harbor ingress.
func HarborTLSSecretName(cluster string) string { return objectName(cluster, "harbor-tls") }

// HarborDBSecretName is the generated internal database password Secret.
func HarborDBSecretName(cluster string) string { return objectName(cluster, "harbor-db") }

// HarborSecretKeyName is the generated Harbor core secret-key Secret.
func HarborSecretKeyName(cluster string) string { return objectName(cluster, "harbor-secret") }

// RegistryProxyWebhookSecretName is the Harbor webhook shared Secret.
func RegistryProxyWebhookSecretName(cluster string) string {
	return objectName(cluster, "registry-proxy-webhook")
}

// RegistryProxyRobotSecretName is the Harbor robot Secret.
func RegistryProxyRobotSecretName(cluster string) string {
	return objectName(cluster, "registry-proxy-robot")
}

// ManagerFullname returns the chart fullnameOverride that makes the Dragonfly
// chart create the <cluster>-manager Service.
func ManagerFullname(cluster string) string {
	return strings.TrimSuffix(ManagerName(cluster), "-manager")
}

// ManagerServiceName returns the manager Service name created by the manager
// release. It is the backend of the extras Ingress.
func ManagerServiceName(cluster string) string { return objectName(cluster, "manager") }

// SchedulerServiceName returns the headless scheduler peer Service name created
// by the control release.
func SchedulerServiceName(cluster string) string {
	return objectName(cluster, "control-scheduler")
}

const (
	maxClusterBase = 35
	// Fleet rejects Helm release names longer than 53 characters.
	maxReleaseName = 53
)

func objectName(cluster, suffix string) string {
	base := strings.Trim(cluster, "-")
	if len(base) > maxClusterBase {
		base = strings.TrimRight(base[:maxClusterBase], "-")
	}
	name := base + "-" + suffix
	if len(name) > maxReleaseName {
		name = strings.TrimRight(name[:maxReleaseName], "-")
	}
	return name
}

func newHelmOp(name string, c Cluster, chart Chart, component string, values map[string]any, dependsOn []string) *fleetv1alpha1.HelmOp {
	return newHelmOpInNamespace(name, TargetNamespace, c, chart, component, values, dependsOn)
}

func newHelmOpInNamespace(name, targetNamespace string, c Cluster, chart Chart, component string, values map[string]any, dependsOn []string) *fleetv1alpha1.HelmOp {
	labels := make(map[string]string, len(c.Labels)+1)
	for key, value := range c.Labels {
		labels[key] = value
	}
	labels["p2p-dragonfly-io"] = component

	op := &fleetv1alpha1.HelmOp{
		TypeMeta: metav1.TypeMeta{APIVersion: APIVersion, Kind: Kind},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: c.Namespace,
			Labels:    labels,
		},
	}
	op.Spec.TargetNamespace = targetNamespace
	op.Spec.Helm = &fleetv1alpha1.HelmOptions{
		Repo:        chart.Repo,
		Chart:       chart.Name,
		Version:     chart.Version,
		ReleaseName: name,
		Values:      &fleetv1alpha1.GenericMap{Data: values},
	}
	op.Spec.Targets = []fleetv1alpha1.BundleTarget{{ClusterName: c.Name}}
	for _, dependency := range dependsOn {
		op.Spec.DependsOn = append(op.Spec.DependsOn, fleetv1alpha1.BundleRef{Name: dependency})
	}
	return op
}

func withMTLS(op *fleetv1alpha1.HelmOp, secretName string) *fleetv1alpha1.HelmOp {
	op.Spec.DownstreamResources = []fleetv1alpha1.DownstreamResource{{Kind: "Secret", Name: secretName}}
	return op
}

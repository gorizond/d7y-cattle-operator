// Package helmop builds Fleet HelmOp resources for the Dragonfly stack.
package helmop

import (
	"fmt"
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
	RegistryProxyChartVersion = "0.1.15"

	// RegistryProxyMirrorProject is the Harbor project that stores converted
	// images and receives the robot's push/grant permissions.
	RegistryProxyMirrorProject = "mirror"

	AdmissionChartRepo    = "https://gorizond.github.io/d7y-cattle-operator"
	AdmissionChartName    = "admission-webhook"
	AdmissionChartVersion = "0.1.2"
)

// Fleet targeting labels. Fleet matches spec.targets[].clusterSelector against
// the provisioning Cluster labels, so the label contract below is the only way
// a shared HelmOp can fan out. Labels are user-managed: the operator stamps the
// cluster-id label (required by the scheduler-host template) and leaves role
// labels untouched.
const (
	// LabelGate opts a Cluster into the Dragonfly stack.
	LabelGate = "d7y-operator-cattle"
	// LabelClusterID carries status.clusterName; the scheduler host template
	// reads it from .ClusterLabels.
	LabelClusterID = "p2p.dragonfly.io/cluster-id"

	LabelRoleManager   = "d7y-operator-manager"
	LabelRoleSeed      = "d7y-operator-seed"
	LabelRoleProxy     = "d7y-operator-proxy"
	LabelRoleHarbor    = "d7y-operator-harbor"
	LabelRoleAdmission = "d7y-operator-admission"

	// AnnotationSchedulerClusterID is the Fleet template input for the
	// scheduler cluster ID. The controller writes it during registration.
	AnnotationSchedulerClusterID = "p2p.dragonfly.io/scheduler-cluster-id"
	// AnnotationSchedulerPublicIP is stamped per Cluster so shared values can
	// render the resolved public advertiseIP without changing the DNS-based
	// resolution itself.
	AnnotationSchedulerPublicIP = "p2p.dragonfly.io/scheduler-public-ip"
	// AnnotationProxyTLSSecret carries the per-cluster registry-proxy ingress
	// TLS Secret. The operator stamps the configured default only when absent.
	AnnotationProxyTLSSecret = "p2p.dragonfly.io/proxy-tls-secret"
	// AnnotationHarborTLSSecret optionally pins a per-cluster Harbor ingress
	// TLS Secret; empty falls back to the fleet-wide d7y-harbor-tls.
	AnnotationHarborTLSSecret    = "p2p.dragonfly.io/harbor-tls-secret"
	AnnotationSchedulerReplicas  = "p2p.dragonfly.io/scheduler-replicas"
	AnnotationSeedClientReplicas = "p2p.dragonfly.io/seedclient-replicas"

	// SharedClusterNameTemplate is the per-target cluster name made available
	// by Fleet inside spec.helm.values.
	SharedClusterNameTemplate = "${ .ClusterName }"
	// SharedSchedulerPublicIPTemplate resolves the per-target public LB IP
	// stamped on the Cluster annotation.
	// The annotation may be absent on a Cluster (for example when a user has not
	// pinned it yet); default keeps Fleet's missingkey=error from failing the
	// whole values render for that cluster.
	SharedSchedulerPublicIPTemplate = `${ index .ClusterAnnotations "p2p.dragonfly.io/scheduler-public-ip" | default "" }`
	// SharedSchedulerClusterIDTemplate resolves the per-target scheduler ID
	// written by the registration flow.
	SharedSchedulerClusterIDTemplate = `${ index .ClusterAnnotations "p2p.dragonfly.io/scheduler-cluster-id" }`
	// SharedProxyTLSSecretTemplate resolves the per-target proxy ingress TLS
	// Secret. Empty means the ingress renders without TLS.
	SharedProxyTLSSecretTemplate = `${ index .ClusterAnnotations "p2p.dragonfly.io/proxy-tls-secret" | default "" }`
	// SharedProxyWebhookAddressTemplate points a same-cluster managed Harbor at
	// the proxy Service. External-Harbor clusters keep the chart default
	// (proxy.publicURL + /api/convert).
	SharedProxyWebhookAddressTemplate = `${ if and (eq (index .ClusterLabels "d7y-operator-harbor") "true") (eq (index .ClusterAnnotations "p2p.dragonfly.io/harbor-url") "") }http://${ .ClusterName }-registry-proxy.dragonfly-system.svc:8080/api/convert${ end }`
	// SharedHarborDomainTemplate resolves the per-target Harbor ingress host.
	SharedHarborDomainTemplate = `${ index .ClusterAnnotations "p2p.dragonfly.io/harbor-domain" }`
	// SharedHarborTLSSecretTemplate resolves the per-target Harbor ingress TLS
	// Secret; absent annotation keeps the fleet-wide d7y-harbor-tls.
	SharedHarborTLSSecretTemplate = `${ index .ClusterAnnotations "p2p.dragonfly.io/harbor-tls-secret" | default "d7y-harbor-tls" }`
	// SharedHarborFullnameTemplate keeps the legacy <cluster>-harbor fullname,
	// so Harbor workloads and the existing PVCs keep their names.
	SharedHarborFullnameTemplate = SharedClusterNameTemplate + "-harbor"
)

// Shared HelmOp names. These are also the Helm release names, so they must be
// fixed and must not contain template markers: Fleet rejects templated
// spec.helm.releaseName values.
const (
	sharedKeyDBName     = "d7y-keydb"
	sharedManagerName   = "d7y-manager"
	sharedControlName   = "d7y-control"
	sharedDataName      = "d7y-data"
	sharedExtrasName    = "d7y-extras"
	sharedMirrorName    = "d7y-containerd-mirror"
	sharedAdmissionName = "d7y-admission"
	sharedProxyName     = "d7y-registry-proxy"
	sharedHarborName    = "d7y-harbor"
)

// SharedKeyDBName returns the shared KeyDB HelmOp and release name.
func SharedKeyDBName() string { return sharedKeyDBName }

// SharedManagerName returns the shared manager HelmOp and release name.
func SharedManagerName() string { return sharedManagerName }

// SharedControlName returns the shared scheduler HelmOp and release name.
func SharedControlName() string { return sharedControlName }

// SharedDataName returns the shared seedClient/client HelmOp and release name.
func SharedDataName() string { return sharedDataName }

// SharedExtrasName returns the shared dragonfly-extras HelmOp and release name.
func SharedExtrasName() string { return sharedExtrasName }

// SharedMirrorName returns the shared containerd-mirror HelmOp and release name.
func SharedMirrorName() string { return sharedMirrorName }

// SharedAdmissionName returns the shared admission-webhook HelmOp and release
// name.
func SharedAdmissionName() string { return sharedAdmissionName }

// SharedRegistryProxyName returns the shared registry-proxy HelmOp and release
// name.
func SharedRegistryProxyName() string { return sharedProxyName }

// SharedHarborName returns the shared Harbor HelmOp and release name.
func SharedHarborName() string { return sharedHarborName }

// SharedHarborAdminSecretName is the fleet-wide Harbor admin Secret.
func SharedHarborAdminSecretName() string { return "d7y-harbor-admin" }

// SharedHarborSecretKeyName is the fleet-wide Harbor core secret-key Secret.
func SharedHarborSecretKeyName() string { return "d7y-harbor-secret" }

// SharedHarborDBSecretName is the fleet-wide Harbor database password Secret.
func SharedHarborDBSecretName() string { return "d7y-harbor-db" }

// SharedRegistryProxyRobotSecretName is the fleet-wide robot Secret shared by
// every registry-proxy target.
func SharedRegistryProxyRobotSecretName() string { return "d7y-registry-proxy-robot" }

// SharedRegistryProxyWebhookSecretName is the fleet-wide Harbor webhook Secret.
func SharedRegistryProxyWebhookSecretName() string { return "d7y-registry-proxy-webhook" }

// GateSelector matches every Cluster that opted into the Dragonfly stack. KeyDB
// must reach manager-only and label-only Clusters too: the manager, Harbor and
// registry-proxy releases point at the cluster-local <cluster>-keydb Service.
func GateSelector() map[string]string {
	return map[string]string{LabelGate: "true"}
}

// ManagerSelector matches the manager role.
func ManagerSelector() map[string]string {
	return map[string]string{LabelGate: "true", LabelRoleManager: "true"}
}

// ProxySelector matches the registry-proxy role.
func ProxySelector() map[string]string {
	return map[string]string{LabelGate: "true", LabelRoleProxy: "true"}
}

// HarborSelector matches the Harbor role.
func HarborSelector() map[string]string {
	return map[string]string{LabelGate: "true", LabelRoleHarbor: "true"}
}

// SharedSelector matches the seed role: the shared control, data, extras, and
// containerd-mirror releases only make sense on Clusters that run a scheduler.
func SharedSelector() map[string]string {
	return map[string]string{LabelGate: "true", LabelRoleSeed: "true"}
}

// AdmissionSelector matches Clusters that opted into the admission webhook.
func AdmissionSelector() map[string]string {
	return map[string]string{LabelGate: "true", LabelRoleAdmission: "true"}
}

// SchedulerHostTemplate returns the Fleet values template for the per-target
// public scheduler host. An empty domain returns an empty host.
func SchedulerHostTemplate(domain string) string {
	domain = strings.TrimSpace(strings.Trim(domain, "."))
	if domain == "" {
		return ""
	}
	return fmt.Sprintf(`d7y-scheduler.${ index .ClusterLabels %q }.`, LabelClusterID) + domain
}

// SharedSchedulerAddrTemplate is the client-facing scheduler address used by
// the dynconfig of the shared data release. It must stay a hostname: the
// scheduler front is a Traefik TCP-TLS route matched by HostSNI, so dialing the
// public IP has no SNI and fails with a transport error. The public IP is only
// used for the scheduler advertiseIP and the extras external-dns target.
func SharedSchedulerAddrTemplate(domain string) string {
	host := SchedulerHostTemplate(domain)
	if host == "" {
		return ""
	}
	return fmt.Sprintf("%s:%d", host, SchedulerPublicPort)
}

// Names are the downstream resource names derived from a cluster name.
type Names struct {
	KeyDB            string
	Manager          string
	Control          string
	Data             string
	ControlScheduler string
	DataSeedClient   string
}

// ClusterNames returns the per-cluster downstream resource names.
func ClusterNames(cluster string) Names {
	return Names{
		KeyDB:            KeyDBName(cluster),
		Manager:          ManagerServiceName(cluster),
		Control:          ControlName(cluster),
		Data:             DataName(cluster),
		ControlScheduler: SchedulerServiceName(cluster),
		DataSeedClient:   DataName(cluster) + "-seed-client",
	}
}

// SharedNames returns the downstream resource names as Fleet templates, so a
// single HelmOp renders the same names on every target cluster.
func SharedNames() Names {
	return Names{
		KeyDB:            SharedClusterNameTemplate + "-keydb",
		Manager:          SharedClusterNameTemplate + "-manager",
		Control:          SharedClusterNameTemplate + "-control",
		Data:             SharedClusterNameTemplate + "-data",
		ControlScheduler: SharedClusterNameTemplate + "-control-scheduler",
		DataSeedClient:   SharedClusterNameTemplate + "-data-seed-client",
	}
}

// HelmOpGVR identifies Fleet HelmOps for the resource client.
var HelmOpGVR = schema.GroupVersionResource{
	Group: "fleet.cattle.io", Version: "v1alpha1", Resource: "helmops",
}

// BundleGVR identifies the Fleet Bundle generated from a HelmOp. The helmop
// controller names the Bundle after the HelmOp and labels it with
// fleet.cattle.io/fleet-helm-name; deleting the Bundle is the bounded way to
// let Fleet purge its BundleDeployments and downstream release.
var BundleGVR = schema.GroupVersionResource{
	Group: "fleet.cattle.io", Version: "v1alpha1", Resource: "bundles",
}

// Chart identifies a pinned Helm chart release.
type Chart struct {
	Repo    string
	Name    string
	Version string
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
	AdmissionChart     Chart

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
	ProxyEnabled bool
	// ProxyDomain is the public proxy host used by the admission rewrite.
	ProxyDomain            string
	ProxyHarborURL         string
	ProxyPublicURL         string
	ProxyBasePath          string
	ProxyTLSSecret         string
	ProxyRegistries        []string
	ProxyWebhookSecretName string
	ProxyRobotSecretName   string
	// ProxyRobotSecretRevision rolls proxy pods when robot credentials rotate.
	ProxyRobotSecretRevision string
	// AdmissionEnabled is the independent p2p.dragonfly.io/admission role.
	AdmissionEnabled         bool
	AdmissionImagePullSecret string
	AdmissionServiceAccount  string
	AdmissionNetworkOnly     bool
	// AdmissionExcludeNamespaces are extra namespaces the webhook must skip.
	AdmissionExcludeNamespaces []string
	// OperatorImageRepository/OperatorImageTag make the admission webhook run
	// the same image as the operator. Empty means the chart default is used.
	OperatorImageRepository string
	OperatorImageTag        string

	ManagerGRPCDomain string
	ManagerGRPCPort   int
	ManagerAPIHost    string
	// ManagerAPICredentialsSecret holds the manager root password and JWT
	// signing key. The operator creates it in its own namespace and replicates
	// it to the HelmOp namespace for Fleet to copy downstream.
	ManagerAPICredentialsSecret string
	SchedulerClusterID          int
	// SchedulerClusterIDTemplate overrides SchedulerClusterID when set. Shared
	// HelmOps use a Fleet template because the ID differs per target cluster.
	SchedulerClusterIDTemplate string
	SchedulerReplicas          string
	SeedClientReplicas         string
	// P2PDomain is the operator-wide scheduler base domain used to build the
	// shared scheduler host template.
	P2PDomain string
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

// AdmissionName returns the admission-webhook HelmOp and release name.
func AdmissionName(cluster string) string { return objectName(cluster, "admission-webhook") }

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

// sharedCluster rewrites the per-cluster inputs that cannot be shared into
// Fleet values templates. Monitoring and P2P data TLS keep their chart
// defaults: Fleet cannot template booleans, and a bool rendered as the string
// "false" is truthy in Helm.
func sharedCluster(c Cluster) Cluster {
	c.Name = SharedClusterNameTemplate
	c.Labels = nil
	c.SchedulerHost = SchedulerHostTemplate(c.P2PDomain)
	c.SchedulerPublicIP = SharedSchedulerPublicIPTemplate
	c.SchedulerClusterIDTemplate = SharedSchedulerClusterIDTemplate
	c.Monitoring = false
	c.P2PDataTLS = false
	return c
}

// newSharedHelmOp builds a fixed-name HelmOp that fans out to clusters matched
// by selector. spec.helm.releaseName and spec.namespace are not templatable, so
// the name is fixed and the same release name is installed on every target.
func newSharedHelmOp(name, targetNamespace string, c Cluster, chart Chart, component string, values map[string]any, dependsOn []string, selector map[string]string) *fleetv1alpha1.HelmOp {
	op := &fleetv1alpha1.HelmOp{
		TypeMeta: metav1.TypeMeta{APIVersion: APIVersion, Kind: Kind},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: c.Namespace,
			// Deliberately not c.Labels: shared objects must not accumulate the
			// ownership labels of whichever Cluster reconciled last.
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "d7y-cattle-operator",
				"p2p-dragonfly-io":             component,
			},
		},
	}
	op.Spec.TargetNamespace = targetNamespace
	op.Spec.Helm = &fleetv1alpha1.HelmOptions{
		Repo:        chart.Repo,
		Chart:       chart.Name,
		Version:     chart.Version,
		ReleaseName: name,
		// Shared HelmOps replace the per-cluster <cluster>-<component>
		// releases during migration, so downstream resources still carry the
		// old release-name/release-namespace Helm annotations. Without
		// takeOwnership Helm refuses to adopt them, e.g.
		// PodDisruptionBudget "poligon-keydb" ... "meta.helm.sh/release-name"
		// must equal "d7y-keydb": current value is "poligon-keydb".
		TakeOwnership: true,
		Values:        &fleetv1alpha1.GenericMap{Data: values},
	}
	op.Spec.Targets = []fleetv1alpha1.BundleTarget{{
		ClusterSelector: &metav1.LabelSelector{MatchLabels: selector},
	}}
	for _, dependency := range dependsOn {
		op.Spec.DependsOn = append(op.Spec.DependsOn, fleetv1alpha1.BundleRef{Name: dependency})
	}
	return op
}

func schedulerClusterIDValue(c Cluster) any {
	if c.SchedulerClusterIDTemplate != "" {
		return c.SchedulerClusterIDTemplate
	}
	return c.SchedulerClusterID
}

// withMTLS declares the downstream P2P Secret so Fleet copies it into the
// release namespace before the chart starts.
func withMTLS(op *fleetv1alpha1.HelmOp, secretName string) *fleetv1alpha1.HelmOp {
	op.Spec.DownstreamResources = []fleetv1alpha1.DownstreamResource{{Kind: "Secret", Name: secretName}}
	return op
}

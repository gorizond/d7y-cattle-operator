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

	ManagerGRPCPort = 65003
	// ManagerGRPCEdgePort is the public gRPC port used by schedulers to reach
	// the manager through the edge.
	ManagerGRPCEdgePort = 443
	ManagerRESTPort     = 8080
	// SchedulerPort is the scheduler gRPC port exposed on each scheduler
	// node and by the headless peer Service.
	SchedulerPort = 8002

	// CertDir is where server leaf Secrets are mounted in Dragonfly pods.
	CertDir = "/etc/dragonfly/certs"
	// ClientCertDir is where the shared client identity is mounted.
	ClientCertDir = "/etc/dragonfly/client-certs"

	// ManagerMTLSSecretName is the manager server identity.
	ManagerMTLSSecretName = "dragonfly-p2p-manager"
	// SchedulerMTLSSecretName is the downstream scheduler server identity.
	SchedulerMTLSSecretName = "dragonfly-p2p-scheduler"
	// SchedulerCertLabel marks HelmOp-generated Bundles that carry a seed
	// scheduler certificate. Shared control depends on that label selector.
	SchedulerCertLabel = "p2p.dragonfly.io/scheduler-cert"
	// AnnotationMTLSCertRevision carries the per-seed scheduler leaf hash.
	AnnotationMTLSCertRevision = "p2p.dragonfly.io/mtls-cert-revision"
	// SchedulerCertRevisionTemplate resolves the per-target scheduler revision.
	SchedulerCertRevisionTemplate = `${ index .ClusterAnnotations "p2p.dragonfly.io/mtls-cert-revision" }`

	// VerifiedDragonflyChartVersion is the Dragonfly chart version this
	// operator is tested against. Other versions are allowed but warn.
	VerifiedDragonflyChartVersion = "1.10.1-patch.2"

	KeyDBChartRepo    = "https://enapter.github.io/charts/"
	KeyDBChartName    = "keydb"
	KeyDBChartVersion = "0.48.0"
)

// Fleet targeting labels. Fleet matches spec.targets[].clusterSelector against
// the provisioning Cluster labels, so the label contract below is the only way
// a shared HelmOp can fan out. Labels are user-managed: the operator stamps the
// cluster-id label for target selection and leaves role labels untouched.
const (
	// LabelGate opts a Cluster into the Dragonfly stack.
	LabelGate = "d7y-operator-cattle"
	// LabelClusterID carries the cluster ID used by shared Fleet targets.
	LabelClusterID = "p2p.dragonfly.io/cluster-id"

	LabelRoleManager = "d7y-operator-manager"
	LabelRoleSeed    = "d7y-operator-seed"

	// AnnotationSchedulerClusterID is the Fleet template input for the
	// scheduler cluster ID. The controller writes it during registration.
	AnnotationSchedulerClusterID = "p2p.dragonfly.io/scheduler-cluster-id"
	AnnotationSchedulerReplicas  = "p2p.dragonfly.io/scheduler-replicas"
	AnnotationSeedClientReplicas = "p2p.dragonfly.io/seedclient-replicas"
	// SharedClusterNameTemplate is the per-target cluster name made available
	// by Fleet inside spec.helm.values.
	SharedClusterNameTemplate = "${ .ClusterName }"
	// SharedManagerIngressEnabledTemplate gates the manager ingress per target:
	// true only when the Cluster carries the manager role label. It must render
	// to a bare YAML bool; a quoted "false" would be truthy in Helm.
	SharedManagerIngressEnabledTemplate = `${ if eq (index .ClusterLabels "` + LabelRoleManager + `") "true" }true${ else }false${ end }`
	// SharedSchedulerPublicIPTemplate resolves the first scheduler-ips entry.
	// The annotation may be absent on a Cluster; default keeps Fleet's
	// missingkey=error from failing the whole values render for that cluster.
	SharedSchedulerPublicIPTemplate = `${ default "" (first (splitList "," (default "" (index .ClusterAnnotations "p2p.dragonfly.io/scheduler-ips")))) }`
	// SharedSchedulerClusterIDTemplate resolves the per-target scheduler ID
	// written by the registration flow.
	SharedSchedulerClusterIDTemplate = `${ index .ClusterAnnotations "p2p.dragonfly.io/scheduler-cluster-id" }`
)

// Shared HelmOp names. These are also the Helm release names, so they must be
// fixed and must not contain template markers: Fleet rejects templated
// spec.helm.releaseName values.
const (
	sharedKeyDBName   = "d7y-keydb"
	sharedManagerName = "d7y-manager"
	sharedControlName = "d7y-control"
	sharedDataName    = "d7y-data"
	sharedExtrasName  = "d7y-extras"
	sharedMirrorName  = "d7y-containerd-mirror"
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

// GateSelector matches every Cluster that opted into the Dragonfly stack. KeyDB
// must reach manager-only and label-only Clusters too: the manager release
// points at the cluster-local <cluster>-keydb Service.
func GateSelector() map[string]string {
	return map[string]string{LabelGate: "true"}
}

// ManagerSelector matches the manager role.
func ManagerSelector() map[string]string {
	return map[string]string{LabelGate: "true", LabelRoleManager: "true"}
}

// SharedSelector matches the seed role: the shared control, data, extras, and
// containerd-mirror releases only make sense on Clusters that run a scheduler.
func SharedSelector() map[string]string {
	return map[string]string{LabelGate: "true", LabelRoleSeed: "true"}
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
	Name                               string
	Namespace                          string
	MTLSSecretName                     string
	Chart                              Chart
	ExtrasChart                        Chart
	MirrorChart                        Chart
	HostSyncChart                      Chart
	SchedulerCertChart                 Chart
	StaticChart                        Chart
	StaticReleaseName                  string
	StaticClientVersion                string
	StaticNydusVersion                 string
	StaticSnapshotterVersion           string
	StaticClientRepo                   string
	StaticNydusRepo                    string
	StaticSnapshotterRepo              string
	StaticClientSHA256AMD64            string
	StaticClientSHA256ARM64            string
	StaticNydusSHA256AMD64             string
	StaticNydusSHA256ARM64             string
	StaticSnapshotterSHA256AMD64       string
	StaticSnapshotterSHA256ARM64       string
	StaticSnapshotterBinarySHA256AMD64 string
	StaticSnapshotterBinarySHA256ARM64 string
	StaticURL                          string
	StaticManagerHost                  string
	StaticTLSSecret                    string
	StaticIngressClassName             string
	// MirrorPort is the node-local dfdaemon/containerd mirror port.
	MirrorPort        int
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
	SchedulerIPs               string
	// MTLSCertRevision is the shared client identity revision.
	MTLSCertRevision string
	// ManagerCertRevision is the manager server identity revision.
	ManagerCertRevision string
	// SchedulerCertRevisionTemplate is the per-target scheduler revision.
	SchedulerCertRevisionTemplate string
	// SchedulerCertRevision is the per-cluster scheduler revision for local
	// control HelmOps.
	SchedulerCertRevision string
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

// HostSyncName returns the per-seed host-sync HelmOp and release name.
func HostSyncName(cluster string) string { return objectName(cluster, "p2p-host-sync") }

// SchedulerBundleName returns the legacy per-seed scheduler certificate raw
// Bundle name. New releases use SchedulerCertName; this name is kept for
// migration and cleanup.
func SchedulerBundleName(cluster string) string { return objectName(cluster, "p2p-scheduler") }

// SchedulerSourceSecretName returns the operator-namespace Secret holding the
// per-seed scheduler leaf before it is embedded in the Bundle.
func SchedulerSourceSecretName(cluster string) string { return objectName(cluster, "p2p-scheduler") }

// DataName returns the data (seedClient/client) HelmOp and release name.
func DataName(cluster string) string { return objectName(cluster, "data") }

// LegacyNydusName returns the release name of the removed in-cluster nydus
// HelmOp. It is only used to delete stale releases on upgrade.
func LegacyNydusName(cluster string) string { return objectName(cluster, "nydus-snapshotter") }

// ExtrasName returns the dragonfly-extras HelmOp and release name.
func ExtrasName(cluster string) string { return objectName(cluster, "extras") }

// MirrorName returns the containerd-mirror HelmOp and release name.
func MirrorName(cluster string) string { return objectName(cluster, "containerd-mirror") }

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
	op.Spec.Targets = []fleetv1alpha1.BundleTarget{{Name: "default", ClusterName: c.Name}}
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
	c.SchedulerClusterIDTemplate = SharedSchedulerClusterIDTemplate
	c.SchedulerCertRevision = ""
	c.SchedulerCertRevisionTemplate = SchedulerCertRevisionTemplate
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
		// PodDisruptionBudget "demo-keydb" ... "meta.helm.sh/release-name"
		// must equal "d7y-keydb": current value is "demo-keydb".
		TakeOwnership: true,
		Values:        &fleetv1alpha1.GenericMap{Data: values},
	}
	op.Spec.Targets = []fleetv1alpha1.BundleTarget{{
		Name:            "default",
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

// withMTLS declares a downstream Secret so Fleet copies it into the release
// namespace before the chart starts.
//
// Each copied Secret must have exactly one owning HelmOp per target cluster.
// Fleet stamps fleet.cattle.io/bundledeployment on the destination Secret; a
// second declaration overwrites the stamp and makes Fleet force a Helm upgrade
// of that release. The shared control owns the client identity; the manager
// owns its own server identity and API credentials.
func withMTLS(op *fleetv1alpha1.HelmOp, secretName string) *fleetv1alpha1.HelmOp {
	for _, resource := range op.Spec.DownstreamResources {
		if resource.Kind == "Secret" && resource.Name == secretName {
			return op
		}
	}
	op.Spec.DownstreamResources = append(op.Spec.DownstreamResources,
		fleetv1alpha1.DownstreamResource{Kind: "Secret", Name: secretName})
	return op
}

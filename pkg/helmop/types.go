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

	// TargetNamespace is where Fleet installs the charts on downstream clusters.
	TargetNamespace = "dragonfly-system"

	// CertDir is where the mTLS Secret is mounted in Dragonfly pods.
	CertDir = "/etc/dragonfly/certs"

	KeyDBChartRepo    = "https://gorizond.github.io/d7y-cattle-operator"
	KeyDBChartName    = "keydb"
	KeyDBChartVersion = "0.1.0"
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

// Cluster carries the per-cluster inputs shared by the HelmOp builders.
type Cluster struct {
	Name           string
	Namespace      string
	MTLSSecretName string
	Chart          Chart
	Labels         map[string]string
}

// KeyDBName returns the KeyDB HelmOp and release name.
func KeyDBName(cluster string) string { return objectName(cluster, "keydb") }

// ManagerName returns the manager HelmOp and release name.
func ManagerName(cluster string) string { return objectName(cluster, "manager") }

// ManagerFullname returns the chart fullnameOverride that makes the Dragonfly
// chart create the <cluster>-manager Service.
func ManagerFullname(cluster string) string {
	return strings.TrimSuffix(ManagerName(cluster), "-manager")
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
	op.Spec.TargetNamespace = TargetNamespace
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

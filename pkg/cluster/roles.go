package cluster

import (
	"strings"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	"k8s.io/apimachinery/pkg/labels"
)

const (
	// LabelEnabled is the single gate for deploying the Dragonfly stack.
	LabelEnabled = "p2p-dragonfly-io"

	AnnotationManager                = "p2p.dragonfly.io/manager"
	AnnotationSeed                   = "p2p.dragonfly.io/seed"
	AnnotationSchedulerClusterID     = "p2p.dragonfly.io/scheduler-cluster-id"
	AnnotationSchedulerClusterIDAuto = "p2p.dragonfly.io/scheduler-cluster-id-auto"
	AnnotationSchedulerReplicas      = "p2p.dragonfly.io/scheduler-replicas"
	AnnotationSeedClientReplicas     = "p2p.dragonfly.io/seedclient-replicas"
	AnnotationMonitoring             = "p2p.dragonfly.io/monitoring"
)

// Roles is the resolved role set for a Cluster.
type Roles struct {
	Enabled bool
	Manager bool
	Seed    bool
}

// ResolveRoles applies the label gate first, then the role annotations.
func ResolveRoles(cluster *provv1.Cluster) Roles {
	if cluster == nil {
		return Roles{}
	}
	enabled := cluster.Labels[LabelEnabled] == "true"
	return Roles{
		Enabled: enabled,
		Manager: enabled && cluster.Annotations[AnnotationManager] == "true",
		Seed:    enabled && cluster.Annotations[AnnotationSeed] == "true",
	}
}

// monitoringEnabled reports whether Dragonfly metrics should be enabled for a
// Cluster. The annotation is a Kubernetes label selector evaluated against the
// provisioning Cluster labels; "true"/"enabled" are unconditional shortcuts.
// Empty, invalid, or non-matching selectors keep monitoring disabled.
func monitoringEnabled(cluster *provv1.Cluster) bool {
	if cluster == nil {
		return false
	}
	raw := strings.TrimSpace(cluster.Annotations[AnnotationMonitoring])
	if raw == "" {
		return false
	}
	if strings.EqualFold(raw, "true") || strings.EqualFold(raw, "enabled") {
		return true
	}
	selector, err := labels.Parse(raw)
	if err != nil {
		return false
	}
	return selector.Matches(labels.Set(cluster.Labels))
}

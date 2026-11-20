package cluster

import (
	"strings"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
)

const (
	// LabelEnabled is the single gate for deploying the Dragonfly stack. It is
	// also the Fleet clusterSelector gate on every shared HelmOp.
	LabelEnabled = helmop.LabelGate

	AnnotationManager                = "p2p.dragonfly.io/manager"
	AnnotationSeed                   = "p2p.dragonfly.io/seed"
	AnnotationSchedulerClusterID     = helmop.AnnotationSchedulerClusterID
	AnnotationSchedulerClusterIDAuto = "p2p.dragonfly.io/scheduler-cluster-id-auto"
	AnnotationSchedulerReplicas      = helmop.AnnotationSchedulerReplicas
	AnnotationSeedClientReplicas     = helmop.AnnotationSeedClientReplicas
	AnnotationMonitoring             = "p2p.dragonfly.io/monitoring"
	// AnnotationP2PDataTLS keeps TLS on the P2P data path (dfdaemon upload
	// server/client and scheduler seedPeer). It is disabled by default; only
	// "true" or "enabled" preserve the legacy data-path TLS values. Scheduler
	// server, manager gRPC/API, and monitoring TLS are always kept.
	AnnotationP2PDataTLS = "p2p.dragonfly.io/p2p-data-tls"
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
	if !enabled {
		return Roles{}
	}
	return Roles{
		Enabled: enabled,
		Manager: cluster.Annotations[AnnotationManager] == "true",
		Seed:    cluster.Annotations[AnnotationSeed] == "true",
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

// p2pDataTLSEnabled reports whether the P2P data path keeps TLS. The default
// is plaintext; only the explicit "true" and "enabled" values opt back in.
func p2pDataTLSEnabled(cluster *provv1.Cluster) bool {
	if cluster == nil {
		return false
	}
	raw := strings.TrimSpace(cluster.Annotations[AnnotationP2PDataTLS])
	return strings.EqualFold(raw, "true") || strings.EqualFold(raw, "enabled")
}

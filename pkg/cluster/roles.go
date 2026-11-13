package cluster

import provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"

const (
	// LabelEnabled is the single gate for deploying the Dragonfly stack.
	LabelEnabled = "p2p-dragonfly-io"

	AnnotationManager = "p2p.dragonfly.io/manager"
	AnnotationSeed    = "p2p.dragonfly.io/seed"
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

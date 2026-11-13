package helmop

import fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"

// KeyDB returns the KeyDB HelmOp: multi-master, no persistence, one per Cluster.
func KeyDB(c Cluster) *fleetv1alpha1.HelmOp {
	values := map[string]any{
		"fullnameOverride": KeyDBName(c.Name),
		"architecture":     "replication",
		"replica": map[string]any{
			"replicaCount": 2,
			"persistence":  map[string]any{"enabled": false},
		},
		"master": map[string]any{
			"persistence": map[string]any{"enabled": false},
		},
	}
	chart := Chart{Repo: KeyDBChartRepo, Name: KeyDBChartName, Version: KeyDBChartVersion}
	return newHelmOp(KeyDBName(c.Name), c, chart, "keydb", values, nil)
}

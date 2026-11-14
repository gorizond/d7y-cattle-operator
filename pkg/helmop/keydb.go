package helmop

import fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"

// KeyDB returns the external enapter/keydb HelmOp: three active KeyDB masters,
// multi-master replication, no persistence, and the service exposed as
// <cluster>-keydb:6379.
func KeyDB(c Cluster) *fleetv1alpha1.HelmOp {
	values := map[string]any{
		"fullnameOverride": KeyDBName(c.Name),
		"nodes":            3,
		"multiMaster":      "yes",
		"activeReplicas":   "yes",
		"threads":          2,
		"protectedMode":    "no",
		"appendonly":       "no",
		"persistentVolume": map[string]any{"enabled": false},
		"imageRepository":  "eqalpha/keydb",
		"imageTag":         "x86_64_v6.3.2",
	}
	chart := Chart{Repo: KeyDBChartRepo, Name: KeyDBChartName, Version: KeyDBChartVersion}
	return newHelmOp(KeyDBName(c.Name), c, chart, "keydb", values, nil)
}

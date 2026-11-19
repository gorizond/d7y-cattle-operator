package helmop

import fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"

// KeyDB returns the external enapter/keydb HelmOp: two active KeyDB masters,
// multi-master replication, no persistence, and the service exposed as
// <cluster>-keydb:6379.
//
// ponytail: enapter/keydb 0.48.0 uses podManagementPolicy OrderedReady and
// server.sh replicates each pod from all peers. With nodes: 3, pod-1 waits for
// pod-2, which OrderedReady never creates; nodes: 2 starts pod-1 against pod-0.
// Raise this only when the chart supports Parallel pod management or an
// explicit peer list.
func KeyDB(c Cluster) *fleetv1alpha1.HelmOp {
	names := ClusterNames(c.Name)
	return newHelmOp(KeyDBName(c.Name), c, keyDBChart(), "keydb", keyDBValues(names), nil)
}

// SharedKeyDB returns the shared KeyDB HelmOp. The chart fullname is a Fleet
// template so each target cluster keeps its <cluster>-keydb Service.
func SharedKeyDB(c Cluster) *fleetv1alpha1.HelmOp {
	c = sharedCluster(c)
	return newSharedHelmOp(SharedKeyDBName(), TargetNamespace, c, keyDBChart(), "keydb",
		keyDBValues(SharedNames()), nil, GateSelector())
}

func keyDBChart() Chart {
	return Chart{Repo: KeyDBChartRepo, Name: KeyDBChartName, Version: KeyDBChartVersion}
}

func keyDBValues(names Names) map[string]any {
	return map[string]any{
		"fullnameOverride": names.KeyDB,
		"nodes":            2,
		"multiMaster":      "yes",
		"activeReplicas":   "yes",
		"threads":          2,
		"protectedMode":    "no",
		"appendonly":       "no",
		"persistentVolume": map[string]any{"enabled": false},
		"imageRepository":  "eqalpha/keydb",
		"imageTag":         "x86_64_v6.3.2",
	}
}

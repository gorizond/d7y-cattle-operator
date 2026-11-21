package helmop

import (
	"strings"

	fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"
)

// HostSync returns the per-cluster host-sync DaemonSet HelmOp. The scheduler
// IP values come from the Cluster annotation and therefore change the HelmOp
// values whenever the seed's scheduler node IPs change.
//
// The DaemonSet mounts the client identity Secret delivered by the shared
// control HelmOp. host-sync depends on control instead of declaring the Secret
// as a second downstream resource: a duplicate declaration would overwrite
// fleet.cattle.io/bundledeployment on the destination Secret and force a Helm
// upgrade on every host-sync reconcile.
func HostSync(c Cluster) *fleetv1alpha1.HelmOp {
	values := map[string]any{
		"schedulerIPs": schedulerAddrs(c.SchedulerIPs),
		"mtlsSecret":   c.MTLSSecretName,
	}
	return newHelmOp(HostSyncName(c.Name), c, c.HostSyncChart, "host-sync", values, []string{SharedControlName()})
}

func schedulerAddrs(raw string) []string {
	seen := map[string]bool{}
	var addrs []string
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if !strings.Contains(item, ":") {
			item += ":8002"
		}
		if seen[item] {
			continue
		}
		seen[item] = true
		addrs = append(addrs, item)
	}
	return addrs
}

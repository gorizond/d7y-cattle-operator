package helmop

import (
	"strings"

	fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"
)

// HostSync returns the per-cluster host-sync DaemonSet HelmOp. The scheduler
// IP values come from the Cluster annotation and therefore change the HelmOp
// values whenever the seed's scheduler node IPs change.
func HostSync(c Cluster) *fleetv1alpha1.HelmOp {
	values := map[string]any{
		"schedulerIPs": schedulerAddrs(c.SchedulerIPs),
		"mtlsSecret":   c.MTLSSecretName,
	}
	op := newHelmOp(HostSyncName(c.Name), c, c.HostSyncChart, "host-sync", values, nil)
	return withMTLS(op, c.MTLSSecretName)
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

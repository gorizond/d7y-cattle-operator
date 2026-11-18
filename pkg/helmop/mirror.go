package helmop

import fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"

// Mirror returns the strict node-local containerd mirror HelmOp. The chart
// writes only certs.d/_default/hosts.toml -> dfdaemon; dfdaemon 1.5.14 infers
// the upstream registry from containerd's ?ns=, so there are no per-registry
// hosts files or X-Dragonfly-Registry headers. Mirror is created only by the
// seed-gated ensureSeed path.
func Mirror(c Cluster) *fleetv1alpha1.HelmOp {
	values := map[string]any{
		"namespace": MirrorTargetNamespace,
		"port":      MirrorPort,
		"hostPath":  MirrorCertsDir,
	}
	return newHelmOpInNamespace(
		MirrorName(c.Name),
		MirrorTargetNamespace,
		c,
		c.MirrorChart,
		"containerd-mirror",
		values,
		[]string{DataName(c.Name)},
	)
}

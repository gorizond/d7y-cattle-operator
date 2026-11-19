package helmop

import fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"

// Mirror returns the strict node-local containerd mirror HelmOp. The chart
// writes certs.d/_default/hosts.toml -> dfdaemon for any registry, plus
// explicit docker.io/index.docker.io aliases pointing at
// registry-1.docker.io. Mirror is created only by the seed-gated path; there
// are no proxy.registries values.
func Mirror(c Cluster) *fleetv1alpha1.HelmOp {
	return newHelmOpInNamespace(
		MirrorName(c.Name),
		MirrorTargetNamespace,
		c,
		c.MirrorChart,
		"containerd-mirror",
		mirrorValues(),
		[]string{DataName(c.Name)},
	)
}

// SharedMirror returns the shared containerd-mirror HelmOp. The chart itself
// has no per-cluster names.
func SharedMirror(c Cluster) *fleetv1alpha1.HelmOp {
	c = sharedCluster(c)
	return newSharedHelmOp(
		SharedMirrorName(),
		MirrorTargetNamespace,
		c,
		c.MirrorChart,
		"containerd-mirror",
		mirrorValues(),
		[]string{SharedDataName()},
		SharedSelector(),
	)
}

func mirrorValues() map[string]any {
	return map[string]any{
		"namespace": MirrorTargetNamespace,
		"port":      MirrorPort,
		"hostPath":  MirrorCertsDir,
	}
}

package helmop

import fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"

// Mirror returns the strict node-local containerd mirror HelmOp. The chart
// writes only the _default, docker.io, and index.docker.io hosts files and
// removes the other registry directories from certs.d.
func Mirror(c Cluster) *fleetv1alpha1.HelmOp {
	values := map[string]any{
		"namespace": MirrorTargetNamespace,
		"port":      MirrorPort,
		"hostPath":  MirrorCertsDir,
		"proxy": map[string]any{
			"enabled":    c.ProxyEnabled && c.ProxyPublicURL != "",
			"url":        c.ProxyPublicURL,
			"registries": c.ProxyRegistries,
		},
	}
	dependsOn := []string{DataName(c.Name)}
	if c.ProxyEnabled && c.ProxyPublicURL != "" {
		dependsOn = append(dependsOn, RegistryProxyName(c.Name))
	}
	return newHelmOpInNamespace(
		MirrorName(c.Name),
		MirrorTargetNamespace,
		c,
		c.MirrorChart,
		"containerd-mirror",
		values,
		dependsOn,
	)
}

package helmop

import fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"

// Control returns the scheduler HelmOp. It is the only Dragonfly release that
// talks to the manager and keeps all scheduler TLS wiring.
func Control(c Cluster) *fleetv1alpha1.HelmOp {
	grpcPort := c.ManagerGRPCPort
	if grpcPort == 0 {
		grpcPort = ManagerGRPCEdgePort
	}
	values := map[string]any{
		"fullnameOverride": ControlName(c.Name),
		"manager":          map[string]any{"enable": false},
		"mysql":            map[string]any{"enable": false},
		"redis":            map[string]any{"enable": false},
		"externalRedis":    map[string]any{"addrs": []any{KeyDBName(c.Name) + ":6379"}},
		"externalManager": map[string]any{
			"host":     c.ManagerGRPCDomain,
			"grpcPort": grpcPort,
		},
		"scheduler": map[string]any{
			"enable":      true,
			"hostNetwork": true,
			"replicas":    c.SchedulerReplicas,
			"config": map[string]any{
				"manager": map[string]any{
					"schedulerClusterID": c.SchedulerClusterID,
					"tls":                tlsPaths(),
				},
				"server":   map[string]any{"tls": tlsPaths()},
				"seedPeer": map[string]any{"tls": tlsPaths()},
			},
			"extraVolumes":      volumes(c.MTLSSecretName, logVolume()),
			"extraVolumeMounts": volumeMounts(logVolumeMount("/var/log/dragonfly/scheduler")),
		},
		"seedClient": map[string]any{"enable": false},
		"client":     map[string]any{"enable": false},
	}
	op := newHelmOp(ControlName(c.Name), c, c.Chart, "control", values, []string{KeyDBName(c.Name)})
	return withMTLS(op, c.MTLSSecretName)
}

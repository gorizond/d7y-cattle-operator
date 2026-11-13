package helmop

import fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"

// Manager returns the manager HelmOp with an embedded MySQL database (no PVC)
// and KeyDB as the external Redis-compatible store.
func Manager(c Cluster) *fleetv1alpha1.HelmOp {
	values := map[string]any{
		"fullnameOverride": ManagerFullname(c.Name),
		"manager": map[string]any{
			"enable": true,
			"config": map[string]any{
				"server": map[string]any{
					"grpc": map[string]any{"tls": tlsPaths()},
					"rest": map[string]any{
						"tls": map[string]any{
							"cert": CertDir + "/tls.crt",
							"key":  CertDir + "/tls.key",
						},
					},
				},
			},
			"extraVolumes":      volumes(c.MTLSSecretName, logVolume()),
			"extraVolumeMounts": volumeMounts(logVolumeMount("/var/log/dragonfly/manager")),
		},
		"mysql": map[string]any{
			"enable":      true,
			"persistence": map[string]any{"enabled": false},
		},
		"redis":         map[string]any{"enable": false},
		"externalRedis": map[string]any{"host": KeyDBName(c.Name), "port": 6379},
		"scheduler":     map[string]any{"enable": false},
		"seedClient":    map[string]any{"enable": false},
		"client":        map[string]any{"enable": false},
	}
	op := newHelmOp(ManagerName(c.Name), c, c.Chart, "manager", values, []string{KeyDBName(c.Name)})
	return withMTLS(op, c.MTLSSecretName)
}

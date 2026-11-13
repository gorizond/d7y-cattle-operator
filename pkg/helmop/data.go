package helmop

import fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"

// Data returns the seedClient/client HelmOp. It deliberately leaves
// externalManager.host empty so the chart renders the local dynconfig.yaml and
// points clients at the control release's scheduler.
//
// The upload and storage ports below are the peer ports dfdaemon advertises to
// schedulers and other peers; with hostNetwork they are bound on the node.
// Dragonfly chart 1.10.1 has no separate advertise fields for dfdaemon.
func Data(c Cluster) *fleetv1alpha1.HelmOp {
	schedulerAddr := SchedulerServiceName(c.Name) + "." + TargetNamespace + ".svc:8002"
	seedClient := map[string]any{
		"enable":      true,
		"hostNetwork": true,
		"replicas":    c.SeedClientReplicas,
		"host":        map[string]any{"schedulerClusterID": c.SchedulerClusterID},
		// The chart defaults to a 100Gi PVC; the seed client must stay PVC-free.
		"persistence": map[string]any{"enable": false},
		"dynconfig":   map[string]any{"scheduler": map[string]any{"addr": schedulerAddr}},
		"config": map[string]any{
			"scheduler": map[string]any{"tls": tlsPaths()},
			"upload":    uploadConfig(4100),
			"proxy":     map[string]any{"ip": "127.0.0.1", "server": map[string]any{"port": 4101}},
			"metrics":   map[string]any{"ip": "127.0.0.1", "server": map[string]any{"port": 4102}},
			"health":    map[string]any{"ip": "127.0.0.1", "server": map[string]any{"port": 4103}},
			"stats":     map[string]any{"ip": "127.0.0.1", "server": map[string]any{"port": 4104}},
			"storage":   map[string]any{"server": map[string]any{"tcpPort": 4105, "quicPort": 4106}},
		},
		"extraVolumes":      volumes(c.MTLSSecretName, logVolume()),
		"extraVolumeMounts": volumeMounts(logVolumeMount("/var/log/dragonfly/dfdaemon/")),
	}
	client := map[string]any{
		"enable":      true,
		"hostNetwork": true,
		"dfinit":      map[string]any{"enable": false},
		"host":        map[string]any{"schedulerClusterID": c.SchedulerClusterID},
		"dynconfig":   map[string]any{"scheduler": map[string]any{"addr": schedulerAddr}},
		"config": map[string]any{
			"scheduler": map[string]any{"tls": tlsPaths()},
			"upload":    uploadConfig(4000),
			"proxy":     map[string]any{"ip": "127.0.0.1", "server": map[string]any{"port": 4001}},
			"metrics":   map[string]any{"ip": "127.0.0.1", "server": map[string]any{"port": 4002}},
			"health":    map[string]any{"ip": "127.0.0.1", "server": map[string]any{"port": 4003}},
			"stats":     map[string]any{"ip": "127.0.0.1", "server": map[string]any{"port": 4004}},
			"storage":   map[string]any{"server": map[string]any{"tcpPort": 4005, "quicPort": 4006}},
		},
		"extraVolumes": volumes(c.MTLSSecretName,
			map[string]any{"name": "storage", "emptyDir": map[string]any{}},
			logVolume(),
		),
		"extraVolumeMounts": volumeMounts(
			map[string]any{"name": "storage", "mountPath": "/var/lib/dragonfly/"},
			logVolumeMount("/var/log/dragonfly/dfdaemon/"),
		),
	}

	values := map[string]any{
		"fullnameOverride": DataName(c.Name),
		"manager":          map[string]any{"enable": false},
		"externalManager":  map[string]any{"host": ""},
		"mysql":            map[string]any{"enable": false},
		"redis":            map[string]any{"enable": false},
		"scheduler":        map[string]any{"enable": false},
		"seedClient":       seedClient,
		"client":           client,
	}
	op := newHelmOp(DataName(c.Name), c, c.Chart, "data", values,
		[]string{ControlName(c.Name), KeyDBName(c.Name)})
	return withMTLS(op, c.MTLSSecretName)
}

func uploadConfig(port int) map[string]any {
	tls := tlsPaths()
	return map[string]any{
		"server": map[string]any{
			"port":   port,
			"caCert": tls["caCert"],
			"cert":   tls["cert"],
			"key":    tls["key"],
		},
		"client": tls,
	}
}

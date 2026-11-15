package helmop

import (
	"fmt"

	fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"
)

// Data returns the seedClient/client HelmOp. It deliberately leaves
// externalManager.host empty so the chart renders the local dynconfig.yaml and
// points clients at the control release's scheduler.
//
// The upload and storage ports below are the peer ports dfdaemon advertises to
// schedulers and other peers; with hostNetwork they are bound on the node.
// The Dragonfly chart has no separate advertise fields for dfdaemon.
func Data(c Cluster) *fleetv1alpha1.HelmOp {
	// DaemonSets on the cluster resolve the stable ClusterIP Service, not the
	// headless peer Service: the P2P client verifies the resolved scheduler IP
	// against an IP SAN, and only the pinned ClusterIP is in the certificate.
	schedulerAddr := fmt.Sprintf("%s.%s.svc:%d", SchedulerClientServiceName(c.Name), TargetNamespace, SchedulerPort)
	seedClient := map[string]any{
		"enable":      true,
		"hostNetwork": true,
		"replicas":    c.SeedClientReplicas,
		// The chart defaults to 8 CPU / 16Gi requests, which no pilot node can
		// fit for three replicas. 1 CPU / 2Gi per replica leaves at least
		// 2.2 CPU / 3.4Gi free on the busiest node even if all three land on
		// one node; in the normal one-replica-per-node spread it leaves at
		// least 4.2 CPU / 7.5Gi with a control scheduler and client on the node.
		"resources": map[string]any{
			"requests": map[string]any{"cpu": "1", "memory": "2Gi"},
			"limits":   map[string]any{"cpu": "4", "memory": "8Gi"},
		},
		// The chart defaults to a 100Gi PVC; the seed client must stay PVC-free.
		"persistence": map[string]any{"enable": false},
		"dynconfig":   map[string]any{"scheduler": map[string]any{"addr": schedulerAddr}},
		"config": map[string]any{
			"host":      map[string]any{"schedulerClusterID": c.SchedulerClusterID},
			"scheduler": tlsPaths(),
			"upload":    uploadConfig(4100),
			"proxy":     map[string]any{"ip": "127.0.0.1", "server": map[string]any{"port": 4101}},
			"metrics":   map[string]any{"ip": "127.0.0.1", "server": map[string]any{"port": 4102}},
			"health":    map[string]any{"ip": "127.0.0.1", "server": map[string]any{"port": 4103}},
			"stats":     map[string]any{"ip": "127.0.0.1", "server": map[string]any{"port": 4104}},
			"storage":   map[string]any{"server": map[string]any{"tcpPort": 4105, "quicPort": 4106}},
		},
		"extraVolumes":      volumes(c.MTLSSecretName, logVolume()),
		"extraVolumeMounts": volumeMounts(logVolumeMount("/var/log/dragonfly/dfdaemon/")),
		"podAnnotations":    certPodAnnotations(c.MTLSCertRevision),
	}
	client := map[string]any{
		"enable":            true,
		"hostNetwork":       true,
		"dfinit":            map[string]any{"enable": false},
		"priorityClassName": "system-node-critical",
		// Pin the already-modest chart request and halve the 4 CPU / 8Gi
		// default limit so every node keeps a bounded client pod.
		"resources": map[string]any{
			"requests": map[string]any{"cpu": "250m", "memory": "512Mi"},
			"limits":   map[string]any{"cpu": "2", "memory": "4Gi"},
		},
		"dynconfig": map[string]any{"scheduler": map[string]any{"addr": schedulerAddr}},
		"config": map[string]any{
			"host":      map[string]any{"schedulerClusterID": c.SchedulerClusterID},
			"scheduler": tlsPaths(),
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
		"podAnnotations": certPodAnnotations(c.MTLSCertRevision),
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
	// The extras release owns the non-headless scheduler client Service used
	// by the local dynconfig, so data waits for it before starting clients.
	op := newHelmOp(DataName(c.Name), c, c.Chart, "data", values,
		[]string{ControlName(c.Name), KeyDBName(c.Name), ExtrasName(c.Name)})
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

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
	names := ClusterNames(c.Name)
	op := newHelmOp(DataName(c.Name), c, c.Chart, "data",
		dataValues(c, names, schedulerAddr(c.SchedulerHost)),
		[]string{names.Control, names.KeyDB, ExtrasName(c.Name)})
	return withMTLS(op, c.MTLSSecretName)
}

// SharedData returns the shared seedClient/client HelmOp. The scheduler host
// and scheduler cluster ID are per-target Fleet templates.
func SharedData(c Cluster) *fleetv1alpha1.HelmOp {
	c = sharedCluster(c)
	names := SharedNames()
	op := newSharedHelmOp(SharedDataName(), TargetNamespace, c, c.Chart, "data",
		dataValues(c, names, schedulerAddr(c.SchedulerHost)),
		[]string{SharedControlName(), SharedKeyDBName(), SharedExtrasName()}, SharedSelector())
	return withMTLS(op, c.MTLSSecretName)
}

// schedulerAddr is the public scheduler front used by DaemonSets: the P2P
// client resolves this host to the public LB IP that is in the certificate SAN
// set, and Traefik passes TLS through to the scheduler.
func schedulerAddr(host string) string {
	return fmt.Sprintf("%s:%d", host, SchedulerPublicPort)
}

func dataValues(c Cluster, names Names, schedulerAddr string) map[string]any {
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
			"host":      map[string]any{"schedulerClusterID": schedulerClusterIDValue(c)},
			"scheduler": tlsPaths(),
			"upload":    uploadConfig(4100, c.P2PDataTLS),
			"download":  map[string]any{"server": map[string]any{"requestRateLimit": 500}},
			"proxy": map[string]any{
				"ip":     "127.0.0.1",
				"server": map[string]any{"port": 4101, "requestRateLimit": 500},
				// Always schedule blob pulls through the seed client: the live
				// poligon-data client needed 500 rps and "always" to stop
				// falling back to the origin.
				"rules": proxyRules(),
			},
			"metrics": map[string]any{"ip": "127.0.0.1", "server": map[string]any{"port": 4102}},
			"health":  map[string]any{"ip": "127.0.0.1", "server": map[string]any{"port": 4103}},
			"stats":   map[string]any{"ip": "127.0.0.1", "server": map[string]any{"port": 4104}},
			"storage": map[string]any{"server": map[string]any{"tcpPort": 4105, "quicPort": 4106}},
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
			"host":      map[string]any{"schedulerClusterID": schedulerClusterIDValue(c)},
			"scheduler": tlsPaths(),
			"upload":    uploadConfig(4000, c.P2PDataTLS),
			"download":  map[string]any{"server": map[string]any{"requestRateLimit": 500}},
			"proxy": map[string]any{
				"ip":     "127.0.0.1",
				"server": map[string]any{"port": 4001, "requestRateLimit": 500},
				// Always schedule blob pulls through the node client: the live
				// poligon-data client needed 500 rps and "always" to stop
				// falling back to the origin.
				"rules": proxyRules(),
			},
			"metrics": map[string]any{"ip": "127.0.0.1", "server": map[string]any{"port": 4002}},
			"health":  map[string]any{"ip": "127.0.0.1", "server": map[string]any{"port": 4003}},
			"stats":   map[string]any{"ip": "127.0.0.1", "server": map[string]any{"port": 4004}},
			"storage": map[string]any{"server": map[string]any{"tcpPort": 4005, "quicPort": 4006}},
		},
		"extraVolumes": volumes(c.MTLSSecretName,
			map[string]any{
				"name": "storage",
				"hostPath": map[string]any{
					"path": "/var/lib/dragonfly",
					"type": "DirectoryOrCreate",
				},
			},
			logVolume(),
		),
		"extraVolumeMounts": volumeMounts(
			map[string]any{"name": "storage", "mountPath": "/var/lib/dragonfly/"},
			logVolumeMount("/var/log/dragonfly/dfdaemon/"),
		),
		"podAnnotations": certPodAnnotations(c.MTLSCertRevision),
	}

	values := map[string]any{
		"fullnameOverride": names.Data,
		"manager":          map[string]any{"enable": false},
		"externalManager":  map[string]any{"host": ""},
		"mysql":            map[string]any{"enable": false},
		"redis":            map[string]any{"enable": false},
		"scheduler":        map[string]any{"enable": false},
		"seedClient":       seedClient,
		"client":           client,
	}
	// The extras release owns the public scheduler TCP route used by the local
	// dynconfig, so data waits for it before starting clients.
	applyMonitoring(values, c.Monitoring)
	return values
}

// proxyRules must render as a YAML sequence: dfdaemon rejects a map form with
// "proxy.rules: invalid type: map, expected a sequence".
func proxyRules() []map[string]any {
	return []map[string]any{
		{"regex": "blobs/sha256.*", "schedulingPolicy": "always"},
		{"regex": "manifests/sha256.*", "schedulingPolicy": "always"},
	}
}

func uploadConfig(port int, tlsEnabled bool) map[string]any {
	server := map[string]any{"port": port}
	upload := map[string]any{"server": server, "client": map[string]any{}}
	if tlsEnabled {
		tls := tlsPaths()
		server["caCert"] = tls["caCert"]
		server["cert"] = tls["cert"]
		server["key"] = tls["key"]
		upload["client"] = tls
	}
	return upload
}

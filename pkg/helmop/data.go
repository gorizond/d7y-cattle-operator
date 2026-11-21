package helmop

import (
	fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"
	"sigs.k8s.io/yaml"
)

// Data returns the seedClient/client HelmOp. It deliberately leaves
// externalManager.host empty so the chart renders the local dynconfig.yaml and
// points clients at the control release's scheduler.
//
// The control HelmOp is the sole Fleet owner of the client identity Secret;
// data only mounts the copy control delivers. Declaring the same Secret here
// would make Fleet overwrite fleet.cattle.io/bundledeployment on the
// destination and force a Helm upgrade on every data reconcile.
//
// The upload and storage ports below are the peer ports dfdaemon advertises to
// schedulers and other peers; with hostNetwork they are bound on the node.
// The Dragonfly chart has no separate advertise fields for dfdaemon.
func Data(c Cluster) *fleetv1alpha1.HelmOp {
	names := ClusterNames(c.Name)
	values := dataValues(c, names)
	op := newHelmOp(DataName(c.Name), c, c.Chart, "data",
		values,
		[]string{names.Control, names.KeyDB, ExtrasName(c.Name)})
	withSchedulerAddrsTemplate(op, values)
	return op
}

// SharedData returns the shared seedClient/client HelmOp. The scheduler host
// and scheduler cluster ID are per-target Fleet templates. Like Data, it
// mounts but never redeclares the client Secret owned by SharedControl.
func SharedData(c Cluster) *fleetv1alpha1.HelmOp {
	c = sharedCluster(c)
	names := SharedNames()
	values := dataValues(c, names)
	op := newSharedHelmOp(SharedDataName(), TargetNamespace, c, c.Chart, "data",
		values,
		[]string{SharedControlName(), SharedKeyDBName(), SharedExtrasName()}, SharedSelector())
	withSchedulerAddrsTemplate(op, values)
	return op
}

func dataValues(c Cluster, names Names) map[string]any {
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
		"config": map[string]any{
			"host":      map[string]any{"schedulerClusterID": schedulerClusterIDValue(c)},
			"scheduler": tlsPaths(),
			"upload":    uploadConfig(4100, c.P2PDataTLS),
			"download":  map[string]any{"server": map[string]any{"requestRateLimit": 500}},
			"proxy": map[string]any{
				"ip":     "127.0.0.1",
				"server": map[string]any{"port": 4101, "requestRateLimit": 500},
				// Always schedule blob pulls through the seed client: the live
				// demo-data client needed 500 rps and "always" to stop
				// falling back to the origin.
				"rules": proxyRules(),
			},
			"metrics": map[string]any{"ip": "127.0.0.1", "server": map[string]any{"port": 4102}},
			"health":  map[string]any{"ip": "127.0.0.1", "server": map[string]any{"port": 4103}},
			"stats":   map[string]any{"ip": "127.0.0.1", "server": map[string]any{"port": 4104}},
			"storage": map[string]any{"server": map[string]any{"tcpPort": 4105, "quicPort": 4106}},
		},
		"extraVolumes":      volumes(secretVolume("mtls", c.MTLSSecretName), logVolume()),
		"extraVolumeMounts": volumeMounts(secretVolumeMount("mtls", CertDir), logVolumeMount("/var/log/dragonfly/dfdaemon/")),
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
		"config": map[string]any{
			"host":      map[string]any{"schedulerClusterID": schedulerClusterIDValue(c)},
			"scheduler": tlsPaths(),
			"upload":    uploadConfig(4000, c.P2PDataTLS),
			"download":  map[string]any{"server": map[string]any{"requestRateLimit": 500}},
			"proxy": map[string]any{
				"ip":     "127.0.0.1",
				"server": map[string]any{"port": mirrorPort(c), "requestRateLimit": 500},
				// Always schedule blob pulls through the node client: the live
				// demo-data client needed 500 rps and "always" to stop
				// falling back to the origin.
				"rules": proxyRules(),
			},
			"metrics": map[string]any{"ip": "127.0.0.1", "server": map[string]any{"port": 4002}},
			"health":  map[string]any{"ip": "127.0.0.1", "server": map[string]any{"port": 4003}},
			"stats":   map[string]any{"ip": "127.0.0.1", "server": map[string]any{"port": 4004}},
			"storage": map[string]any{"server": map[string]any{"tcpPort": 4005, "quicPort": 4006}},
		},
		"extraVolumes": volumes(secretVolume("mtls", c.MTLSSecretName),
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
			secretVolumeMount("mtls", CertDir),
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

const schedulerAddrsTemplate = `dynconfig:
  scheduler:
${- with (default "" (index .ClusterAnnotations "p2p.dragonfly.io/scheduler-ips")) }
    addrs:
${- range splitList "," . }
    - "${ . }:8002"
${- end }
${- else }
    addrs: []
${- end }
`

// withSchedulerAddrsTemplate supplies the per-target dynconfig as Fleet
// template values so the chart receives a real list of scheduler node IPs.
func withSchedulerAddrsTemplate(op *fleetv1alpha1.HelmOp, values map[string]any) {
	templates := map[string]string{}
	for _, key := range []string{"seedClient", "client"} {
		component, ok := values[key].(map[string]any)
		if !ok {
			continue
		}
		withoutDynconfig := make(map[string]any, len(component))
		for k, v := range component {
			if k == "dynconfig" {
				continue
			}
			withoutDynconfig[k] = v
		}
		raw, err := yaml.Marshal(withoutDynconfig)
		if err != nil {
			continue
		}
		templates[key] = string(raw) + schedulerAddrsTemplate
	}
	if len(templates) == 0 {
		return
	}
	if op.Spec.Helm == nil {
		op.Spec.Helm = &fleetv1alpha1.HelmOptions{}
	}
	op.Spec.Helm.TemplateValues = templates
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

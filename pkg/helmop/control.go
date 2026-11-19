package helmop

import fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"

// Control returns the scheduler HelmOp. It is the only Dragonfly release that
// talks to the manager and keeps all scheduler TLS wiring.
func Control(c Cluster) *fleetv1alpha1.HelmOp {
	names := ClusterNames(c.Name)
	op := newHelmOp(ControlName(c.Name), c, c.Chart, "control", controlValues(c, names), []string{names.KeyDB})
	return withMTLS(op, c.MTLSSecretName)
}

// SharedControl returns the shared scheduler HelmOp. The scheduler cluster ID
// and public advertiseIP are per-target Fleet templates; the release name is
// fixed.
func SharedControl(c Cluster) *fleetv1alpha1.HelmOp {
	c = sharedCluster(c)
	names := SharedNames()
	op := newSharedHelmOp(SharedControlName(), TargetNamespace, c, c.Chart, "control",
		controlValues(c, names), []string{SharedKeyDBName()}, SharedSelector())
	return withMTLS(op, c.MTLSSecretName)
}

func controlValues(c Cluster, names Names) map[string]any {
	grpcPort := c.ManagerGRPCPort
	if grpcPort == 0 {
		grpcPort = ManagerGRPCEdgePort
	}
	values := map[string]any{
		"fullnameOverride": names.Control,
		"manager":          map[string]any{"enable": false},
		"mysql":            map[string]any{"enable": false},
		"redis":            map[string]any{"enable": false},
		"externalRedis":    map[string]any{"addrs": []any{names.KeyDB + ":6379"}},
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
					"schedulerClusterID": schedulerClusterIDValue(c),
					"tls":                tlsPaths(),
				},
				"server": map[string]any{
					"tls": tlsPaths(),
					// Bootstrap nodes receive the public Traefik address from
					// the manager dynconfig; DaemonSets use their local
					// dynconfig and the same public host.
					"advertiseIP":   c.SchedulerPublicIP,
					"advertisePort": SchedulerPort,
				},
				"seedPeer": seedPeerConfig(c.P2PDataTLS),
			},
			"extraVolumes":      volumes(c.MTLSSecretName, logVolume()),
			"extraVolumeMounts": volumeMounts(logVolumeMount("/var/log/dragonfly/scheduler")),
			"podAnnotations":    certPodAnnotations(c.MTLSCertRevision),
		},
		"seedClient": map[string]any{"enable": false},
		"client":     map[string]any{"enable": false},
	}
	applyMonitoring(values, c.Monitoring)
	return values
}

func seedPeerConfig(tlsEnabled bool) map[string]any {
	seedPeer := map[string]any{}
	if tlsEnabled {
		seedPeer["tls"] = tlsPaths()
	}
	return seedPeer
}

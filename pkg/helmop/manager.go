package helmop

import (
	"fmt"

	fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"
)

// Manager returns the manager HelmOp with an embedded MySQL database (no PVC)
// and KeyDB as the external Redis-compatible store.
func Manager(c Cluster) *fleetv1alpha1.HelmOp {
	grpcTLS := tlsPaths()
	extraVolumes := []map[string]any{logVolume()}
	extraVolumeMounts := []map[string]any{logVolumeMount("/var/log/dragonfly/manager")}
	if c.ManagerPublicTLSSecret != "" {
		// The manager gRPC server presents the public certificate while the
		// internal CA stays the client CA.
		grpcTLS["cert"] = PublicTLSDir + "/tls.crt"
		grpcTLS["key"] = PublicTLSDir + "/tls.key"
		extraVolumes = append(extraVolumes, publicTLSVolume(c.ManagerPublicTLSSecret))
		extraVolumeMounts = append(extraVolumeMounts, publicTLSVolumeMount())
	}
	values := map[string]any{
		"fullnameOverride": ManagerFullname(c.Name),
		"manager": map[string]any{
			"enable": true,
			"config": map[string]any{
				"server": map[string]any{
					"grpc": map[string]any{"tls": grpcTLS},
					"rest": map[string]any{
						"tls": map[string]any{
							"cert": CertDir + "/tls.crt",
							"key":  CertDir + "/tls.key",
						},
					},
				},
			},
			"extraVolumes":      volumes(c.MTLSSecretName, extraVolumes...),
			"extraVolumeMounts": volumeMounts(extraVolumeMounts...),
		},
		// Bitnami MySQL 11.x reads persistence under the primary subchart.
		"mysql": map[string]any{
			"enable": true,
			"primary": map[string]any{
				"persistence": map[string]any{"enabled": false},
			},
		},
		"redis": map[string]any{"enable": false},
		// The Dragonfly chart expects externalRedis.addrs, not host/port.
		// KeyDB is installed downstream into TargetNamespace; c.Namespace is the
		// Fleet namespace of the HelmOp, so it is not the in-cluster namespace.
		"externalRedis": map[string]any{
			"addrs": []any{fmt.Sprintf("%s.%s.svc.cluster.local:6379", KeyDBName(c.Name), TargetNamespace)},
		},
		"scheduler":  map[string]any{"enable": false},
		"seedClient": map[string]any{"enable": false},
		"client":     map[string]any{"enable": false},
	}
	op := newHelmOp(ManagerName(c.Name), c, c.Chart, "manager", values, []string{KeyDBName(c.Name)})
	return withMTLS(op, c.MTLSSecretName)
}

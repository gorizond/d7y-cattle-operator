package helmop

import (
	"fmt"

	fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"
)

// Manager returns the manager HelmOp with an embedded MySQL database (no PVC)
// and KeyDB as the external Redis-compatible store.
func Manager(c Cluster) *fleetv1alpha1.HelmOp {
	managerValues := map[string]any{
		"enable": true,
		"config": map[string]any{
			"server": map[string]any{
				// The fork chart (1.10.1-patch.2) renders grpc.tls under
				// server.grpc, so the manager serves gRPC over mTLS and
				// its probes use the TLS flags.
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
		"podAnnotations":    certPodAnnotations(c.MTLSCertRevision),
	}
	if c.ManagerAPICredentialsSecret != "" {
		managerValues["extraEnvVars"] = managerCredentialsEnvVars(c.ManagerAPICredentialsSecret)
	}

	values := map[string]any{
		"fullnameOverride": ManagerFullname(c.Name),
		"manager":          managerValues,
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
	applyMonitoring(values, c.Monitoring)
	op := newHelmOp(ManagerName(c.Name), c, c.Chart, "manager", values, []string{KeyDBName(c.Name)})
	op = withMTLS(op, c.MTLSSecretName)
	if c.ManagerAPICredentialsSecret != "" {
		op.Spec.DownstreamResources = append(op.Spec.DownstreamResources,
			fleetv1alpha1.DownstreamResource{Kind: "Secret", Name: c.ManagerAPICredentialsSecret})
	}
	return op
}

// managerCredentialsEnvVars feeds the operator-generated root password and JWT
// signing key to the manager. Only the Secret name and keys are rendered; the
// values never enter Helm values.
func managerCredentialsEnvVars(secretName string) []any {
	return []any{
		managerSecretEnvVar("DRAGONFLY_INITIAL_ROOT_PASSWORD", secretName, "password"),
		managerSecretEnvVar("MANAGER_AUTH_JWT_KEY", secretName, "jwt-key"),
	}
}

func managerSecretEnvVar(name, secretName, key string) map[string]any {
	return map[string]any{
		"name": name,
		"valueFrom": map[string]any{
			"secretKeyRef": map[string]any{
				"name": secretName,
				"key":  key,
			},
		},
	}
}

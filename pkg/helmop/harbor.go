package helmop

import (
	"fmt"

	fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"
)

// Harbor returns the per-cluster Harbor HelmOp. It is retained for back-compat
// and tests; the controller reconciles the shared Harbor.
func Harbor(c Cluster) *fleetv1alpha1.HelmOp {
	values := harborValues(c, harborOptions{
		domain:      c.HarborDomain,
		tlsSecret:   HarborTLSSecretName(c.Name),
		adminSecret: c.HarborAdminSecret,
		secretKey:   c.HarborSecretKey,
		dbPassword:  c.HarborDBPassword,
		redisAddr:   fmt.Sprintf("%s.%s.svc:6379", KeyDBName(c.Name), TargetNamespace),
	})
	op := newHelmOpInNamespace(HarborName(c.Name), HarborTargetNamespace, c, c.HarborChart, "harbor", values, []string{KeyDBName(c.Name)})
	op.Spec.DownstreamResources = harborDownstreamResources(c.HarborAdminSecret, c.HarborSecretKey)
	return op
}

// SharedHarbor returns the shared Harbor HelmOp. The chart fullname is the
// Fleet template ${ .ClusterName }-harbor, so the per-target workloads and the
// existing PVCs (registry, jobservice, database) keep their names. The admin,
// database-password and secret-key Secrets are fleet-wide fixed names.
func SharedHarbor(c Cluster) *fleetv1alpha1.HelmOp {
	c = sharedCluster(c)
	values := harborValues(c, harborOptions{
		fullnameOverride: SharedHarborFullnameTemplate,
		domain:           SharedHarborDomainTemplate,
		tlsSecret:        SharedHarborTLSSecretTemplate,
		adminSecret:      SharedHarborAdminSecretName(),
		secretKey:        SharedHarborSecretKeyName(),
		dbPassword:       c.HarborDBPassword,
		redisAddr:        SharedClusterNameTemplate + "-keydb." + TargetNamespace + ".svc:6379",
	})
	op := newSharedHelmOp(SharedHarborName(), HarborTargetNamespace, c, c.HarborChart, "harbor",
		values, []string{SharedKeyDBName()}, HarborSelector())
	// Do not set helm.force or helm.preserveOnReplace here.
	//
	// The in-cluster Fleet CRD (Rancher 2.15.3 ships Fleet 0.16.x) prunes
	// spec.helm.preserveOnReplace; it only exists in Fleet master:
	//   Warning: unknown field "spec.helm.preserveOnReplace"
	// Without that field a force replace sends each PVC without its
	// server-assigned volumeName and the API server rejects it:
	//   PersistentVolumeClaim "poligon-harbor-jobservice" is invalid:
	//   spec: Forbidden: spec is immutable after creation except
	//   resources.requests and volumeAttributesClassName for bound claims
	//   -  "VolumeName": "pvc-39b93102-2294-47b3-82dd-272b343e98a8",
	//   +  "VolumeName": "",
	//
	// Force was only needed for the one-time adoption of Harbor workloads
	// whose immutable selectors embed .Release.Name; the running pods already
	// carry app.kubernetes.io/instance: d7y-harbor, so a plain Helm upgrade is
	// sufficient and leaves server-set PVC fields alone. PVCs keep
	// resourcePolicy: keep and are not replaced.
	op.Spec.DownstreamResources = harborDownstreamResources(
		SharedHarborAdminSecretName(), SharedHarborSecretKeyName())
	return op
}

func harborDownstreamResources(adminSecret, secretKey string) []fleetv1alpha1.DownstreamResource {
	return []fleetv1alpha1.DownstreamResource{
		{Kind: "Secret", Name: adminSecret},
		{Kind: "Secret", Name: secretKey},
	}
}

type harborOptions struct {
	fullnameOverride string
	domain           string
	tlsSecret        string
	adminSecret      string
	secretKey        string
	dbPassword       string
	redisAddr        string
}

func harborValues(c Cluster, opts harborOptions) map[string]any {
	ingressClass := c.HarborIngressClass
	if ingressClass == "" {
		ingressClass = "traefik"
	}
	values := map[string]any{
		"expose": map[string]any{
			"type": "ingress",
			"tls": map[string]any{
				"enabled":    true,
				"certSource": "secret",
				"secret": map[string]any{
					"secretName": opts.tlsSecret,
				},
			},
			"ingress": map[string]any{
				"className": ingressClass,
				"hosts": map[string]any{
					"core": opts.domain,
				},
			},
		},
		"externalURL": fmt.Sprintf("https://%s", opts.domain),
		"persistence": map[string]any{
			"enabled":        true,
			"resourcePolicy": "keep",
			"persistentVolumeClaim": map[string]any{
				"registry": harborPVC("50Gi"),
				"jobservice": map[string]any{
					"jobLog": harborPVC("5Gi"),
				},
				"database": harborPVC("10Gi"),
			},
		},
		"trivy": map[string]any{"enabled": false},
		"database": map[string]any{
			"type": "internal",
			"internal": map[string]any{
				"password": opts.dbPassword,
			},
		},
		"redis": map[string]any{
			"type": "external",
			"external": map[string]any{
				"addr": opts.redisAddr,
			},
		},
		"existingSecretAdminPassword": opts.adminSecret,
		"existingSecretSecretKey":     opts.secretKey,
		"updateStrategy":              map[string]any{"type": "Recreate"},
	}
	if opts.fullnameOverride != "" {
		values["fullnameOverride"] = opts.fullnameOverride
	}
	return values
}

func harborPVC(size string) map[string]any {
	return map[string]any{
		"size":         size,
		"storageClass": "longhorn",
	}
}

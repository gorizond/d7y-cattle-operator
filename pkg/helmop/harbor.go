package helmop

import (
	"fmt"

	fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"
)

// Harbor returns the Harbor HelmOp for a cluster that owns its Harbor.
// Redis is the existing KeyDB HelmOp; registry, jobservice, and database use
// Longhorn PVCs. There is deliberately no Redis PVC.
func Harbor(c Cluster) *fleetv1alpha1.HelmOp {
	domain := c.HarborDomain
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
					"secretName": HarborTLSSecretName(c.Name),
				},
			},
			"ingress": map[string]any{
				"className": ingressClass,
				"hosts": map[string]any{
					"core": domain,
				},
			},
		},
		"externalURL": fmt.Sprintf("https://%s", domain),
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
				"password": c.HarborDBPassword,
			},
		},
		"redis": map[string]any{
			"type": "external",
			"external": map[string]any{
				"addr": fmt.Sprintf("%s.%s.svc:6379", KeyDBName(c.Name), TargetNamespace),
			},
		},
		"existingSecretAdminPassword": c.HarborAdminSecret,
		"existingSecretSecretKey":     c.HarborSecretKey,
		"updateStrategy":              map[string]any{"type": "Recreate"},
	}
	op := newHelmOpInNamespace(HarborName(c.Name), HarborTargetNamespace, c, c.HarborChart, "harbor", values, []string{KeyDBName(c.Name)})
	op.Spec.DownstreamResources = []fleetv1alpha1.DownstreamResource{
		{Kind: "Secret", Name: c.HarborAdminSecret},
		{Kind: "Secret", Name: c.HarborSecretKey},
	}
	return op
}

func harborPVC(size string) map[string]any {
	return map[string]any{
		"size":         size,
		"storageClass": "longhorn",
	}
}

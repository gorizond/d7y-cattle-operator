package helmop

import (
	fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// VPAGVR identifies the VerticalPodAutoscaler API used to decide whether the
// extras chart should render VPAs.
var VPAGVR = schema.GroupVersionResource{
	Group: "autoscaling.k8s.io", Version: "v1", Resource: "verticalpodautoscalers",
}

// ExtrasOptions selects which extras resources the chart renders.
type ExtrasOptions struct {
	IngressEnabled bool
	VPAEnabled     bool
	Manager        bool
	Seed           bool
}

// Extras returns the dragonfly-extras HelmOp. The Ingress and
// VerticalPodAutoscaler objects are rendered by the chart; the operator only
// creates this HelmOp and needs no Ingress/VPA RBAC.
func Extras(c Cluster, opts ExtrasOptions) *fleetv1alpha1.HelmOp {
	values := map[string]any{
		"ingress": map[string]any{
			"enabled": opts.IngressEnabled,
			"grpc": map[string]any{
				"host":    c.ManagerGRPCDomain,
				"service": ManagerServiceName(c.Name),
				"port":    ManagerGRPCPort,
			},
			"rest": map[string]any{
				"host":    c.ManagerAPIHost,
				"service": ManagerServiceName(c.Name),
				"port":    ManagerRESTPort,
			},
			"mtlsSecret": c.MTLSSecretName,
		},
		"vpa": map[string]any{
			"enabled":     opts.VPAEnabled,
			"clusterName": c.Name,
			"namespace":   TargetNamespace,
			"targets": map[string]any{
				"keydb":   vpaTarget(true, "Deployment", KeyDBName(c.Name)),
				"manager": vpaTarget(opts.Manager, "Deployment", ManagerName(c.Name)),
				"control": vpaTarget(opts.Seed, "StatefulSet", SchedulerServiceName(c.Name)),
				"data":    vpaTarget(opts.Seed, "StatefulSet", DataName(c.Name)+"-seed-client"),
				"nydus":   vpaTarget(opts.Seed, "DaemonSet", NydusName(c.Name)),
			},
		},
	}

	var dependsOn []string
	if opts.IngressEnabled {
		dependsOn = []string{ManagerName(c.Name)}
	}
	return newHelmOp(ExtrasName(c.Name), c, c.ExtrasChart, "extras", values, dependsOn)
}

func vpaTarget(enabled bool, kind, name string) map[string]any {
	return map[string]any{"enabled": enabled, "kind": kind, "name": name}
}

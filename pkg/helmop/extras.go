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
	// IngressController is "nginx" or "traefik"; empty defaults to nginx.
	IngressController string
	IngressClassName  string
	GRPCEnabled       bool
	RESTEnabled       bool
	TLSSecret         string
	// TraefikServersTransportEnabled renders the Traefik ServersTransport and
	// its REST Ingress annotation.
	TraefikServersTransportEnabled bool
	VPAEnabled                     bool
	Manager                        bool
	Seed                           bool
}

// Extras returns the dragonfly-extras HelmOp. The Ingress and
// VerticalPodAutoscaler objects are rendered by the chart; the operator only
// creates this HelmOp and needs no Ingress/VPA RBAC.
func Extras(c Cluster, opts ExtrasOptions) *fleetv1alpha1.HelmOp {
	names := ClusterNames(c.Name)
	values := extrasValues(c, opts, names)

	var dependsOn []string
	if opts.IngressEnabled {
		dependsOn = []string{names.Manager}
	}
	return newHelmOp(ExtrasName(c.Name), c, c.ExtrasChart, "extras", values, dependsOn)
}

// SharedExtras returns the shared dragonfly-extras HelmOp. It targets seed
// Clusters: the VPA target names are per-target Fleet templates. The manager
// Ingress is gated per target by a templated boolean on the
// d7y-operator-manager label: seed-only Clusters render ingress.enabled=false,
// so the chart skips the manager Ingress and IngressRouteTCP entirely.
func SharedExtras(c Cluster, opts ExtrasOptions) *fleetv1alpha1.HelmOp {
	c = sharedCluster(c)
	names := SharedNames()
	opts.IngressEnabled = true
	opts.Manager = true
	opts.Seed = true
	values := extrasValues(c, opts, names)
	values["ingress"].(map[string]any)["enabled"] = SharedManagerIngressEnabledTemplate
	return newSharedHelmOp(SharedExtrasName(), TargetNamespace, c, c.ExtrasChart, "extras",
		values, nil, SharedSelector())
}

func extrasValues(c Cluster, opts ExtrasOptions, names Names) map[string]any {
	controller := opts.IngressController
	if controller == "" {
		controller = "nginx"
	}
	values := map[string]any{
		"ingress": map[string]any{
			"enabled":    opts.IngressEnabled,
			"controller": controller,
			"className":  opts.IngressClassName,
			"grpc": map[string]any{
				"enabled": opts.GRPCEnabled,
				"host":    c.ManagerGRPCDomain,
				"service": names.Manager,
				"port":    ManagerGRPCPort,
				// nginx-only P2P Secret for the GRPCS backend and mTLS.
				// Traefik uses TCP passthrough and does not consume it. REST
				// keeps opts.TLSSecret.
				"tlsSecret": ManagerMTLSSecretName,
			},
			"rest": map[string]any{
				"enabled": opts.RESTEnabled,
				"host":    c.ManagerAPIHost,
				"service": names.Manager,
				"port":    ManagerRESTPort,
			},
			"tls": map[string]any{
				"enabled":    opts.TLSSecret != "",
				"secretName": opts.TLSSecret,
			},
			"traefik": map[string]any{
				"serversTransport": map[string]any{
					"enabled":            opts.TraefikServersTransportEnabled,
					"insecureSkipVerify": true,
				},
			},
			"mtlsSecret": ManagerMTLSSecretName,
		},
		"vpa": map[string]any{
			"enabled":     opts.VPAEnabled,
			"clusterName": c.Name,
			"namespace":   TargetNamespace,
			"targets": map[string]any{
				"keydb":   vpaTarget(true, "Deployment", names.KeyDB),
				"manager": vpaTarget(opts.Manager, "Deployment", names.Manager),
				"control": vpaTarget(opts.Seed, "StatefulSet", names.ControlScheduler),
				"data":    vpaTarget(opts.Seed, "StatefulSet", names.DataSeedClient),
			},
		},
	}
	return values
}

func vpaTarget(enabled bool, kind, name string) map[string]any {
	return map[string]any{"enabled": enabled, "kind": kind, "name": name}
}

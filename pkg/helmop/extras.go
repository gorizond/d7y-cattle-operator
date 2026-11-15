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
	NydusEnabled                   bool
	Manager                        bool
	Seed                           bool
	// SchedulerPublicEnabled renders the Traefik TCP passthrough front for the
	// fork chart's headless scheduler Service. It is independent of the manager
	// Ingress toggle.
	SchedulerPublicEnabled bool
	SchedulerPublicIP      string
	// SchedulerDomain and SchedulerClusterID build the DNS alias
	// d7y-scheduler.<cluster-id>.<domain>.
	SchedulerDomain    string
	SchedulerClusterID string
	// SchedulerService is the fork chart's headless scheduler Service that
	// backs the public Traefik TCP route.
	SchedulerService string
	SchedulerPort    int
}

// Extras returns the dragonfly-extras HelmOp. The Ingress and
// VerticalPodAutoscaler objects are rendered by the chart; the operator only
// creates this HelmOp and needs no Ingress/VPA RBAC.
func Extras(c Cluster, opts ExtrasOptions) *fleetv1alpha1.HelmOp {
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
				"service": ManagerServiceName(c.Name),
				"port":    ManagerGRPCPort,
				// nginx-only P2P Secret for the GRPCS backend and mTLS.
				// Traefik uses TCP passthrough and does not consume it. REST
				// keeps opts.TLSSecret.
				"tlsSecret": c.MTLSSecretName,
			},
			"rest": map[string]any{
				"enabled": opts.RESTEnabled,
				"host":    c.ManagerAPIHost,
				"service": ManagerServiceName(c.Name),
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
			"mtlsSecret": c.MTLSSecretName,
		},
		"scheduler": map[string]any{
			"enabled":   opts.SchedulerPublicEnabled,
			"clusterID": opts.SchedulerClusterID,
			"domain":    opts.SchedulerDomain,
			"publicIP":  opts.SchedulerPublicIP,
			"service":   opts.SchedulerService,
			"port":      opts.SchedulerPort,
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
				"nydus":   vpaTarget(opts.Seed && opts.NydusEnabled, "DaemonSet", NydusName(c.Name)),
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

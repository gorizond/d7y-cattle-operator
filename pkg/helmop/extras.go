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
	// scheduler. It is independent of the manager Ingress toggle.
	SchedulerPublicEnabled bool
	SchedulerPublicIP      string
	// SchedulerHost is the per-cluster DNS alias that resolves to
	// SchedulerPublicIP (d7y-scheduler.<cluster>.<domain>).
	SchedulerHost    string
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
				// The scheduler verifies the manager with the P2P CA, so the
				// gRPC edge must serve the P2P cert. REST keeps opts.TLSSecret.
				"tlsSecret": c.MTLSSecretName,
				// Traefik connects to the manager with TLS + client mTLS and
				// verifies it against the P2P CA.
				"serversTransport": map[string]any{
					"enabled":    true,
					"serverName": c.ManagerGRPCDomain,
					"secretName": c.MTLSSecretName,
				},
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
			// The public scheduler front is an end-to-end mTLS TCP
			// passthrough. The DNS alias points at the public LB IP; the P2P
			// certificate carries the IP SAN, not the hostname.
			"scheduler": map[string]any{
				"enabled":  opts.SchedulerPublicEnabled,
				"publicIP": opts.SchedulerPublicIP,
				"host":     opts.SchedulerHost,
				"service":  opts.SchedulerService,
				"port":     opts.SchedulerPort,
			},
			"mtlsSecret":   c.MTLSSecretName,
			"mtlsCA":       c.MTLSCA,
			"mtlsCASecret": c.MTLSSecretName + "-ca",
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

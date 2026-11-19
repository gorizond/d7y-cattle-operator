package helmop

import "testing"

func TestExtrasHelmOp(t *testing.T) {
	op := Extras(testCluster(), ExtrasOptions{
		IngressEnabled:                 true,
		IngressController:              "nginx",
		GRPCEnabled:                    true,
		RESTEnabled:                    true,
		TraefikServersTransportEnabled: true,
		VPAEnabled:                     true,
		Manager:                        true,
		Seed:                           true,
		SchedulerPublicEnabled:         true,
		SchedulerPublicIP:              "203.0.113.10",
		SchedulerDomain:                "example.com",
		SchedulerClusterID:             "c-m-test",
		SchedulerService:               "c1-control-scheduler",
		SchedulerPort:                  SchedulerPort,
	})

	if op.Name != "c1-extras" || op.Namespace != "fleet-default" {
		t.Errorf("name/namespace = %s/%s", op.Name, op.Namespace)
	}
	if op.Labels["p2p-dragonfly-io"] != "extras" {
		t.Errorf("component label = %q", op.Labels["p2p-dragonfly-io"])
	}
	if len(op.Spec.DependsOn) != 1 || op.Spec.DependsOn[0].Name != "c1-manager" {
		t.Errorf("dependsOn = %+v", op.Spec.DependsOn)
	}
	if len(op.Spec.DownstreamResources) != 0 {
		t.Errorf("downstreamResources = %+v, want none", op.Spec.DownstreamResources)
	}
	if op.Spec.Helm.Chart != "dragonfly-extras" || op.Spec.Helm.Version != "0.1.10" {
		t.Errorf("chart = %s %s", op.Spec.Helm.Chart, op.Spec.Helm.Version)
	}

	values := op.Spec.Helm.Values.Data
	if got := valueAt(t, values, "ingress", "enabled"); got != true {
		t.Errorf("ingress.enabled = %v", got)
	}
	if got := valueAt(t, values, "ingress", "grpc", "host"); got != "grpc.example.com" {
		t.Errorf("ingress.grpc.host = %v", got)
	}
	if got := valueAt(t, values, "ingress", "grpc", "service"); got != "c1-manager" {
		t.Errorf("ingress.grpc.service = %v", got)
	}
	if got := valueAt(t, values, "ingress", "grpc", "port"); got != ManagerGRPCPort {
		t.Errorf("ingress.grpc.port = %v", got)
	}
	if got := valueAt(t, values, "ingress", "rest", "host"); got != "api.example.com" {
		t.Errorf("ingress.rest.host = %v", got)
	}
	if got := valueAt(t, values, "ingress", "rest", "service"); got != "c1-manager" {
		t.Errorf("ingress.rest.service = %v", got)
	}
	if got := valueAt(t, values, "ingress", "rest", "port"); got != ManagerRESTPort {
		t.Errorf("ingress.rest.port = %v", got)
	}
	if got := valueAt(t, values, "ingress", "mtlsSecret"); got != "dragonfly-p2p-mtls" {
		t.Errorf("ingress.mtlsSecret = %v", got)
	}
	if got := valueAt(t, values, "ingress", "controller"); got != "nginx" {
		t.Errorf("ingress.controller = %v", got)
	}
	if got := valueAt(t, values, "ingress", "className"); got != "" {
		t.Errorf("ingress.className = %v", got)
	}
	if got := valueAt(t, values, "ingress", "grpc", "enabled"); got != true {
		t.Errorf("ingress.grpc.enabled = %v", got)
	}
	if got := valueAt(t, values, "ingress", "rest", "enabled"); got != true {
		t.Errorf("ingress.rest.enabled = %v", got)
	}
	if got := valueAt(t, values, "ingress", "tls", "enabled"); got != false {
		t.Errorf("ingress.tls.enabled = %v", got)
	}
	if got := valueAt(t, values, "ingress", "tls", "secretName"); got != "" {
		t.Errorf("ingress.tls.secretName = %v", got)
	}
	if got := valueAt(t, values, "ingress", "traefik", "serversTransport", "enabled"); got != true {
		t.Errorf("traefik ServersTransport enabled = %v", got)
	}
	if got := valueAt(t, values, "ingress", "traefik", "serversTransport", "insecureSkipVerify"); got != true {
		t.Errorf("traefik insecureSkipVerify = %v", got)
	}
	if got := valueAt(t, values, "scheduler", "enabled"); got != true {
		t.Errorf("scheduler.enabled = %v", got)
	}
	if got := valueAt(t, values, "scheduler", "publicIP"); got != "203.0.113.10" {
		t.Errorf("scheduler.publicIP = %v", got)
	}
	if got := valueAt(t, values, "scheduler", "domain"); got != "example.com" {
		t.Errorf("scheduler.domain = %v", got)
	}
	if got := valueAt(t, values, "scheduler", "clusterID"); got != "c-m-test" {
		t.Errorf("scheduler.clusterID = %v", got)
	}
	if got := valueAt(t, values, "scheduler", "service"); got != "c1-control-scheduler" {
		t.Errorf("scheduler.service = %v", got)
	}
	if got := valueAt(t, values, "scheduler", "port"); got != SchedulerPort {
		t.Errorf("scheduler.port = %v", got)
	}

	if got := valueAt(t, values, "vpa", "enabled"); got != true {
		t.Errorf("vpa.enabled = %v", got)
	}
	if got := valueAt(t, values, "vpa", "clusterName"); got != "c1" {
		t.Errorf("vpa.clusterName = %v", got)
	}
	if got := valueAt(t, values, "vpa", "namespace"); got != TargetNamespace {
		t.Errorf("vpa.namespace = %v", got)
	}

	targets := nested(t, values, "vpa", "targets")
	for name, want := range map[string]struct {
		enabled bool
		kind    string
		target  string
	}{
		"keydb":   {true, "Deployment", "c1-keydb"},
		"manager": {true, "Deployment", "c1-manager"},
		"control": {true, "StatefulSet", "c1-control-scheduler"},
		"data":    {true, "StatefulSet", "c1-data-seed-client"},
	} {
		targetMap := nested(t, targets, name)
		if targetMap["enabled"] != want.enabled || targetMap["kind"] != want.kind || targetMap["name"] != want.target {
			t.Errorf("vpa target %s = %v, want enabled=%v kind=%s name=%s", name, targetMap, want.enabled, want.kind, want.target)
		}
	}
}

func TestExtrasSeedOnly(t *testing.T) {
	op := Extras(testCluster(), ExtrasOptions{
		IngressEnabled: false,
		VPAEnabled:     true,
		Seed:           true,
	})

	if len(op.Spec.DependsOn) != 0 {
		t.Errorf("dependsOn = %+v, want none", op.Spec.DependsOn)
	}
	values := op.Spec.Helm.Values.Data
	if got := valueAt(t, values, "ingress", "enabled"); got != false {
		t.Errorf("ingress.enabled = %v", got)
	}
	if got := valueAt(t, values, "vpa", "targets", "manager", "enabled"); got != false {
		t.Errorf("manager VPA enabled = %v", got)
	}
	if got := valueAt(t, values, "vpa", "targets", "keydb", "enabled"); got != true {
		t.Errorf("keydb VPA enabled = %v", got)
	}
	for _, component := range []string{"control", "data"} {
		if got := valueAt(t, values, "vpa", "targets", component, "enabled"); got != true {
			t.Errorf("%s VPA enabled = %v", component, got)
		}
	}
}

func TestExtrasGRPCUsesP2PCert(t *testing.T) {
	op := Extras(testCluster(), ExtrasOptions{
		IngressEnabled:    true,
		IngressController: "traefik",
		GRPCEnabled:       true,
		RESTEnabled:       true,
		TLSSecret:         "demo-manager-public-tls",
		Manager:           true,
	})

	values := op.Spec.Helm.Values.Data
	if got := valueAt(t, values, "ingress", "grpc", "tlsSecret"); got != "dragonfly-p2p-mtls" {
		t.Errorf("ingress.grpc.tlsSecret = %v, want the P2P secret", got)
	}
	if got := valueAt(t, values, "ingress", "tls", "secretName"); got != "demo-manager-public-tls" {
		t.Errorf("ingress.tls.secretName = %v, want the public REST secret", got)
	}
}

func TestExtrasVPADisabled(t *testing.T) {
	op := Extras(testCluster(), ExtrasOptions{IngressEnabled: true, Manager: true})
	if got := valueAt(t, op.Spec.Helm.Values.Data, "vpa", "enabled"); got != false {
		t.Errorf("vpa.enabled = %v", got)
	}
}

func TestExtrasTraefikManagerOnly(t *testing.T) {
	op := Extras(testCluster(), ExtrasOptions{
		IngressEnabled:                 true,
		IngressController:              "traefik",
		IngressClassName:               "traefik",
		GRPCEnabled:                    false,
		RESTEnabled:                    true,
		TLSSecret:                      "dragonfly-p2p-mtls",
		TraefikServersTransportEnabled: true,
		Manager:                        true,
	})

	values := op.Spec.Helm.Values.Data
	if got := valueAt(t, values, "ingress", "controller"); got != "traefik" {
		t.Errorf("ingress.controller = %v", got)
	}
	if got := valueAt(t, values, "ingress", "className"); got != "traefik" {
		t.Errorf("ingress.className = %v", got)
	}
	if got := valueAt(t, values, "ingress", "grpc", "enabled"); got != false {
		t.Errorf("ingress.grpc.enabled = %v", got)
	}
	if got := valueAt(t, values, "ingress", "rest", "enabled"); got != true {
		t.Errorf("ingress.rest.enabled = %v", got)
	}
	if got := valueAt(t, values, "ingress", "tls", "enabled"); got != true {
		t.Errorf("ingress.tls.enabled = %v", got)
	}
	if got := valueAt(t, values, "ingress", "tls", "secretName"); got != "dragonfly-p2p-mtls" {
		t.Errorf("ingress.tls.secretName = %v", got)
	}
	if got := valueAt(t, values, "ingress", "traefik", "serversTransport", "enabled"); got != true {
		t.Errorf("traefik ServersTransport enabled = %v", got)
	}
}

package cluster

import (
	"strings"
	"testing"

	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
)

func TestSharedExtrasTemplatedManagerBackends(t *testing.T) {
	controller, store, _ := newTestController(t)
	cluster := testSeedCluster("")
	delete(cluster.Annotations, AnnotationSeed)
	cluster.Annotations[AnnotationManager] = "true"

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}

	extras := store.object("fleet-default/" + helmop.SharedExtrasName())
	if extras == nil {
		t.Fatal("shared extras HelmOp was not created")
	}
	if got := dependsOn(extras); len(got) != 0 {
		t.Errorf("shared extras dependsOn = %v, want none", got)
	}
	if got := selectorLabels(extras); got[helmop.LabelGate] != "true" || got[helmop.LabelRoleSeed] != "true" {
		t.Errorf("extras selector = %v, want gate+seed", got)
	}

	values := helmValues(t, extras)
	if got := valueAtPath(t, values, "ingress", "enabled"); got != helmop.SharedManagerIngressEnabledTemplate {
		t.Errorf("ingress.enabled = %v, want the manager-label template", got)
	}
	if got := valueAtPath(t, values, "ingress", "grpc", "host"); got != "grpc.example.com" {
		t.Errorf("ingress.grpc.host = %v", got)
	}
	for _, path := range [][]string{
		{"ingress", "grpc", "service"},
		{"ingress", "rest", "service"},
	} {
		if got := valueAtPath(t, values, path...); got != helmop.SharedClusterNameTemplate+"-manager" {
			t.Errorf("%v = %v, want templated manager name", path, got)
		}
	}
	if got := valueAtPath(t, values, "ingress", "grpc", "port"); got != int64(65003) {
		t.Errorf("ingress.grpc.port = %v", got)
	}
	if got := valueAtPath(t, values, "ingress", "rest", "host"); got != "api.example.com" {
		t.Errorf("ingress.rest.host = %v", got)
	}
	if got := valueAtPath(t, values, "ingress", "rest", "port"); got != int64(8080) {
		t.Errorf("ingress.rest.port = %v", got)
	}
	if got := valueAtPath(t, values, "ingress", "mtlsSecret"); got != "dragonfly-p2p-mtls" {
		t.Errorf("ingress.mtlsSecret = %v", got)
	}
	if got := valueAtPath(t, values, "ingress", "controller"); got != "nginx" {
		t.Errorf("ingress.controller = %v", got)
	}
}

func TestSharedExtrasSchedulerFront(t *testing.T) {
	controller, store, _ := newTestController(t)
	cluster := testSeedCluster("42")

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}

	values := helmValues(t, store.object("fleet-default/"+helmop.SharedExtrasName()))
	for path, want := range map[string]any{
		"scheduler.enabled": false,
		"scheduler.service": helmop.SharedClusterNameTemplate + "-control-scheduler",
		"scheduler.port":    int64(helmop.SchedulerPort),
		"vpa.enabled":       true,
	} {
		keys := strings.Split(path, ".")
		if got := valueAtPath(t, values, keys...); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
	for _, component := range []string{"keydb", "manager", "control", "data"} {
		if got := valueAtPath(t, values, "vpa", "targets", component, "enabled"); got != true {
			t.Errorf("%s VPA enabled = %v, want true", component, got)
		}
	}
	if got := valueAtPath(t, values, "vpa", "targets", "control", "name"); got != helmop.SharedClusterNameTemplate+"-control-scheduler" {
		t.Errorf("control VPA target = %v", got)
	}
	if got := valueAtPath(t, values, "vpa", "targets", "data", "name"); got != helmop.SharedClusterNameTemplate+"-data-seed-client" {
		t.Errorf("data VPA target = %v", got)
	}
}

func TestSharedExtrasVPADisabled(t *testing.T) {
	controller, store, _ := newTestController(t)
	controller.opts.VPAEnabled = false
	cluster := testSeedCluster("42")

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}

	extras := store.object("fleet-default/" + helmop.SharedExtrasName())
	if extras == nil {
		t.Fatal("shared extras HelmOp was not created")
	}
	if got := valueAtPath(t, helmValues(t, extras), "vpa", "enabled"); got != false {
		t.Errorf("vpa.enabled = %v, want false", got)
	}
}

func TestSharedExtrasTraefikOptions(t *testing.T) {
	controller, store, _ := newTestController(t)
	controller.opts.ExtrasIngressController = "traefik"
	controller.opts.ExtrasIngressClassName = "traefik"
	controller.opts.ExtrasIngressGRPCEnabled = false
	controller.opts.ExtrasIngressRESTEnabled = true
	controller.opts.ExtrasIngressTLSSecret = "dragonfly-p2p-mtls"
	controller.opts.ExtrasTraefikTransport = true
	cluster := testSeedCluster("42")

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}

	values := helmValues(t, store.object("fleet-default/"+helmop.SharedExtrasName()))
	for path, want := range map[string]any{
		"controller":                       "traefik",
		"className":                        "traefik",
		"grpc.enabled":                     false,
		"rest.enabled":                     true,
		"tls.enabled":                      true,
		"tls.secretName":                   "dragonfly-p2p-mtls",
		"traefik.serversTransport.enabled": true,
		"traefik.serversTransport.insecureSkipVerify": true,
	} {
		keys := strings.Split(path, ".")
		got := valueAtPath(t, values, append([]string{"ingress"}, keys...)...)
		if got != want {
			t.Errorf("ingress.%s = %v, want %v", path, got, want)
		}
	}
}

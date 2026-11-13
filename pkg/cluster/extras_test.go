package cluster

import (
	"testing"
)

func TestReconcileManagerExtrasIngress(t *testing.T) {
	controller, store, _ := newTestController(t)
	cluster := testSeedCluster("")
	delete(cluster.Annotations, AnnotationSeed)
	cluster.Annotations[AnnotationManager] = "true"

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}

	extras := store.object("fleet-default/c1-extras")
	if extras == nil {
		t.Fatal("extras HelmOp was not created")
	}
	if got := dependsOn(extras); len(got) != 1 || got[0] != "c1-manager" {
		t.Errorf("extras dependsOn = %v, want [c1-manager]", got)
	}

	values := helmValues(t, extras)
	if got := valueAtPath(t, values, "ingress", "enabled"); got != true {
		t.Errorf("ingress.enabled = %v", got)
	}
	if got := valueAtPath(t, values, "ingress", "grpc", "host"); got != "grpc.example.com" {
		t.Errorf("ingress.grpc.host = %v", got)
	}
	if got := valueAtPath(t, values, "ingress", "grpc", "service"); got != "c1-manager" {
		t.Errorf("ingress.grpc.service = %v", got)
	}
	if got := valueAtPath(t, values, "ingress", "grpc", "port"); got != int64(65003) {
		t.Errorf("ingress.grpc.port = %v", got)
	}
	if got := valueAtPath(t, values, "ingress", "rest", "host"); got != "api.example.com" {
		t.Errorf("ingress.rest.host = %v", got)
	}
	if got := valueAtPath(t, values, "ingress", "rest", "service"); got != "c1-manager" {
		t.Errorf("ingress.rest.service = %v", got)
	}
	if got := valueAtPath(t, values, "ingress", "rest", "port"); got != int64(8080) {
		t.Errorf("ingress.rest.port = %v", got)
	}
	if got := valueAtPath(t, values, "ingress", "mtlsSecret"); got != "dragonfly-p2p-mtls" {
		t.Errorf("ingress.mtlsSecret = %v", got)
	}
	if got := valueAtPath(t, values, "vpa", "enabled"); got != true {
		t.Errorf("vpa.enabled = %v", got)
	}
}

func TestReconcileSeedOnlyExtrasVPA(t *testing.T) {
	controller, store, _ := newTestController(t)
	cluster := testSeedCluster("42")

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}

	extras := store.object("fleet-default/c1-extras")
	if extras == nil {
		t.Fatal("extras HelmOp was not created")
	}
	if got := dependsOn(extras); len(got) != 0 {
		t.Errorf("seed-only extras dependsOn = %v, want none", got)
	}

	values := helmValues(t, extras)
	if got := valueAtPath(t, values, "ingress", "enabled"); got != false {
		t.Errorf("seed-only ingress.enabled = %v", got)
	}
	for component, want := range map[string]bool{
		"keydb": true, "manager": false, "control": true, "data": true, "nydus": true,
	} {
		if got := valueAtPath(t, values, "vpa", "targets", component, "enabled"); got != want {
			t.Errorf("%s VPA enabled = %v, want %v", component, got, want)
		}
	}
}

func TestReconcileLabelOnlyExtrasVPA(t *testing.T) {
	controller, store, _ := newTestController(t)
	cluster := testSeedCluster("")
	cluster.Annotations = nil

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}

	extras := store.object("fleet-default/c1-extras")
	if extras == nil {
		t.Fatal("extras HelmOp was not created")
	}
	values := helmValues(t, extras)
	if got := valueAtPath(t, values, "ingress", "enabled"); got != false {
		t.Errorf("label-only ingress.enabled = %v", got)
	}
	if got := valueAtPath(t, values, "vpa", "targets", "keydb", "enabled"); got != true {
		t.Errorf("keydb VPA enabled = %v", got)
	}
	for _, component := range []string{"manager", "control", "data", "nydus"} {
		if got := valueAtPath(t, values, "vpa", "targets", component, "enabled"); got != false {
			t.Errorf("%s VPA enabled = %v, want false", component, got)
		}
	}
}

func TestReconcileExtrasVPADisabled(t *testing.T) {
	controller, store, _ := newTestController(t)
	controller.opts.VPAEnabled = false
	cluster := testSeedCluster("42")

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}

	extras := store.object("fleet-default/c1-extras")
	if extras == nil {
		t.Fatal("extras HelmOp was not created")
	}
	if got := valueAtPath(t, helmValues(t, extras), "vpa", "enabled"); got != false {
		t.Errorf("vpa.enabled = %v, want false", got)
	}
}

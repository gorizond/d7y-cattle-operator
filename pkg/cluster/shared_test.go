package cluster

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
)

func legacyHelmOp(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "fleet.cattle.io/v1alpha1",
		"kind":       "HelmOp",
		"metadata": map[string]any{
			"name":      name,
			"namespace": "fleet-default",
		},
	}}
}

func TestReconcileCleansLegacyPerClusterHelmOps(t *testing.T) {
	controller, store, _ := newTestController(t)
	bundles := controller.bundles.(*fakeHelmOpStore)
	legacy := []string{
		"c1-keydb", "c1-control", "c1-data", "c1-extras",
		"c1-containerd-mirror", "c1-admission-webhook",
	}
	for _, name := range legacy {
		store.objects["fleet-default/"+name] = legacyHelmOp(name)
		bundles.objects["fleet-default/"+name] = fleetBundle(name, "fleet-default")
	}
	bundles.objects["fleet-default/unrelated"] = fleetBundle("unrelated", "fleet-default")

	cluster := testSeedCluster("42")
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}

	for _, name := range legacy {
		if store.has("fleet-default/" + name) {
			t.Errorf("legacy HelmOp %s was not deleted", name)
		}
		if bundles.has("fleet-default/" + name) {
			t.Errorf("legacy Bundle %s was not deleted", name)
		}
	}
	if !bundles.has("fleet-default/unrelated") {
		t.Error("unrelated Bundle was deleted")
	}
	for _, name := range []string{
		helmop.SharedKeyDBName(), helmop.SharedControlName(), helmop.SharedDataName(),
		helmop.SharedExtrasName(), helmop.SharedMirrorName(), helmop.SharedAdmissionName(),
	} {
		if !store.has("fleet-default/" + name) {
			t.Errorf("%s shared HelmOp is missing", name)
		}
	}
}

func TestSharedKeyDBTargetsEveryEnabledCluster(t *testing.T) {
	controller, store, _ := newTestController(t)
	cluster := testSeedCluster("42")
	delete(cluster.Annotations, AnnotationSeed)
	cluster.Annotations[AnnotationManager] = "true"

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	labels := selectorLabels(store.object("fleet-default/" + helmop.SharedKeyDBName()))
	if len(labels) != 1 || labels[helmop.LabelGate] != "true" {
		t.Errorf("keydb selector = %v, want the gate label only", labels)
	}
}

func TestProxyDomainMissingKeepsLegacyAdmission(t *testing.T) {
	controller, store, _ := newTestController(t)
	controller.opts.ProxyDomain = ""
	store.objects["fleet-default/c1-admission-webhook"] = legacyHelmOp("c1-admission-webhook")
	cluster := admissionCluster()

	// The admission role reports the missing precondition, so reconcile fails.
	if _, err := controller.reconcile("", cluster); err == nil {
		t.Fatal("reconcile() error = nil, want the admission proxy.domain error")
	}
	if !store.has("fleet-default/c1-admission-webhook") {
		t.Error("legacy admission HelmOp was deleted without a shared replacement")
	}
	if store.has("fleet-default/" + helmop.SharedAdmissionName()) {
		t.Error("shared admission HelmOp was created without proxy.domain")
	}
}

package cluster

import (
	"strings"
	"testing"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
		"c1-keydb", "c1-manager", "c1-control", "c1-data", "c1-extras",
		"c1-containerd-mirror", "c1-admission-webhook", "c1-registry-proxy",
	}
	for _, name := range legacy {
		store.objects["fleet-default/"+name] = legacyHelmOp(name)
		bundles.objects["fleet-default/"+name] = fleetBundle(name, "fleet-default")
	}
	bundles.objects["fleet-default/unrelated"] = fleetBundle("unrelated", "fleet-default")

	controller.opts.ProxyHarborURL = "https://harbor.example.com"
	cluster := testSeedCluster("42")
	cluster.Annotations[AnnotationManager] = "true"
	cluster.Annotations[AnnotationProxy] = "true"
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
		helmop.SharedKeyDBName(), helmop.SharedManagerName(), helmop.SharedControlName(),
		helmop.SharedDataName(), helmop.SharedExtrasName(), helmop.SharedMirrorName(),
		helmop.SharedAdmissionName(), helmop.SharedRegistryProxyName(),
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

func helmTakeOwnership(op *unstructured.Unstructured) bool {
	spec, _ := op.Object["spec"].(map[string]any)
	helm, _ := spec["helm"].(map[string]any)
	value, _ := helm["takeOwnership"].(bool)
	return value
}

func TestReconcileCreatesSharedManager(t *testing.T) {
	controller, store, _ := newTestController(t)
	cluster := testSeedCluster("")
	delete(cluster.Annotations, AnnotationSeed)
	delete(cluster.Annotations, AnnotationSchedulerClusterID)
	cluster.Annotations[AnnotationManager] = "true"

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	op := store.object("fleet-default/" + helmop.SharedManagerName())
	if op == nil {
		t.Fatal("shared manager HelmOp was not created")
	}
	if labels := selectorLabels(op); labels[helmop.LabelGate] != "true" || labels[helmop.LabelRoleManager] != "true" {
		t.Errorf("manager selector = %v, want gate+manager", labels)
	}
	if !helmTakeOwnership(op) {
		t.Error("shared manager takeOwnership = false, want true")
	}
	values := helmValues(t, op)
	if got := valueAtPath(t, values, "fullnameOverride"); got != helmop.SharedClusterNameTemplate {
		t.Errorf("manager fullnameOverride = %v, want %s", got, helmop.SharedClusterNameTemplate)
	}
	addrs, _ := valueAtPath(t, values, "externalRedis", "addrs").([]any)
	wantAddr := helmop.SharedClusterNameTemplate + "-keydb.dragonfly-system.svc.cluster.local:6379"
	if len(addrs) != 1 || addrs[0] != wantAddr {
		t.Errorf("manager externalRedis.addrs = %v, want [%s]", addrs, wantAddr)
	}
	if store.has("fleet-default/c1-manager") {
		t.Error("legacy per-cluster manager HelmOp still exists")
	}
	if !store.has("fleet-default/" + helmop.SharedKeyDBName()) {
		t.Error("shared KeyDB HelmOp is missing")
	}
}

func TestSharedManagerNotCreatedWithoutRole(t *testing.T) {
	controller, store, _ := newTestController(t)
	cluster := testSeedCluster("")
	delete(cluster.Annotations, AnnotationSeed)
	delete(cluster.Annotations, AnnotationSchedulerClusterID)

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	if store.has("fleet-default/" + helmop.SharedManagerName()) {
		t.Error("shared manager HelmOp was created without the manager role")
	}
}

func testProxyCluster() *provv1.Cluster {
	cluster := testSeedCluster("")
	delete(cluster.Annotations, AnnotationSeed)
	cluster.Annotations[AnnotationProxy] = "true"
	return cluster
}

func TestReconcileCreatesSharedRegistryProxy(t *testing.T) {
	controller, store, _ := newTestController(t)
	controller.opts.ProxyHarborURL = "https://harbor.example.com"
	cluster := testProxyCluster()

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	op := store.object("fleet-default/" + helmop.SharedRegistryProxyName())
	if op == nil {
		t.Fatal("shared registry proxy HelmOp was not created")
	}
	if labels := selectorLabels(op); labels[helmop.LabelGate] != "true" || labels[helmop.LabelRoleProxy] != "true" {
		t.Errorf("proxy selector = %v, want gate+proxy", labels)
	}
	if !helmTakeOwnership(op) {
		t.Error("shared registry proxy takeOwnership = false, want true")
	}
	values := helmValues(t, op)
	if got := valueAtPath(t, values, "fullnameOverride"); got != helmop.SharedClusterNameTemplate {
		t.Errorf("fullnameOverride = %v, want %s", got, helmop.SharedClusterNameTemplate)
	}
	if got := valueAtPath(t, values, "ingress", "tlsSecret"); got != helmop.SharedProxyTLSSecretTemplate {
		t.Errorf("ingress.tlsSecret = %v, want the annotation template", got)
	}
	for path, want := range map[string]any{
		"harbor.adminSecret": helmop.SharedHarborAdminSecretName(),
		"harbor.robotSecret": helmop.SharedRegistryProxyRobotSecretName(),
		"webhook.secretName": helmop.SharedRegistryProxyWebhookSecretName(),
	} {
		keys := strings.Split(path, ".")
		if got := valueAtPath(t, values, keys...); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
	for _, secret := range []string{
		helmop.SharedHarborAdminSecretName(),
		helmop.SharedRegistryProxyWebhookSecretName(),
		helmop.SharedRegistryProxyRobotSecretName(),
	} {
		if _, err := controller.secrets.(*fakeSecretStore).Get("fleet-default", secret, metav1.GetOptions{}); err != nil {
			t.Errorf("fleet-wide Secret %s was not created: %v", secret, err)
		}
	}
	if store.has("fleet-default/" + helmop.RegistryProxyName(cluster.Name)) {
		t.Error("legacy per-cluster registry proxy HelmOp still exists")
	}
}

func TestProxyTLSSecretAnnotationNotOverwritten(t *testing.T) {
	controller, _, _ := newTestController(t)
	controller.opts.ProxyHarborURL = "https://harbor.example.com"
	cluster := testProxyCluster()
	cluster.Annotations[helmop.AnnotationProxyTLSSecret] = "custom-proxy-tls"
	clusterStore := controller.clusters.(*fakeClusterStore)
	clusterStore.set(cluster)

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	if got := clusterStore.annotation(helmop.AnnotationProxyTLSSecret); got != "custom-proxy-tls" {
		t.Errorf("proxy-tls-secret annotation = %q, want the pinned value", got)
	}
}

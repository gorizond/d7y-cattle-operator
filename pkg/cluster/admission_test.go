package cluster

import (
	"testing"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
)

func admissionCluster() *provv1.Cluster {
	cluster := testSeedCluster("42")
	delete(cluster.Annotations, AnnotationSeed)
	delete(cluster.Annotations, AnnotationSchedulerClusterID)
	cluster.Annotations[AnnotationAdmission] = "true"
	return cluster
}

func TestReconcileAdmissionToggleMatrix(t *testing.T) {
	tests := []struct {
		name       string
		harbor     bool
		proxy      bool
		admission  bool
		wantHarbor bool
		wantProxy  bool
	}{
		{name: "none"},
		{name: "admission only", admission: true},
		{name: "harbor only", harbor: true, wantHarbor: true},
		{name: "proxy only", proxy: true, wantProxy: true},
		{name: "harbor and proxy", harbor: true, proxy: true, wantHarbor: true, wantProxy: true},
		{name: "harbor and admission", harbor: true, admission: true, wantHarbor: true},
		{name: "proxy and admission", proxy: true, admission: true, wantProxy: true},
		{name: "all toggles", harbor: true, proxy: true, admission: true, wantHarbor: true, wantProxy: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			controller, store, _ := newTestController(t)
			if test.proxy && !test.harbor {
				// External Harbor makes the proxy toggle standalone.
				controller.opts.ProxyHarborURL = "https://external.example.com"
			}
			cluster := testSeedCluster("42")
			delete(cluster.Annotations, AnnotationSeed)
			delete(cluster.Annotations, AnnotationSchedulerClusterID)
			if test.harbor {
				cluster.Annotations[AnnotationHarbor] = "true"
				cluster.Annotations[AnnotationHarborDomain] = "harbor.example.com"
			}
			if test.proxy {
				cluster.Annotations[AnnotationProxy] = "true"
			}
			if test.admission {
				cluster.Annotations[AnnotationAdmission] = "true"
			}

			if _, err := controller.reconcile("", cluster); err != nil {
				t.Fatalf("reconcile() error = %v", err)
			}
			for name, want := range map[string]bool{
				"c1-harbor":                      test.wantHarbor,
				helmop.SharedRegistryProxyName(): test.wantProxy,
			} {
				if got := store.has("fleet-default/" + name); got != want {
					t.Errorf("%s present = %t, want %t", name, got, want)
				}
			}
			// Admission is a shared HelmOp: it exists for every enabled Cluster
			// regardless of this Cluster's admission role, and the per-cluster
			// copy is always cleaned up.
			if !store.has("fleet-default/" + helmop.SharedAdmissionName()) {
				t.Error("shared admission HelmOp was not created")
			}
			if store.has("fleet-default/c1-admission-webhook") {
				t.Error("legacy per-cluster admission HelmOp still exists")
			}
			op := store.object("fleet-default/" + helmop.SharedAdmissionName())
			if got := selectorLabels(op); got[helmop.LabelGate] != "true" || got[helmop.LabelRoleAdmission] != "true" {
				t.Errorf("admission selector = %v, want gate+admission", got)
			}
			if got := valueAtPath(t, helmValues(t, op), "proxy", "domain"); got != "proxy.example.com" {
				t.Errorf("admission proxy.domain = %v", got)
			}
		})
	}
}

func TestReconcileAdmissionToggleKeepsSharedHelmOp(t *testing.T) {
	controller, store, _ := newTestController(t)
	cluster := admissionCluster()
	cluster.Annotations[AnnotationHarbor] = "true"
	cluster.Annotations[AnnotationHarborDomain] = "harbor.example.com"
	cluster.Annotations[AnnotationProxy] = "true"
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}

	delete(cluster.Annotations, AnnotationAdmission)
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile(remove admission) error = %v", err)
	}
	if !store.has("fleet-default/" + helmop.SharedAdmissionName()) {
		t.Error("shared admission HelmOp was removed by the per-cluster toggle")
	}
	if store.has("fleet-default/c1-admission-webhook") {
		t.Error("legacy per-cluster admission HelmOp still exists")
	}
	for _, name := range []string{"c1-harbor", helmop.SharedRegistryProxyName()} {
		if !store.has("fleet-default/" + name) {
			t.Errorf("%s was removed with admission", name)
		}
	}
}

func TestReconcileAdmissionCondition(t *testing.T) {
	controller, store, _ := newTestController(t)
	clusterStore := controller.clusters.(*fakeClusterStore)
	cluster := admissionCluster()
	clusterStore.set(cluster)

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	if got := clusterStore.condition(string(conditionAdmissionWebhookReady)); got == nil ||
		got.Status != corev1.ConditionFalse || got.Reason != "Progressing" {
		t.Errorf("AdmissionWebhookReady before HelmOp status = %+v, want False/Progressing", got)
	}

	op := store.object("fleet-default/" + helmop.SharedAdmissionName())
	if err := unstructured.SetNestedSlice(op.Object, []any{
		map[string]any{"type": "Ready", "status": "True", "message": "Ready"},
	}, "status", "conditions"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile(ready HelmOp) error = %v", err)
	}
	if got := clusterStore.condition(string(conditionAdmissionWebhookReady)); got == nil ||
		got.Status != corev1.ConditionTrue || got.Reason != "Ready" {
		t.Errorf("AdmissionWebhookReady after HelmOp Ready = %+v, want True/Ready", got)
	}
}

func TestReconcileAdmissionProxyDomainMissingDoesNotBlockOthers(t *testing.T) {
	controller, store, events := newTestController(t)
	controller.opts.ProxyDomain = ""
	cluster := admissionCluster()
	cluster.Annotations[AnnotationHarbor] = "true"
	cluster.Annotations[AnnotationHarborDomain] = "harbor.example.com"
	cluster.Annotations[AnnotationProxy] = "true"

	if _, err := controller.reconcile("", cluster); err == nil {
		t.Fatal("reconcile() error = nil, want the admission proxy.domain error")
	}
	if store.has("fleet-default/" + helmop.SharedAdmissionName()) {
		t.Error("shared admission HelmOp was created without proxy.domain")
	}
	// Harbor has no dependency on the proxy domain and must still be applied.
	if !store.has("fleet-default/c1-harbor") {
		t.Error("Harbor was blocked by the admission failure")
	}
	// The proxy shares the same operator-wide proxy.domain, so it reports its
	// own precondition failure; admission must not change that behavior.
	if store.has("fleet-default/" + helmop.SharedRegistryProxyName()) {
		t.Error("registry proxy was applied without proxy.domain")
	}
	found := false
	for _, event := range events.events {
		if event.Reason == "AdmissionProxyDomainMissing" {
			found = true
		}
	}
	if !found {
		t.Errorf("events = %+v, want AdmissionProxyDomainMissing", events.events)
	}
}

func TestAdmissionHelmOpUsesIndependentChart(t *testing.T) {
	controller, _, _ := newTestController(t)
	controller.opts.AdmissionChart = helmop.Chart{Repo: "https://charts.example.com", Name: "admission-webhook", Version: "9.9.9"}
	cluster := admissionCluster()
	hc := controller.helmopCluster(cluster)
	if !hc.AdmissionEnabled {
		t.Error("AdmissionEnabled = false")
	}
	if hc.AdmissionChart.Name != "admission-webhook" || hc.AdmissionChart.Version != "9.9.9" {
		t.Errorf("AdmissionChart = %+v", hc.AdmissionChart)
	}
	if hc.ProxyDomain != "proxy.example.com" {
		t.Errorf("ProxyDomain = %q", hc.ProxyDomain)
	}
}

func TestSharedAdmissionImageFromOperatorOptions(t *testing.T) {
	controller, store, _ := newTestController(t)
	controller.opts.OperatorImageRepository = "ghcr.io/gorizond/d7y-cattle-operator"
	controller.opts.OperatorImageTag = "v0.1.43"
	cluster := admissionCluster()

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	values := helmValues(t, store.object("fleet-default/"+helmop.SharedAdmissionName()))
	if got := valueAtPath(t, values, "image", "repository"); got != "ghcr.io/gorizond/d7y-cattle-operator" {
		t.Errorf("image.repository = %v", got)
	}
	if got := valueAtPath(t, values, "image", "tag"); got != "v0.1.43" {
		t.Errorf("image.tag = %v", got)
	}
}

func fleetBundle(name, namespace string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "fleet.cattle.io/v1alpha1",
		"kind":       "Bundle",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
			"labels":    map[string]any{"fleet.cattle.io/fleet-helm-name": name},
		},
	}}
}

func TestReconcileAdmissionCleanupDeletesLegacyBundle(t *testing.T) {
	controller, _, _ := newTestController(t)
	cluster := admissionCluster()
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	bundles := controller.bundles.(*fakeHelmOpStore)
	bundles.objects["fleet-default/c1-admission-webhook"] = fleetBundle("c1-admission-webhook", "fleet-default")
	bundles.objects["fleet-default/c1-harbor"] = fleetBundle("c1-harbor", "fleet-default")

	delete(cluster.Annotations, AnnotationAdmission)
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile(remove admission) error = %v", err)
	}
	if bundles.has("fleet-default/c1-admission-webhook") {
		t.Error("legacy admission Bundle was not removed")
	}
	if !bundles.has("fleet-default/c1-harbor") {
		t.Error("unrelated Bundle was removed with admission cleanup")
	}
}

func TestReconcileDisableClusterDeletesLegacyAdmissionBundle(t *testing.T) {
	controller, store, _ := newTestController(t)
	cluster := admissionCluster()
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	bundles := controller.bundles.(*fakeHelmOpStore)
	bundles.objects["fleet-default/c1-admission-webhook"] = fleetBundle("c1-admission-webhook", "fleet-default")

	delete(cluster.Labels, LabelEnabled)
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile(disable cluster) error = %v", err)
	}
	if store.has("fleet-default/c1-admission-webhook") {
		t.Error("legacy admission HelmOp was not removed by cleanupAll")
	}
	if bundles.has("fleet-default/c1-admission-webhook") {
		t.Error("legacy admission Bundle was not removed by cleanupAll")
	}
}

func TestReconcileAdmissionCleanupLeavesUnlabeledBundle(t *testing.T) {
	controller, _, _ := newTestController(t)
	cluster := admissionCluster()
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	bundles := controller.bundles.(*fakeHelmOpStore)
	foreign := fleetBundle("c1-admission-webhook", "fleet-default")
	delete(foreign.Object["metadata"].(map[string]any)["labels"].(map[string]any), "fleet.cattle.io/fleet-helm-name")
	bundles.objects["fleet-default/c1-admission-webhook"] = foreign

	delete(cluster.Annotations, AnnotationAdmission)
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile(remove admission) error = %v", err)
	}
	if !bundles.has("fleet-default/c1-admission-webhook") {
		t.Error("same-named Bundle without the HelmOp label was deleted")
	}
}

// TestSharedAdmissionIgnoresClusterExcludeList documents a deliberate
// limitation: the excluded-namespace list is a per-Cluster annotation and Fleet
// values templates cannot render a YAML list, so the shared release keeps the
// chart's system-namespace exclusions only.
func TestSharedAdmissionIgnoresClusterExcludeList(t *testing.T) {
	controller, store, _ := newTestController(t)
	cluster := admissionCluster()
	cluster.Annotations[AnnotationAdmissionExclude] = " ns-a, ns-b "
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	raw, ok := valueAtPath(t, helmValues(t, store.object("fleet-default/"+helmop.SharedAdmissionName())),
		"namespaceSelector", "excludedNamespaces").([]any)
	if !ok {
		t.Fatalf("namespaceSelector.excludedNamespaces = %T, want []any",
			valueAtPath(t, helmValues(t, store.object("fleet-default/"+helmop.SharedAdmissionName())), "namespaceSelector", "excludedNamespaces"))
	}
	if len(raw) != 0 {
		t.Errorf("namespaceSelector.excludedNamespaces = %v, want empty for the shared release", raw)
	}
}

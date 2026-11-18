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
		wantAdm    bool
	}{
		{name: "none"},
		{name: "admission only", admission: true, wantAdm: true},
		{name: "harbor only", harbor: true, wantHarbor: true},
		{name: "proxy only", proxy: true, wantProxy: true},
		{name: "harbor and proxy", harbor: true, proxy: true, wantHarbor: true, wantProxy: true},
		{name: "harbor and admission", harbor: true, admission: true, wantHarbor: true, wantAdm: true},
		{name: "proxy and admission", proxy: true, admission: true, wantProxy: true, wantAdm: true},
		{name: "all toggles", harbor: true, proxy: true, admission: true, wantHarbor: true, wantProxy: true, wantAdm: true},
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
				"c1-harbor":            test.wantHarbor,
				"c1-registry-proxy":    test.wantProxy,
				"c1-admission-webhook": test.wantAdm,
			} {
				if got := store.has("fleet-default/" + name); got != want {
					t.Errorf("%s present = %t, want %t", name, got, want)
				}
			}
			if test.wantAdm {
				op := store.object("fleet-default/c1-admission-webhook")
				if len(dependsOn(op)) != 0 {
					t.Errorf("admission dependsOn = %v, want none", dependsOn(op))
				}
				if got := valueAtPath(t, helmValues(t, op), "proxy", "domain"); got != "proxy.example.com" {
					t.Errorf("admission proxy.domain = %v", got)
				}
			}
		})
	}
}

func TestReconcileAdmissionIsRemovedIndependently(t *testing.T) {
	controller, store, _ := newTestController(t)
	cluster := admissionCluster()
	cluster.Annotations[AnnotationHarbor] = "true"
	cluster.Annotations[AnnotationHarborDomain] = "harbor.example.com"
	cluster.Annotations[AnnotationProxy] = "true"
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	if !store.has("fleet-default/c1-admission-webhook") {
		t.Fatal("admission HelmOp was not created")
	}

	delete(cluster.Annotations, AnnotationAdmission)
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile(remove admission) error = %v", err)
	}
	if store.has("fleet-default/c1-admission-webhook") {
		t.Error("admission HelmOp was not removed")
	}
	for _, name := range []string{"c1-harbor", "c1-registry-proxy"} {
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

	op := store.object("fleet-default/c1-admission-webhook")
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
	if store.has("fleet-default/c1-admission-webhook") {
		t.Error("admission HelmOp was created without proxy.domain")
	}
	// Harbor has no dependency on the proxy domain and must still be applied.
	if !store.has("fleet-default/c1-harbor") {
		t.Error("Harbor was blocked by the admission failure")
	}
	// The proxy shares the same operator-wide proxy.domain, so it reports its
	// own precondition failure; admission must not change that behavior.
	if store.has("fleet-default/c1-registry-proxy") {
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

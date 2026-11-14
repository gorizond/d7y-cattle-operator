package cluster

import (
	"errors"
	"strings"
	"testing"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	"github.com/rancher/wrangler/v3/pkg/condition"
	"github.com/rancher/wrangler/v3/pkg/genericcondition"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/gorizond/d7y-cattle-operator/pkg/managerapi"
)

func TestReconcileSetsRoleConditions(t *testing.T) {
	controller, _, _ := newTestController(t)
	controller.opts.NydusEnabled = true
	clusterStore := controller.clusters.(*fakeClusterStore)
	clusterStore.set(testSeedCluster("42"))

	if _, err := controller.reconcile("", testSeedCluster("42")); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}

	for _, name := range []string{
		string(conditionRolesResolved), string(conditionKeyDBReady),
		string(conditionControlReady), string(conditionDataReady),
		string(conditionNydusReady), string(conditionRegistered),
	} {
		if got := clusterStore.condition(name); got == nil || got.Status != corev1.ConditionTrue {
			t.Errorf("%s condition = %+v, want True", name, got)
		}
	}
	if clusterStore.statusUpdates != 1 {
		t.Errorf("status updates = %d, want 1", clusterStore.statusUpdates)
	}

	stored := clusterStore.get()
	before := clusterStore.statusUpdates
	if _, err := controller.reconcile("", stored); err != nil {
		t.Fatalf("second reconcile() error = %v", err)
	}
	if clusterStore.statusUpdates != before {
		t.Errorf("status updates = %d after unchanged reconcile, want %d", clusterStore.statusUpdates, before)
	}
}

func TestReconcileLabelOnlyClusterConditions(t *testing.T) {
	controller, _, _ := newTestController(t)
	clusterStore := controller.clusters.(*fakeClusterStore)
	cluster := &provv1.Cluster{ObjectMeta: metav1.ObjectMeta{
		Name:      "c1",
		Namespace: "fleet-default",
		Labels:    map[string]string{LabelEnabled: "true"},
	}}
	clusterStore.set(cluster)

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}

	if got := clusterStore.condition(string(conditionRolesResolved)); got == nil || got.Status != corev1.ConditionTrue {
		t.Errorf("RolesResolved = %+v, want True", got)
	}
	if got := clusterStore.condition(string(conditionKeyDBReady)); got == nil || got.Status != corev1.ConditionTrue {
		t.Errorf("KeyDBReady = %+v, want True", got)
	}
	for _, name := range []string{
		string(conditionControlReady), string(conditionDataReady), string(conditionNydusReady),
	} {
		if got := clusterStore.condition(name); got == nil || got.Status != corev1.ConditionFalse {
			t.Errorf("%s = %+v, want False", name, got)
		}
		if got := clusterStore.condition(name); got != nil && got.Reason != "SeedDisabled" {
			t.Errorf("%s reason = %q, want SeedDisabled", name, got.Reason)
		}
	}
}

func TestUnlabeledClusterHasNoStatus(t *testing.T) {
	controller, _, _ := newTestController(t)
	clusterStore := controller.clusters.(*fakeClusterStore)
	cluster := &provv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "fleet-default"}}
	clusterStore.set(cluster)

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	if clusterStore.statusUpdates != 0 {
		t.Errorf("status updates = %d, want 0", clusterStore.statusUpdates)
	}
	if got := clusterStore.condition(string(conditionRolesResolved)); got != nil {
		t.Errorf("RolesResolved = %+v, want nil", got)
	}
}

func TestSeedDisabledClearsSeedConditions(t *testing.T) {
	controller, _, _ := newTestController(t)
	clusterStore := controller.clusters.(*fakeClusterStore)
	clusterStore.set(testSeedCluster("42"))
	if _, err := controller.reconcile("", testSeedCluster("42")); err != nil {
		t.Fatalf("reconcile(seed) error = %v", err)
	}

	stored := clusterStore.get()
	delete(stored.Annotations, AnnotationSeed)
	if _, err := controller.reconcile("", stored); err != nil {
		t.Fatalf("reconcile(seed removed) error = %v", err)
	}

	for _, name := range []string{
		string(conditionControlReady), string(conditionDataReady), string(conditionNydusReady),
	} {
		if got := clusterStore.condition(name); got == nil || got.Status != corev1.ConditionFalse || got.Reason != "SeedDisabled" {
			t.Errorf("%s = %+v, want False/SeedDisabled", name, got)
		}
	}
	if got := clusterStore.condition(string(conditionRegistered)); got == nil || got.Status != corev1.ConditionFalse || got.Reason != "SeedDisabled" {
		t.Errorf("Registered = %+v, want False/SeedDisabled", got)
	}
}

func TestUnsupportedChartVersionEvent(t *testing.T) {
	controller, _, events := newTestController(t)
	controller.opts.DragonflyChart.Version = "9.9.9"
	cluster := &provv1.Cluster{ObjectMeta: metav1.ObjectMeta{
		Name:      "c1",
		Namespace: "fleet-default",
		Labels:    map[string]string{LabelEnabled: "true"},
	}}

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	found := false
	for _, event := range events.events {
		if event.Reason == "UnsupportedChartVersion" {
			found = true
		}
	}
	if !found {
		t.Errorf("events = %+v, want UnsupportedChartVersion", events.events)
	}
}

func TestRegistrationFailureSetsSeedConditions(t *testing.T) {
	controller, _, _ := newTestController(t)
	controller.registrar.(*fakeRegistrar).err = managerapi.ErrUnauthorized
	clusterStore := controller.clusters.(*fakeClusterStore)
	cluster := testSeedCluster("42")
	clusterStore.set(cluster)

	if _, err := controller.reconcile("", cluster); err == nil {
		t.Fatal("reconcile() error = nil, want registration failure")
	}

	if got := clusterStore.condition(string(conditionKeyDBReady)); got == nil || got.Status != corev1.ConditionTrue {
		t.Errorf("KeyDBReady = %+v, want True", got)
	}
	for _, name := range []string{
		string(conditionControlReady), string(conditionDataReady), string(conditionNydusReady),
	} {
		if got := clusterStore.condition(name); got == nil || got.Status != corev1.ConditionFalse || got.Reason != "RegistrationFailed" {
			t.Errorf("%s = %+v, want False/RegistrationFailed", name, got)
		}
	}
	if got := clusterStore.condition(string(conditionRegistrationFailed)); got == nil || got.Status != corev1.ConditionTrue {
		t.Errorf("RegistrationFailed = %+v, want True", got)
	}
}

func TestStatusUpdatePreservesRancherConditions(t *testing.T) {
	controller, _, _ := newTestController(t)
	clusterStore := controller.clusters.(*fakeClusterStore)
	cluster := &provv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "c1",
			Namespace: "fleet-default",
			Labels:    map[string]string{LabelEnabled: "true"},
		},
		Status: provv1.ClusterStatus{Conditions: []genericcondition.GenericCondition{{
			Type:    "rancher.io/Provisioned",
			Status:  corev1.ConditionTrue,
			Reason:  "Provisioned",
			Message: "managed by Rancher",
		}}},
	}
	clusterStore.set(cluster)

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}

	got := clusterStore.condition("rancher.io/Provisioned")
	if got == nil || got.Status != corev1.ConditionTrue || got.Reason != "Provisioned" {
		t.Errorf("Rancher condition = %+v, want preserved True/Provisioned", got)
	}
	if got := clusterStore.condition(string(conditionKeyDBReady)); got == nil || got.Status != corev1.ConditionTrue {
		t.Errorf("operator KeyDBReady = %+v, want True", got)
	}
}

func TestStatusUpdateErrorIsReturned(t *testing.T) {
	controller, _, _ := newTestController(t)
	clusterStore := controller.clusters.(*fakeClusterStore)
	statusErr := errors.New("status conflict")
	clusterStore.statusErr = statusErr
	cluster := &provv1.Cluster{ObjectMeta: metav1.ObjectMeta{
		Name:      "c1",
		Namespace: "fleet-default",
		Labels:    map[string]string{LabelEnabled: "true"},
	}}
	clusterStore.set(cluster)

	if _, err := controller.reconcile("", cluster); !errors.Is(err, statusErr) {
		t.Fatalf("reconcile() error = %v, want wrapped status error", err)
	}
	if clusterStore.statusUpdates == 0 {
		t.Error("UpdateStatus was not attempted")
	}
}

func TestOperatorConditionTypesAreNamespaced(t *testing.T) {
	for _, cond := range []condition.Cond{
		conditionRolesResolved, conditionKeyDBReady, conditionControlReady,
		conditionDataReady, conditionNydusReady, conditionRegistered,
		conditionRegistrationFailed, conditionRetrying, conditionIDMismatch,
	} {
		if !strings.HasPrefix(string(cond), "d7y.io/") {
			t.Errorf("condition type %q is not in the d7y.io/ namespace", cond)
		}
	}
}

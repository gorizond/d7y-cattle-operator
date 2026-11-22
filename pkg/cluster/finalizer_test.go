package cluster

import (
	"errors"
	"testing"
	"time"

	fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
)

func TestReconcileAddsFinalizerAndCleansPKIOnDeletion(t *testing.T) {
	controller, store, _ := newTestController(t)
	cluster := testSeedCluster("42")
	clusterStore := controller.clusters.(*fakeClusterStore)
	clusterStore.set(cluster)

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	if !hasClusterFinalizer(clusterStore.get()) {
		t.Fatalf("finalizers = %v, want %s", clusterStore.get().Finalizers, clusterFinalizer)
	}
	if !store.has("fleet-default/" + helmop.SharedKeyDBName()) {
		t.Error("normal reconcile did not create the shared KeyDB HelmOp")
	}
	if !store.has("fleet-default/" + helmop.HostSyncName("c1")) {
		t.Error("normal reconcile did not create the host-sync HelmOp")
	}

	schedulerName := helmop.SchedulerSourceSecretName("c1")
	secret, err := controller.secrets.Get("dragonfly-system", schedulerName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("scheduler Secret was not created: %v", err)
	}
	if len(secret.OwnerReferences) != 0 {
		t.Errorf("scheduler Secret ownerReferences = %+v; cleanup must not rely on a cross-namespace ownerRef", secret.OwnerReferences)
	}
	if !controller.helmops.(*fakeHelmOpStore).has("fleet-default/" + helmop.SchedulerCertName("c1")) {
		t.Error("normal reconcile did not create the scheduler certificate HelmOp")
	}
	if controller.bundles.(*fakeHelmOpStore).has("fleet-default/" + helmop.SchedulerBundleName("c1")) {
		t.Error("normal reconcile created a legacy raw scheduler Bundle")
	}
	certBundle := helmop.SchedulerCertName("c1")
	controller.bundles.(*fakeHelmOpStore).objects["fleet-default/"+certBundle] = &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "fleet.cattle.io/v1alpha1",
		"kind":       "Bundle",
		"metadata": map[string]any{
			"name":      certBundle,
			"namespace": "fleet-default",
			"labels":    map[string]any{fleetv1alpha1.HelmOpLabel: certBundle},
		},
	}}

	deleting := clusterStore.get()
	now := metav1.NewTime(time.Now())
	deleting.DeletionTimestamp = &now
	clusterStore.set(deleting)
	statusBefore := clusterStore.statusUpdates

	if _, err := controller.reconcile("", deleting); err != nil {
		t.Fatalf("reconcile(deleting) error = %v", err)
	}
	if hasClusterFinalizer(clusterStore.get()) {
		t.Errorf("finalizers = %v, finalizer was not removed", clusterStore.get().Finalizers)
	}
	if _, err := controller.secrets.Get("dragonfly-system", schedulerName, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("scheduler Secret still exists after deletion: %v", err)
	}
	if controller.helmops.(*fakeHelmOpStore).has("fleet-default/" + helmop.SchedulerCertName("c1")) {
		t.Error("scheduler certificate HelmOp still exists after deletion")
	}
	if controller.bundles.(*fakeHelmOpStore).has("fleet-default/" + helmop.SchedulerBundleName("c1")) {
		t.Error("legacy scheduler Bundle still exists after deletion")
	}
	if controller.bundles.(*fakeHelmOpStore).has("fleet-default/" + certBundle) {
		t.Error("scheduler certificate Bundle still exists after deletion")
	}
	if store.has("fleet-default/" + helmop.HostSyncName("c1")) {
		t.Error("host-sync HelmOp still exists after deletion")
	}
	if clusterStore.statusUpdates != statusBefore {
		t.Errorf("status updates = %d after deletion, want %d", clusterStore.statusUpdates, statusBefore)
	}
}

func TestReconcileDeletionKeepsFinalizerOnCleanupError(t *testing.T) {
	controller, _, _ := newTestController(t)
	cluster := testSeedCluster("42")
	clusterStore := controller.clusters.(*fakeClusterStore)
	clusterStore.set(cluster)
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}

	deleting := clusterStore.get()
	now := metav1.NewTime(time.Now())
	deleting.DeletionTimestamp = &now
	clusterStore.set(deleting)

	secrets := controller.secrets.(*fakeSecretStore)
	secrets.deleteErr = errors.New("transient delete failure")
	if _, err := controller.reconcile("", deleting); err == nil {
		t.Fatal("reconcile(deleting) returned nil, want a requeue error")
	}
	if !hasClusterFinalizer(clusterStore.get()) {
		t.Fatal("finalizer was removed after a cleanup error")
	}

	secrets.deleteErr = nil
	if _, err := controller.reconcile("", clusterStore.get()); err != nil {
		t.Fatalf("reconcile(deleting retry) error = %v", err)
	}
	if hasClusterFinalizer(clusterStore.get()) {
		t.Error("finalizer was not removed after the retry succeeded")
	}
	if _, err := controller.secrets.Get("dragonfly-system", helmop.SchedulerSourceSecretName("c1"), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("scheduler Secret still exists after the successful retry: %v", err)
	}
}

package cluster

import (
	"context"
	"testing"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	capiv1beta2 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

type fakeClusterCache struct {
	clusters []*provv1.Cluster
}

func (f *fakeClusterCache) List(_ string, _ labels.Selector) ([]*provv1.Cluster, error) {
	return f.clusters, nil
}

type fakeMachineCache struct {
	machines []*capiv1beta2.Machine
}

func (f *fakeMachineCache) List(_ string, _ labels.Selector) ([]*capiv1beta2.Machine, error) {
	return f.machines, nil
}

func schedulerCluster(name string) *provv1.Cluster {
	return &provv1.Cluster{ObjectMeta: metav1.ObjectMeta{
		Name:        name,
		Namespace:   "fleet-default",
		Labels:      map[string]string{LabelEnabled: "true"},
		Annotations: map[string]string{AnnotationSeed: "true"},
	}}
}

func schedulerMachine(cluster, name string, addresses ...capiv1beta2.MachineAddress) *capiv1beta2.Machine {
	return &capiv1beta2.Machine{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: "fleet-default",
		Labels: map[string]string{capiClusterName: cluster},
	}, Status: capiv1beta2.MachineStatus{Addresses: addresses}}
}

func testSchedulerController(target *provv1.Cluster, clusters []*provv1.Cluster, machines []*capiv1beta2.Machine) (*Controller, *fakeClusterStore, *fakeEventStore) {
	store := newFakeClusterStore()
	store.set(target)
	events := &fakeEventStore{}
	c := &Controller{
		ctx:          context.Background(),
		clusters:     store,
		clusterCache: &fakeClusterCache{clusters: clusters},
		machines:     &fakeMachineCache{machines: machines},
		events:       events,
	}
	return c, store, events
}

func TestSchedulerIPsUseOtherGatedSeedClusters(t *testing.T) {
	target := schedulerCluster("a")
	other := schedulerCluster("b")
	clusters := []*provv1.Cluster{target, other}
	machines := []*capiv1beta2.Machine{
		schedulerMachine("a", "a-0", capiv1beta2.MachineAddress{Type: capiv1beta2.MachineExternalIP, Address: "203.0.113.10"}),
		schedulerMachine("b", "b-0", capiv1beta2.MachineAddress{Type: capiv1beta2.MachineExternalIP, Address: "203.0.113.20"}),
		schedulerMachine("b", "b-1", capiv1beta2.MachineAddress{Type: capiv1beta2.MachineInternalIP, Address: "203.0.113.21"}),
	}
	controller, store, events := testSchedulerController(target, clusters, machines)

	updated, err := controller.ensureSchedulerIPs(target)
	if err != nil {
		t.Fatalf("ensureSchedulerIPs() error = %v", err)
	}
	if got := store.annotation(AnnotationSchedulerIPs); got != "203.0.113.20,203.0.113.21" {
		t.Errorf("scheduler IPs = %q, want other seed IPs only", got)
	}
	if updated == nil {
		t.Fatal("expected an updated Cluster")
	}
	if len(events.events) != 1 || events.events[0].Reason != "SchedulerIPsUpdated" {
		t.Errorf("events = %+v, want SchedulerIPsUpdated", events.events)
	}
}

func TestSchedulerIPsEmptyRemovesAnnotation(t *testing.T) {
	target := schedulerCluster("a")
	target.Annotations[AnnotationSchedulerIPs] = "203.0.113.99"
	controller, store, _ := testSchedulerController(target, []*provv1.Cluster{target}, nil)

	if _, err := controller.ensureSchedulerIPs(target); err != nil {
		t.Fatalf("ensureSchedulerIPs() error = %v", err)
	}
	if got := store.annotation(AnnotationSchedulerIPs); got != "" {
		t.Errorf("scheduler IPs = %q, want removed", got)
	}
}

func TestSchedulerIPsUseHetznerStatusAddresses(t *testing.T) {
	target := schedulerCluster("a")
	other := schedulerCluster("b")
	machine := schedulerMachine("b", "b-0")
	machine.Spec.InfrastructureRef = capiv1beta2.ContractVersionedObjectReference{Kind: "HetznerMachine", Name: "b-0"}
	infra := newFakeHelmOpStore()
	infra.objects["fleet-default/b-0"] = &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rke-machine.cattle.io/v1",
		"kind":       "HetznerMachine",
		"status": map[string]any{"addresses": []any{
			map[string]any{"type": "ExternalIP", "address": "203.0.113.30"},
		}},
	}}
	controller, store, _ := testSchedulerController(target, []*provv1.Cluster{target, other}, []*capiv1beta2.Machine{machine})
	controller.infra = infra

	if _, err := controller.ensureSchedulerIPs(target); err != nil {
		t.Fatalf("ensureSchedulerIPs() error = %v", err)
	}
	if got := store.annotation(AnnotationSchedulerIPs); got != "203.0.113.30" {
		t.Errorf("scheduler IPs = %q, want Hetzner address", got)
	}
}

func TestSchedulerIPsSkipDeletingAndMissingAddresses(t *testing.T) {
	target := schedulerCluster("a")
	other := schedulerCluster("b")
	deleting := schedulerMachine("b", "b-0", capiv1beta2.MachineAddress{Type: capiv1beta2.MachineExternalIP, Address: "203.0.113.40"})
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	missing := schedulerMachine("b", "b-1")
	controller, store, _ := testSchedulerController(target, []*provv1.Cluster{target, other}, []*capiv1beta2.Machine{deleting, missing})

	if _, err := controller.ensureSchedulerIPs(target); err != nil {
		t.Fatalf("ensureSchedulerIPs() error = %v", err)
	}
	if got := store.annotation(AnnotationSchedulerIPs); got != "" {
		t.Errorf("scheduler IPs = %q, want empty", got)
	}
}

package cluster

import (
	"context"
	"testing"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
)

type fakeClusterCache struct {
	clusters []*provv1.Cluster
}

func (f *fakeClusterCache) List(_ string, _ labels.Selector) ([]*provv1.Cluster, error) {
	return f.clusters, nil
}

func schedulerCluster(name, publicIP string) *provv1.Cluster {
	annotations := map[string]string{AnnotationSeed: "true"}
	if publicIP != "" {
		annotations[helmop.AnnotationSchedulerPublicIP] = publicIP
	}
	return &provv1.Cluster{ObjectMeta: metav1.ObjectMeta{
		Name:        name,
		Namespace:   "fleet-default",
		Labels:      map[string]string{LabelEnabled: "true"},
		Annotations: annotations,
	}}
}

func testSchedulerController(target *provv1.Cluster, clusters []*provv1.Cluster) (*Controller, *fakeClusterStore, *fakeEventStore) {
	store := newFakeClusterStore()
	store.set(target)
	events := &fakeEventStore{}
	c := &Controller{
		ctx:          context.Background(),
		clusters:     store,
		clusterCache: &fakeClusterCache{clusters: clusters},
		events:       events,
	}
	return c, store, events
}

func TestSchedulerIPsUseOtherSeedPublicIPs(t *testing.T) {
	target := schedulerCluster("a", "203.0.113.99")
	other := schedulerCluster("b", "203.0.113.30")
	third := schedulerCluster("c", "203.0.113.10")
	controller, store, events := testSchedulerController(target, []*provv1.Cluster{target, other, third})

	if _, err := controller.ensureSchedulerIPs(target); err != nil {
		t.Fatalf("ensureSchedulerIPs() error = %v", err)
	}
	if got := store.annotation(AnnotationSchedulerIPs); got != "203.0.113.10,203.0.113.30" {
		t.Errorf("scheduler IPs = %q, want sorted other seed public IPs", got)
	}
	if len(events.events) != 1 || events.events[0].Reason != "SchedulerIPsUpdated" {
		t.Errorf("events = %+v, want SchedulerIPsUpdated", events.events)
	}
}

func TestSchedulerIPsSkipInvalidPrivateAndDuplicate(t *testing.T) {
	target := schedulerCluster("a", "")
	bad := schedulerCluster("b", "not-an-ip")
	private := schedulerCluster("c", "10.0.0.1")
	one := schedulerCluster("d", "203.0.113.10")
	two := schedulerCluster("e", "203.0.113.10")
	controller, store, _ := testSchedulerController(target, []*provv1.Cluster{target, bad, private, one, two})

	if _, err := controller.ensureSchedulerIPs(target); err != nil {
		t.Fatalf("ensureSchedulerIPs() error = %v", err)
	}
	if got := store.annotation(AnnotationSchedulerIPs); got != "203.0.113.10" {
		t.Errorf("scheduler IPs = %q, want one deduplicated public IP", got)
	}
}

func TestSchedulerIPsEmptyRemovesAnnotation(t *testing.T) {
	target := schedulerCluster("a", "")
	target.Annotations[AnnotationSchedulerIPs] = "203.0.113.99"
	controller, store, events := testSchedulerController(target, []*provv1.Cluster{target})

	if _, err := controller.ensureSchedulerIPs(target); err != nil {
		t.Fatalf("ensureSchedulerIPs() error = %v", err)
	}
	if got := store.annotation(AnnotationSchedulerIPs); got != "" {
		t.Errorf("scheduler IPs = %q, want removed", got)
	}
	if len(events.events) == 0 || events.events[0].Reason != "SchedulerIPsUpdated" {
		t.Errorf("events = %+v, want SchedulerIPsUpdated", events.events)
	}
}

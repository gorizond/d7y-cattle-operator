package cluster

import (
	"context"
	"testing"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

type fakeClusterCache struct {
	clusters []*provv1.Cluster
}

func (f *fakeClusterCache) List(_ string, _ labels.Selector) ([]*provv1.Cluster, error) {
	return f.clusters, nil
}

func schedulerCluster(name string) *provv1.Cluster {
	return &provv1.Cluster{ObjectMeta: metav1.ObjectMeta{
		Name:        name,
		Namespace:   "fleet-default",
		Labels:      map[string]string{LabelEnabled: "true"},
		Annotations: map[string]string{},
	}}
}

func schedulerSeed(name, ips string) *provv1.Cluster {
	cluster := schedulerCluster(name)
	cluster.Annotations[AnnotationSeed] = "true"
	if ips != "" {
		cluster.Annotations[AnnotationSchedulerIPs] = ips
	}
	return cluster
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

func TestSeedPublishesOwnResolvedPublicIPs(t *testing.T) {
	target := schedulerSeed("a", "203.0.113.99")
	target.Annotations[legacySchedulerPublicIP] = "192.0.2.1"
	controller, store, events := testSchedulerController(target, []*provv1.Cluster{target})

	if _, err := controller.ensureSchedulerIPs(target, Roles{Enabled: true, Seed: true}, []string{"203.0.113.10", "203.0.113.11", "203.0.113.10"}); err != nil {
		t.Fatalf("ensureSchedulerIPs() error = %v", err)
	}
	if got := store.annotation(AnnotationSchedulerIPs); got != "203.0.113.10,203.0.113.11" {
		t.Errorf("scheduler IPs = %q, want seed's own deduped sorted IPs", got)
	}
	if got := store.annotation(legacySchedulerPublicIP); got != "" {
		t.Errorf("legacy scheduler-public-ip = %q, want removed", got)
	}
	if len(events.events) != 1 || events.events[0].Reason != "SchedulerIPsUpdated" {
		t.Errorf("events = %+v, want SchedulerIPsUpdated", events.events)
	}
}

func TestNonSeedAggregatesOtherSeedIPs(t *testing.T) {
	target := schedulerCluster("managed")
	seedA := schedulerSeed("seed-a", "203.0.113.30,203.0.113.10")
	seedB := schedulerSeed("seed-b", "203.0.113.10")
	nonSeed := schedulerCluster("other")
	nonSeed.Annotations[AnnotationSchedulerIPs] = "198.51.100.99"
	controller, store, _ := testSchedulerController(target, []*provv1.Cluster{target, seedA, seedB, nonSeed})

	if _, err := controller.ensureSchedulerIPs(target, Roles{Enabled: true}, nil); err != nil {
		t.Fatalf("ensureSchedulerIPs() error = %v", err)
	}
	if got := store.annotation(AnnotationSchedulerIPs); got != "203.0.113.10,203.0.113.30" {
		t.Errorf("scheduler IPs = %q, want union of seed IPs only", got)
	}
}

func TestSchedulerIPsSkipInvalidPrivateAndSelf(t *testing.T) {
	target := schedulerCluster("managed")
	target.Annotations[AnnotationSchedulerIPs] = "198.51.100.99"
	bad := schedulerSeed("bad", "not-an-ip")
	private := schedulerSeed("private", "10.0.0.1")
	good := schedulerSeed("good", "203.0.113.10")
	controller, store, _ := testSchedulerController(target, []*provv1.Cluster{target, bad, private, good})

	if _, err := controller.ensureSchedulerIPs(target, Roles{Enabled: true}, nil); err != nil {
		t.Fatalf("ensureSchedulerIPs() error = %v", err)
	}
	if got := store.annotation(AnnotationSchedulerIPs); got != "203.0.113.10" {
		t.Errorf("scheduler IPs = %q, want only the valid public seed IP", got)
	}
}

func TestSchedulerIPsEmptyRemovesAnnotationAndLegacy(t *testing.T) {
	target := schedulerCluster("managed")
	target.Annotations[AnnotationSchedulerIPs] = "198.51.100.99"
	target.Annotations[legacySchedulerPublicIP] = "192.0.2.1"
	controller, store, events := testSchedulerController(target, []*provv1.Cluster{target})

	if _, err := controller.ensureSchedulerIPs(target, Roles{Enabled: true}, nil); err != nil {
		t.Fatalf("ensureSchedulerIPs() error = %v", err)
	}
	if got := store.annotation(AnnotationSchedulerIPs); got != "" {
		t.Errorf("scheduler IPs = %q, want removed", got)
	}
	if got := store.annotation(legacySchedulerPublicIP); got != "" {
		t.Errorf("legacy scheduler-public-ip = %q, want removed", got)
	}
	if len(events.events) == 0 || events.events[0].Reason != "SchedulerIPsUpdated" {
		t.Errorf("events = %+v, want SchedulerIPsUpdated", events.events)
	}
}

func TestSchedulerIPsSeedEmptyRemovesAnnotation(t *testing.T) {
	target := schedulerSeed("seed-a", "203.0.113.99")
	controller, store, _ := testSchedulerController(target, []*provv1.Cluster{target})

	if _, err := controller.ensureSchedulerIPs(target, Roles{Enabled: true, Seed: true}, nil); err != nil {
		t.Fatalf("ensureSchedulerIPs() error = %v", err)
	}
	if got := store.annotation(AnnotationSchedulerIPs); got != "" {
		t.Errorf("scheduler IPs = %q, want removed when DNS resolved nothing", got)
	}
}

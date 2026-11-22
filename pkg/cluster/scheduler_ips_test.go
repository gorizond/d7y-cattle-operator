package cluster

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	err      error
}

type fakeSchedulerPodLister struct {
	pods          []schedulerPod
	err           error
	secretMissing bool
	secretErr     error
}

func (f *fakeSchedulerPodLister) ListSchedulerPods(_ context.Context, _ []byte) ([]schedulerPod, error) {
	return f.pods, f.err
}

func (f *fakeSchedulerPodLister) SecretExists(_ context.Context, _ []byte, _, _ string) (bool, error) {
	return !f.secretMissing, f.secretErr
}

func (f *fakeMachineCache) List(_ string, _ labels.Selector) ([]*capiv1beta2.Machine, error) {
	return f.machines, f.err
}

func seedMachine(cluster, name, address string) *capiv1beta2.Machine {
	return &capiv1beta2.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "fleet-default",
			Labels: map[string]string{capiClusterName: cluster},
		},
		Status: capiv1beta2.MachineStatus{Addresses: []capiv1beta2.MachineAddress{{
			Type: capiv1beta2.MachineExternalIP, Address: address,
		}}},
	}
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

func TestSeedUsesSchedulerPodsOnMachineNodes(t *testing.T) {
	target := schedulerSeed("seed-a", "203.0.113.99")
	target.Annotations[legacySchedulerPublicIP] = "192.0.2.1"
	nodeA := seedMachine("seed-a", "seed-a-0", "203.0.113.10")
	nodeA.Status.NodeRef = capiv1beta2.MachineNodeReference{Name: "node-a"}
	nodeB := seedMachine("seed-a", "seed-a-1", "203.0.113.11")
	nodeB.Status.NodeRef = capiv1beta2.MachineNodeReference{Name: "node-b"}
	fallback := seedMachine("seed-a", "seed-a-2", "203.0.113.12")
	fallback.Status.Addresses = append(fallback.Status.Addresses,
		capiv1beta2.MachineAddress{Type: capiv1beta2.MachineInternalIP, Address: "10.0.0.1"},
		capiv1beta2.MachineAddress{Type: capiv1beta2.MachineInternalIP, Address: "2001:db8::1"},
	)
	deleting := seedMachine("seed-a", "seed-a-deleting", "203.0.113.40")
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	deleting.Status.NodeRef = capiv1beta2.MachineNodeReference{Name: "node-deleting"}
	controller, store, events := testSchedulerController(target, []*provv1.Cluster{target})
	secrets := newFakeSecretStore()
	secrets.put(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "seed-a-kubeconfig", Namespace: "fleet-default"}, Data: map[string][]byte{"value": []byte("fake")}})
	controller.secrets = secrets
	controller.machines = &fakeMachineCache{machines: []*capiv1beta2.Machine{
		nodeA, nodeB, fallback,
		seedMachine("other", "other-0", "203.0.113.50"),
		deleting,
		seedMachine("seed-a", "seed-a-empty", ""),
	}}
	controller.downstream = &fakeSchedulerPodLister{pods: []schedulerPod{
		{NodeName: "node-a", HostIP: "203.0.113.10"},
		{NodeName: "node-a", HostIP: "203.0.113.10"},
		{NodeName: "node-b", HostIP: "203.0.113.11"},
		{NodeName: "missing-node", HostIP: "203.0.113.12"},
		{NodeName: "node-deleting", HostIP: "203.0.113.40"},
	}}

	ips, err := controller.discoverSeedSchedulerIPs(target)
	if err != nil {
		t.Fatalf("discoverSeedSchedulerIPs() error = %v", err)
	}
	if _, err := controller.ensureSchedulerIPs(target, Roles{Enabled: true, Seed: true}, ips); err != nil {
		t.Fatalf("ensureSchedulerIPs() error = %v", err)
	}
	if got := store.annotation(AnnotationSchedulerIPs); got != "203.0.113.10,203.0.113.11,203.0.113.12" {
		t.Errorf("scheduler IPs = %q, want deduped/sorted scheduler node IPs", got)
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

func TestSeedMissingKubeconfigKeepsAnnotation(t *testing.T) {
	controller, _, events := newTestController(t)
	cluster := testSeedCluster("42")
	cluster.Annotations[AnnotationSchedulerIPs] = "203.0.113.10"
	controller.clusters.(*fakeClusterStore).set(cluster)
	controller.secrets.(*fakeSecretStore).secrets = map[string]*corev1.Secret{}
	controller.downstream.(*fakeSchedulerPodLister).pods = nil

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	if got := controller.clusters.(*fakeClusterStore).annotation(AnnotationSchedulerIPs); got != "203.0.113.10" {
		t.Errorf("scheduler IPs = %q, want stale annotation preserved on discovery failure", got)
	}
	if !hasEventReason(events.events, "SchedulerIPsUnavailable") {
		t.Errorf("events = %+v, want SchedulerIPsUnavailable", events.events)
	}
}

func TestSeedEmptySchedulerPodsRemovesAnnotation(t *testing.T) {
	controller, _, _ := newTestController(t)
	cluster := testSeedCluster("42")
	controller.downstream.(*fakeSchedulerPodLister).pods = nil

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	if got := controller.clusters.(*fakeClusterStore).annotation(AnnotationSchedulerIPs); got != "" {
		t.Errorf("scheduler IPs = %q, want empty without scheduler pods", got)
	}
}

func TestSeedDownstreamErrorKeepsAnnotation(t *testing.T) {
	controller, _, events := newTestController(t)
	cluster := testSeedCluster("42")
	cluster.Annotations[AnnotationSchedulerIPs] = "203.0.113.10"
	controller.clusters.(*fakeClusterStore).set(cluster)
	controller.downstream.(*fakeSchedulerPodLister).err = errors.New("downstream unavailable")

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	if got := controller.clusters.(*fakeClusterStore).annotation(AnnotationSchedulerIPs); got != "203.0.113.10" {
		t.Errorf("scheduler IPs = %q, want stale annotation preserved on downstream error", got)
	}
	if !hasEventReason(events.events, "SchedulerIPsUnavailable") {
		t.Errorf("events = %+v, want SchedulerIPsUnavailable", events.events)
	}
}

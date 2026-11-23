package cluster

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

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
	nodes         map[string]schedulerNode
	nodesErr      error
	err           error
	secretMissing bool
	secretErr     error
}

func (f *fakeSchedulerPodLister) ListSchedulerPods(_ context.Context, _ []byte) ([]schedulerPod, error) {
	return f.pods, f.err
}

func (f *fakeSchedulerPodLister) ListSchedulerNodes(_ context.Context, _ []byte) (map[string]schedulerNode, error) {
	return f.nodes, f.nodesErr
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

func schedulerMachine(name, nodeName string, addresses ...capiv1beta2.MachineAddress) *capiv1beta2.Machine {
	machine := &capiv1beta2.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "fleet-default",
			Labels: map[string]string{capiClusterName: "seed-a"},
		},
		Status: capiv1beta2.MachineStatus{Addresses: addresses},
	}
	if nodeName != "" {
		machine.Status.NodeRef = capiv1beta2.MachineNodeReference{Name: nodeName}
	}
	return machine
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

func TestSeedSchedulerIPsPreferDownstreamNode(t *testing.T) {
	internal := func(address string) capiv1beta2.MachineAddress {
		return capiv1beta2.MachineAddress{Type: capiv1beta2.MachineInternalIP, Address: address}
	}
	external := func(address string) capiv1beta2.MachineAddress {
		return capiv1beta2.MachineAddress{Type: capiv1beta2.MachineExternalIP, Address: address}
	}
	tests := []struct {
		name     string
		pod      schedulerPod
		node     schedulerNode
		machines []*capiv1beta2.Machine
		want     string
	}{
		{
			name: "node external ip wins",
			pod:  schedulerPod{NodeName: "node-a", HostIP: "10.0.0.1"},
			node: schedulerNode{InternalIPs: []string{"10.0.0.1"}, ExternalIPs: []string{"203.0.113.20"}},
			machines: []*capiv1beta2.Machine{
				schedulerMachine("seed-a-0", "node-a", internal("10.0.0.1"), external("203.0.113.99")),
			},
			want: "203.0.113.20",
		},
		{
			name: "machine external ip fallback",
			pod:  schedulerPod{NodeName: "node-b", HostIP: "10.0.0.2"},
			node: schedulerNode{InternalIPs: []string{"10.0.0.2"}},
			machines: []*capiv1beta2.Machine{
				schedulerMachine("seed-a-1", "node-b", internal("10.0.0.2"), external("203.0.113.21")),
			},
			want: "203.0.113.21",
		},
		{
			name: "internal ips are never published",
			pod:  schedulerPod{NodeName: "node-c", HostIP: "10.0.0.3"},
			node: schedulerNode{InternalIPs: []string{"10.0.0.3"}},
			machines: []*capiv1beta2.Machine{
				schedulerMachine("seed-a-2", "node-c", internal("10.0.0.3")),
			},
			want: "",
		},
		{
			name: "first internal ip matches the machine",
			pod:  schedulerPod{NodeName: "node-d", HostIP: "10.0.0.4"},
			node: schedulerNode{InternalIPs: []string{"10.0.0.4", "10.0.1.4"}},
			machines: []*capiv1beta2.Machine{
				schedulerMachine("seed-a-3", "", internal("10.0.0.4"), external("203.0.113.23")),
				schedulerMachine("seed-a-decoy", "", internal("10.0.1.4"), external("203.0.113.24")),
			},
			want: "203.0.113.23",
		},
		{
			name: "missing noderef matches by first internal ip",
			pod:  schedulerPod{NodeName: "node-e", HostIP: "10.0.0.5"},
			node: schedulerNode{InternalIPs: []string{"10.0.0.5"}},
			machines: []*capiv1beta2.Machine{
				schedulerMachine("seed-a-4", "", internal("10.0.0.5"), external("203.0.113.25")),
			},
			want: "203.0.113.25",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := schedulerSeed("seed-a", "")
			controller, _, _ := testSchedulerController(target, []*provv1.Cluster{target})
			secrets := newFakeSecretStore()
			secrets.put(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "seed-a-kubeconfig", Namespace: "fleet-default"}, Data: map[string][]byte{"value": []byte("fake")}})
			controller.secrets = secrets
			controller.machines = &fakeMachineCache{machines: tt.machines}
			controller.downstream = &fakeSchedulerPodLister{
				pods:  []schedulerPod{tt.pod},
				nodes: map[string]schedulerNode{tt.pod.NodeName: tt.node},
			}

			ips, err := controller.discoverSeedSchedulerIPs(target)
			if err != nil {
				t.Fatalf("discoverSeedSchedulerIPs() error = %v", err)
			}
			if got := strings.Join(ips, ","); got != tt.want {
				t.Errorf("scheduler IPs = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSeedNodeLookupErrorUsesOtherSeedIPs(t *testing.T) {
	controller, _, events := newTestController(t)
	cluster := testSeedCluster("42")
	controller.clusters.(*fakeClusterStore).set(cluster)
	poligon := schedulerSeed("poligon", "203.0.113.50")
	controller.clusterCache = &fakeClusterCache{clusters: []*provv1.Cluster{cluster, poligon}}
	controller.downstream.(*fakeSchedulerPodLister).nodesErr = errors.New("nodes unavailable")

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	if got := controller.clusters.(*fakeClusterStore).annotation(AnnotationSchedulerIPs); got != "203.0.113.50" {
		t.Errorf("scheduler IPs = %q, want the other seed's scheduler IPs", got)
	}
	if !hasEventReason(events.events, "SchedulerIPsFallback") {
		t.Errorf("events = %+v, want SchedulerIPsFallback", events.events)
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

func TestSeedForbiddenDownstreamFallsBackToMachineIPs(t *testing.T) {
	controller, _, events := newTestController(t)
	cluster := testSeedCluster("42")
	cluster.Annotations[AnnotationSchedulerIPs] = "203.0.113.99"
	controller.clusters.(*fakeClusterStore).set(cluster)
	controller.downstream.(*fakeSchedulerPodLister).err = apierrors.NewForbidden(
		schema.GroupResource{Group: "management.cattle.io", Resource: "clusters"}, "c-m-test", errors.New("forbidden"))
	controller.machines = &fakeMachineCache{machines: []*capiv1beta2.Machine{
		seedMachine("c1", "c1-0", "203.0.113.10"),
		seedMachine("c1", "c1-1", "203.0.113.11"),
		seedMachine("other", "other-0", "203.0.113.50"),
	}}

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	if got := controller.clusters.(*fakeClusterStore).annotation(AnnotationSchedulerIPs); got != "203.0.113.10,203.0.113.11" {
		t.Errorf("scheduler IPs = %q, want all seed machine public IPs", got)
	}
	if hasEventReason(events.events, "SchedulerIPsUnavailable") {
		t.Errorf("events = %+v, must not report SchedulerIPsUnavailable when the fallback succeeds", events.events)
	}
	if !hasEventReason(events.events, "SchedulerIPsFallback") {
		t.Errorf("events = %+v, want SchedulerIPsFallback", events.events)
	}
}

func TestSeedForbiddenDownstreamWithoutMachineIPsKeepsAnnotation(t *testing.T) {
	controller, _, events := newTestController(t)
	cluster := testSeedCluster("42")
	cluster.Annotations[AnnotationSchedulerIPs] = "203.0.113.10"
	controller.clusters.(*fakeClusterStore).set(cluster)
	controller.downstream.(*fakeSchedulerPodLister).err = apierrors.NewForbidden(
		schema.GroupResource{Group: "management.cattle.io", Resource: "clusters"}, "c-m-test", errors.New("forbidden"))
	controller.machines = &fakeMachineCache{}

	if _, err := controller.reconcile("", cluster); err == nil {
		t.Fatal("reconcile() error = nil, want the discovery error for a retry")
	}
	if got := controller.clusters.(*fakeClusterStore).annotation(AnnotationSchedulerIPs); got != "203.0.113.10" {
		t.Errorf("scheduler IPs = %q, want the stale annotation preserved", got)
	}
	if !hasEventReason(events.events, "SchedulerIPsUnavailable") {
		t.Errorf("events = %+v, want SchedulerIPsUnavailable", events.events)
	}
}

func TestSeedMissingKubeconfigFallsBackToMachineIPs(t *testing.T) {
	controller, _, events := newTestController(t)
	cluster := testSeedCluster("42")
	cluster.Annotations[AnnotationSchedulerIPs] = "203.0.113.99"
	controller.clusters.(*fakeClusterStore).set(cluster)
	controller.secrets.(*fakeSecretStore).secrets = map[string]*corev1.Secret{}
	controller.machines = &fakeMachineCache{machines: []*capiv1beta2.Machine{
		seedMachine("c1", "c1-0", "203.0.113.10"),
	}}

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	if got := controller.clusters.(*fakeClusterStore).annotation(AnnotationSchedulerIPs); got != "203.0.113.10" {
		t.Errorf("scheduler IPs = %q, want the seed machine public IP", got)
	}
	if !hasEventReason(events.events, "SchedulerIPsFallback") {
		t.Errorf("events = %+v, want SchedulerIPsFallback", events.events)
	}
	if hasEventReason(events.events, "SchedulerIPsUnavailable") {
		t.Errorf("events = %+v, must not report SchedulerIPsUnavailable after a fallback", events.events)
	}
}

func TestSeedMissingKubeconfigWithoutFallbackKeepsAnnotation(t *testing.T) {
	controller, _, events := newTestController(t)
	cluster := testSeedCluster("42")
	cluster.Annotations[AnnotationSchedulerIPs] = "203.0.113.10"
	controller.clusters.(*fakeClusterStore).set(cluster)
	controller.secrets.(*fakeSecretStore).secrets = map[string]*corev1.Secret{}
	controller.machines = &fakeMachineCache{}
	controller.clusterCache = &fakeClusterCache{}

	if _, err := controller.reconcile("", cluster); err == nil {
		t.Fatal("reconcile() error = nil, want the discovery error for a retry")
	}
	if got := controller.clusters.(*fakeClusterStore).annotation(AnnotationSchedulerIPs); got != "203.0.113.10" {
		t.Errorf("scheduler IPs = %q, want the stale annotation preserved", got)
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

func TestSeedDownstreamErrorWithoutFallbackKeepsAnnotation(t *testing.T) {
	controller, _, events := newTestController(t)
	cluster := testSeedCluster("42")
	cluster.Annotations[AnnotationSchedulerIPs] = "203.0.113.10"
	controller.clusters.(*fakeClusterStore).set(cluster)
	controller.downstream.(*fakeSchedulerPodLister).err = errors.New("downstream unavailable")
	controller.machines = &fakeMachineCache{}
	controller.clusterCache = &fakeClusterCache{}

	if _, err := controller.reconcile("", cluster); err == nil {
		t.Fatal("reconcile() error = nil, want the discovery error for a retry")
	}
	if got := controller.clusters.(*fakeClusterStore).annotation(AnnotationSchedulerIPs); got != "203.0.113.10" {
		t.Errorf("scheduler IPs = %q, want the stale annotation preserved", got)
	}
	if !hasEventReason(events.events, "SchedulerIPsUnavailable") {
		t.Errorf("events = %+v, want SchedulerIPsUnavailable", events.events)
	}
}

func TestSeedEmptySchedulerPodsUsesOtherSeedIPs(t *testing.T) {
	controller, _, events := newTestController(t)
	cluster := testSeedCluster("42")
	controller.clusters.(*fakeClusterStore).set(cluster)
	poligon := schedulerSeed("poligon", "203.0.113.50")
	controller.clusterCache = &fakeClusterCache{clusters: []*provv1.Cluster{cluster, poligon}}
	controller.downstream.(*fakeSchedulerPodLister).pods = nil

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	if got := controller.clusters.(*fakeClusterStore).annotation(AnnotationSchedulerIPs); got != "203.0.113.50" {
		t.Errorf("scheduler IPs = %q, want the other seed's scheduler IPs", got)
	}
	if !hasEventReason(events.events, "SchedulerIPsFallback") {
		t.Errorf("events = %+v, want SchedulerIPsFallback", events.events)
	}
	if hasEventReason(events.events, "SchedulerIPsUnavailable") {
		t.Errorf("events = %+v, must not report SchedulerIPsUnavailable after a fallback", events.events)
	}
}

func TestSeedRawForbiddenUsesOtherSeedIPs(t *testing.T) {
	controller, _, events := newTestController(t)
	cluster := testSeedCluster("42")
	controller.clusters.(*fakeClusterStore).set(cluster)
	poligon := schedulerSeed("poligon", "203.0.113.50")
	controller.clusterCache = &fakeClusterCache{clusters: []*provv1.Cluster{cluster, poligon}}
	controller.downstream.(*fakeSchedulerPodLister).err = errors.New(`{"Code":{"Code":"Forbidden","Status":403},"Message":"clusters.management.cattle.io is forbidden: User "u-test" cannot get resource "clusters" in API group "management.cattle.io" at the cluster scope","Cause":null,"FieldName":""} (get pods)`)

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	if got := controller.clusters.(*fakeClusterStore).annotation(AnnotationSchedulerIPs); got != "203.0.113.50" {
		t.Errorf("scheduler IPs = %q, want the other seed's scheduler IPs", got)
	}
	if !hasEventReason(events.events, "SchedulerIPsFallback") {
		t.Errorf("events = %+v, want SchedulerIPsFallback", events.events)
	}
	if hasEventReason(events.events, "SchedulerIPsUnavailable") {
		t.Errorf("events = %+v, must not report SchedulerIPsUnavailable after a fallback", events.events)
	}
}

func TestSeedNilMachineCacheFallsBackToOtherSeedIPs(t *testing.T) {
	controller, _, events := newTestController(t)
	cluster := testSeedCluster("42")
	controller.clusters.(*fakeClusterStore).set(cluster)
	poligon := schedulerSeed("poligon", "203.0.113.50")
	controller.clusterCache = &fakeClusterCache{clusters: []*provv1.Cluster{cluster, poligon}}
	controller.machines = nil

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	if got := controller.clusters.(*fakeClusterStore).annotation(AnnotationSchedulerIPs); got != "203.0.113.50" {
		t.Errorf("scheduler IPs = %q, want the other seed's scheduler IPs", got)
	}
	if !hasEventReason(events.events, "SchedulerIPsFallback") {
		t.Errorf("events = %+v, want SchedulerIPsFallback", events.events)
	}
}

func TestDownstreamAccessDeniedRawRancherError(t *testing.T) {
	raw := errors.New(`{"Code":{"Code":"Forbidden","Status":403},"Message":"clusters.management.cattle.io is forbidden","Cause":null,"FieldName":""} (get pods)`)
	if !downstreamAccessDenied(raw) {
		t.Fatal("downstreamAccessDenied() = false, want true for the raw Rancher 403 body")
	}
	if downstreamAccessDenied(errors.New("connection refused")) {
		t.Fatal("downstreamAccessDenied() = true for an unrelated error")
	}
}

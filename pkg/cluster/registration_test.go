package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	"github.com/rancher/wrangler/v3/pkg/genericcondition"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
	"github.com/gorizond/d7y-cattle-operator/pkg/managerapi"
	"github.com/gorizond/d7y-cattle-operator/pkg/registry"
)

func TestReconcileRegistrationUsesManagerAPI(t *testing.T) {
	var signins, creates, links atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == managerapi.SignInPath:
			signins.Add(1)
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode signin: %v", err)
			}
			if body["name"] != "admin" {
				t.Errorf("signin name = %v", body["name"])
			}
			writeJSONResponse(w, http.StatusOK, map[string]any{"token": "jwt", "expire": "2026-01-01T00:00:00Z"})
		case r.URL.Path == managerapi.SchedulerClustersPath:
			if r.Method == http.MethodPost {
				creates.Add(1)
				writeJSONResponse(w, http.StatusCreated, map[string]any{"id": 5, "name": "fleet-default-c1"})
				return
			}
			writeJSONResponse(w, http.StatusOK, []any{})
		case r.URL.Path == managerapi.SeedPeerClustersPath:
			if r.Method == http.MethodPost {
				creates.Add(1)
				writeJSONResponse(w, http.StatusCreated, map[string]any{"id": 7, "name": "fleet-default-c1-seed"})
				return
			}
			writeJSONResponse(w, http.StatusOK, []any{})
		case r.URL.Path == managerapi.SeedPeerClustersPath+"/7/scheduler-clusters/5":
			links.Add(1)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	controller, store, _ := newTestController(t)
	controller.registrar = nil
	controller.allocator = nil
	controller.managerHTTPClient = server.Client()
	controller.opts.ManagerAPIURL = server.URL
	controller.opts.ManagerAPICredentialsSecret = "manager-api-creds"
	controller.secrets.(*fakeSecretStore).put(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "manager-api-creds", Namespace: "dragonfly-system"},
		Data:       map[string][]byte{"username": []byte("admin"), "password": []byte("password")},
	})

	cluster := testSeedCluster("")
	clusterStore := controller.clusters.(*fakeClusterStore)
	clusterStore.set(cluster)

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	if got := clusterStore.annotation(AnnotationSchedulerClusterID); got != "5" {
		t.Errorf("annotation = %q, want server ID 5", got)
	}
	if got := clusterStore.annotation(AnnotationSchedulerClusterIDAuto); got != "5" {
		t.Errorf("auto marker = %q, want server ID 5", got)
	}
	if !store.has("fleet-default/c1-control") || !store.has("fleet-default/c1-data") {
		t.Error("seed HelmOps were not created after successful manager registration")
	}
	if signins.Load() != 1 || creates.Load() != 2 || links.Load() != 1 {
		t.Errorf("signins/creates/links = %d/%d/%d, want 1/2/1", signins.Load(), creates.Load(), links.Load())
	}
}

func writeJSONResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func TestReconcileAutoRegistrationWritesAnnotation(t *testing.T) {
	controller, store, _ := newTestController(t)
	registrar := controller.registrar.(*fakeRegistrar)
	registrar.schedulerID = 5
	registrar.seedPeerID = 7

	cluster := testSeedCluster("")
	clusterStore := controller.clusters.(*fakeClusterStore)
	clusterStore.set(cluster)

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}

	if got := clusterStore.annotation(AnnotationSchedulerClusterID); got != "5" {
		t.Errorf("scheduler ID annotation = %q, want 5", got)
	}
	if got := clusterStore.annotation(AnnotationSchedulerClusterIDAuto); got != "5" {
		t.Errorf("auto marker = %q, want 5", got)
	}
	if !store.has("fleet-default/c1-keydb") {
		t.Error("KeyDB HelmOp was not created")
	}
	for _, name := range []string{"c1-control", "c1-data", "c1-nydus-snapshotter"} {
		if !store.has("fleet-default/" + name) {
			t.Errorf("%s HelmOp was not created after registration", name)
		}
	}

	control := store.object("fleet-default/c1-control")
	if got := valueAtPath(t, helmValues(t, control), "scheduler", "config", "manager", "schedulerClusterID"); got != int64(5) {
		t.Errorf("control schedulerClusterID = %v, want 5", got)
	}
	data := store.object("fleet-default/c1-data")
	for _, component := range []string{"seedClient", "client"} {
		if got := valueAtPath(t, helmValues(t, data), component, "host", "schedulerClusterID"); got != int64(5) {
			t.Errorf("data %s schedulerClusterID = %v, want 5", component, got)
		}
	}

	links := registrar.linkCalls()
	if len(links) != 1 || links[0].seedPeerID != 7 || links[0].schedulerID != 5 {
		t.Errorf("links = %+v, want seed 7 -> scheduler 5", links)
	}
	if got := clusterStore.condition("Registered"); got == nil || got.Status != corev1.ConditionTrue {
		t.Errorf("Registered condition = %+v, want True", got)
	}
	if got := clusterStore.condition("IdMismatch"); got == nil || got.Status != corev1.ConditionFalse {
		t.Errorf("IdMismatch condition = %+v, want False", got)
	}
}

func TestReconcileUserIDPreservedAndMismatch(t *testing.T) {
	controller, store, events := newTestController(t)
	registrar := controller.registrar.(*fakeRegistrar)
	registrar.schedulerID = 5
	registrar.seedPeerID = 7

	cluster := testSeedCluster("42")
	cluster.Annotations[AnnotationSchedulerClusterIDAuto] = "5"
	clusterStore := controller.clusters.(*fakeClusterStore)
	clusterStore.set(cluster)

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}

	if got := clusterStore.annotation(AnnotationSchedulerClusterID); got != "42" {
		t.Errorf("annotation = %q, want user-provided 42", got)
	}
	if got := clusterStore.annotation(AnnotationSchedulerClusterIDAuto); got != "5" {
		t.Errorf("auto marker = %q, want stale 5 (not overwritten)", got)
	}
	if clusterStore.updateCount() != 0 {
		t.Errorf("annotation updates = %d, want 0", clusterStore.updateCount())
	}
	control := store.object("fleet-default/c1-control")
	if got := valueAtPath(t, helmValues(t, control), "scheduler", "config", "manager", "schedulerClusterID"); got != int64(42) {
		t.Errorf("control schedulerClusterID = %v, want annotation 42", got)
	}
	data := store.object("fleet-default/c1-data")
	if got := valueAtPath(t, helmValues(t, data), "seedClient", "host", "schedulerClusterID"); got != int64(42) {
		t.Errorf("data schedulerClusterID = %v, want annotation 42", got)
	}

	if got := clusterStore.condition("Registered"); got == nil || got.Status != corev1.ConditionTrue {
		t.Errorf("Registered condition = %+v, want True", got)
	}
	if got := clusterStore.condition("IdMismatch"); got == nil || got.Status != corev1.ConditionTrue {
		t.Errorf("IdMismatch condition = %+v, want True", got)
	}
	found := false
	for _, event := range events.events {
		if event.Reason == "IdMismatch" {
			found = true
		}
	}
	if !found {
		t.Errorf("events = %+v, want IdMismatch", events.events)
	}
	links := registrar.linkCalls()
	if len(links) != 1 || links[0].seedPeerID != 7 || links[0].schedulerID != 5 {
		t.Errorf("links = %+v, want seed 7 -> server scheduler 5", links)
	}
}

func TestReconcileAutoDBWipeUpdatesAnnotation(t *testing.T) {
	controller, store, _ := newTestController(t)
	registrar := controller.registrar.(*fakeRegistrar)
	registrar.schedulerID = 5
	registrar.seedPeerID = 7

	cluster := testSeedCluster("")
	clusterStore := controller.clusters.(*fakeClusterStore)
	clusterStore.set(cluster)
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile(first) error = %v", err)
	}
	if got := clusterStore.annotation(AnnotationSchedulerClusterID); got != "5" {
		t.Fatalf("annotation = %q, want 5", got)
	}

	// Simulate the next informer delivery after the auto write, then a manager
	// DB wipe that assigns a new server ID.
	cluster.Annotations[AnnotationSchedulerClusterID] = "5"
	cluster.Annotations[AnnotationSchedulerClusterIDAuto] = "5"
	registrar.schedulerID = 9
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile(wipe) error = %v", err)
	}
	if got := clusterStore.annotation(AnnotationSchedulerClusterID); got != "9" {
		t.Errorf("annotation = %q, want updated 9", got)
	}
	if clusterStore.updateCount() != 2 {
		t.Errorf("annotation updates = %d, want 2", clusterStore.updateCount())
	}
	control := store.object("fleet-default/c1-control")
	if got := valueAtPath(t, helmValues(t, control), "scheduler", "config", "manager", "schedulerClusterID"); got != int64(9) {
		t.Errorf("control schedulerClusterID = %v, want 9", got)
	}
	if got := clusterStore.condition("IdMismatch"); got == nil || got.Status != corev1.ConditionFalse {
		t.Errorf("IdMismatch condition = %+v, want False", got)
	}
}

func TestReconcileAutoOwnershipSurvivesRestart(t *testing.T) {
	controller, _, _ := newTestController(t)
	registrar := controller.registrar.(*fakeRegistrar)
	registrar.schedulerID = 5
	registrar.seedPeerID = 7

	cluster := testSeedCluster("")
	clusterStore := controller.clusters.(*fakeClusterStore)
	clusterStore.set(cluster)
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile(first) error = %v", err)
	}
	annotations := clusterStore.annotations()
	if annotations[AnnotationSchedulerClusterID] != "5" || annotations[AnnotationSchedulerClusterIDAuto] != "5" {
		t.Fatalf("auto annotations = %+v, want both 5", annotations)
	}

	// A fresh controller must make the same auto-owned decision using only the
	// durable annotations (no in-memory state).
	restarted, _, _ := newTestController(t)
	restartedRegistrar := restarted.registrar.(*fakeRegistrar)
	restartedRegistrar.schedulerID = 9
	restartedRegistrar.seedPeerID = 10
	restartedCluster := testSeedCluster("")
	for key, value := range annotations {
		restartedCluster.Annotations[key] = value
	}
	restartedStore := restarted.clusters.(*fakeClusterStore)
	restartedStore.set(restartedCluster)

	if _, err := restarted.reconcile("", restartedCluster); err != nil {
		t.Fatalf("reconcile(restart) error = %v", err)
	}
	if got := restartedStore.annotation(AnnotationSchedulerClusterID); got != "9" {
		t.Errorf("scheduler ID annotation = %q, want 9", got)
	}
	if got := restartedStore.annotation(AnnotationSchedulerClusterIDAuto); got != "9" {
		t.Errorf("auto marker = %q, want 9", got)
	}
	if got := restartedStore.condition("IdMismatch"); got == nil || got.Status != corev1.ConditionFalse {
		t.Errorf("IdMismatch condition = %+v, want False", got)
	}
}

func TestReconcileAutoSetOnce(t *testing.T) {
	controller, _, _ := newTestController(t)
	registrar := controller.registrar.(*fakeRegistrar)
	registrar.schedulerID = 5
	registrar.seedPeerID = 7

	cluster := testSeedCluster("")
	clusterStore := controller.clusters.(*fakeClusterStore)
	clusterStore.set(cluster)
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile(first) error = %v", err)
	}
	if clusterStore.updateCount() != 1 {
		t.Fatalf("annotation updates = %d, want 1", clusterStore.updateCount())
	}

	cluster.Annotations[AnnotationSchedulerClusterID] = "5"
	cluster.Annotations[AnnotationSchedulerClusterIDAuto] = "5"
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile(second) error = %v", err)
	}
	if clusterStore.updateCount() != 1 {
		t.Errorf("annotation updates = %d, want 1 (set-once)", clusterStore.updateCount())
	}
	if got := clusterStore.annotation(AnnotationSchedulerClusterID); got != "5" {
		t.Errorf("annotation = %q, want 5", got)
	}
}

func TestReconcileRegistrationFailureKeepsManagerAndKeyDB(t *testing.T) {
	controller, store, events := newTestController(t)
	registrar := controller.registrar.(*fakeRegistrar)
	registrar.err = managerapi.ErrUnauthorized

	cluster := testSeedCluster("")
	cluster.Annotations[AnnotationManager] = "true"
	clusterStore := controller.clusters.(*fakeClusterStore)
	clusterStore.set(cluster)

	if _, err := controller.reconcile("", cluster); !errors.Is(err, managerapi.ErrUnauthorized) {
		t.Fatalf("reconcile() error = %v, want ErrUnauthorized", err)
	}
	for _, name := range []string{"c1-keydb", "c1-manager"} {
		if !store.has("fleet-default/" + name) {
			t.Errorf("%s HelmOp was not created", name)
		}
	}
	for _, name := range []string{"c1-control", "c1-data", "c1-nydus-snapshotter"} {
		if store.has("fleet-default/" + name) {
			t.Errorf("%s HelmOp was created despite registration failure", name)
		}
	}
	if got := clusterStore.condition("Registered"); got == nil || got.Status != corev1.ConditionFalse {
		t.Errorf("Registered condition = %+v, want False", got)
	}
	if got := clusterStore.condition("RegistrationFailed"); got == nil || got.Status != corev1.ConditionTrue {
		t.Errorf("RegistrationFailed condition = %+v, want True", got)
	}
	found := false
	for _, event := range events.events {
		if event.Reason == "RegistrationFailed" {
			found = true
		}
	}
	if !found {
		t.Errorf("events = %+v, want RegistrationFailed", events.events)
	}
}

func TestReconcileRegistrationRetryCondition(t *testing.T) {
	controller, store, events := newTestController(t)
	controller.registrar.(*fakeRegistrar).err = &managerapi.HTTPError{
		StatusCode: 500, Status: "500 Internal Server Error", Body: "boom",
	}

	cluster := testSeedCluster("")
	clusterStore := controller.clusters.(*fakeClusterStore)
	clusterStore.set(cluster)

	if _, err := controller.reconcile("", cluster); err == nil {
		t.Fatal("reconcile() error = nil, want manager error")
	}
	if !store.has("fleet-default/c1-keydb") {
		t.Error("KeyDB HelmOp was not created")
	}
	for _, name := range []string{"c1-control", "c1-data", "c1-nydus-snapshotter"} {
		if store.has("fleet-default/" + name) {
			t.Errorf("%s HelmOp was created despite registration failure", name)
		}
	}
	if got := clusterStore.condition("Retrying"); got == nil || got.Status != corev1.ConditionTrue {
		t.Errorf("Retrying condition = %+v, want True", got)
	}
	if got := clusterStore.condition("RegistrationFailed"); got == nil || got.Status != corev1.ConditionFalse {
		t.Errorf("RegistrationFailed condition = %+v, want False", got)
	}
	found := false
	for _, event := range events.events {
		if event.Reason == "Retrying" {
			found = true
		}
	}
	if !found {
		t.Errorf("events = %+v, want Retrying", events.events)
	}
}

func TestHelmopClusterReplicas(t *testing.T) {
	controller := &Controller{}
	cluster := &provv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				AnnotationSchedulerReplicas:  "2",
				AnnotationSeedClientReplicas: "3",
			},
		},
	}
	hc := controller.helmopCluster(cluster)
	if hc.SchedulerReplicas != helmop.ReplicasTemplate(AnnotationSchedulerReplicas) {
		t.Errorf("scheduler replicas = %q, want template", hc.SchedulerReplicas)
	}
	if hc.SeedClientReplicas != helmop.ReplicasTemplate(AnnotationSeedClientReplicas) {
		t.Errorf("seed client replicas = %q, want template", hc.SeedClientReplicas)
	}

	cluster.Annotations[AnnotationSchedulerReplicas] = "bad"
	cluster.Annotations[AnnotationSeedClientReplicas] = "0"
	hc = controller.helmopCluster(cluster)
	if hc.SchedulerReplicas != "1" || hc.SeedClientReplicas != "1" {
		t.Errorf("invalid replicas = %q/%q, want 1/1", hc.SchedulerReplicas, hc.SeedClientReplicas)
	}
}

type fakeLink struct {
	seedPeerID  int64
	schedulerID int64
}

type fakeRegistrar struct {
	mu          sync.Mutex
	schedulerID int64
	seedPeerID  int64
	err         error
	linkErr     error
	links       []fakeLink
}

func newFakeRegistrar() *fakeRegistrar {
	return &fakeRegistrar{schedulerID: 42, seedPeerID: 43}
}

func (f *fakeRegistrar) EnsureSchedulerCluster(_ context.Context, req managerapi.SchedulerClusterRequest) (*managerapi.SchedulerCluster, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return &managerapi.SchedulerCluster{ID: f.schedulerID, Name: req.Name}, nil
}

func (f *fakeRegistrar) EnsureSeedPeerCluster(_ context.Context, req managerapi.SeedPeerClusterRequest) (*managerapi.SeedPeerCluster, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return &managerapi.SeedPeerCluster{ID: f.seedPeerID, Name: req.Name}, nil
}

func (f *fakeRegistrar) LinkSeedPeerCluster(_ context.Context, seedPeerID, schedulerID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.linkErr != nil {
		return f.linkErr
	}
	f.links = append(f.links, fakeLink{seedPeerID: seedPeerID, schedulerID: schedulerID})
	return nil
}

func (f *fakeRegistrar) linkCalls() []fakeLink {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeLink, len(f.links))
	copy(out, f.links)
	return out
}

type fakeSchedulerClusterLister struct {
	clusters []managerapi.SchedulerCluster
}

func (f *fakeSchedulerClusterLister) ListSchedulerClusters(_ context.Context) ([]managerapi.SchedulerCluster, error) {
	return f.clusters, nil
}

func newFakeAllocator() *registry.Allocator {
	return registry.New(&fakeSchedulerClusterLister{})
}

type fakeClusterStore struct {
	mu            sync.Mutex
	cluster       *provv1.Cluster
	updates       int
	statusUpdates int
}

func newFakeClusterStore() *fakeClusterStore {
	return &fakeClusterStore{}
}

func (f *fakeClusterStore) set(cluster *provv1.Cluster) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cluster = cluster.DeepCopy()
}

func (f *fakeClusterStore) Update(cluster *provv1.Cluster) (*provv1.Cluster, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	f.cluster = cluster.DeepCopy()
	return f.cluster.DeepCopy(), nil
}

func (f *fakeClusterStore) UpdateStatus(cluster *provv1.Cluster) (*provv1.Cluster, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusUpdates++
	if f.cluster == nil {
		f.cluster = cluster.DeepCopy()
	} else {
		f.cluster.Status = *cluster.Status.DeepCopy()
	}
	return f.cluster.DeepCopy(), nil
}

func (f *fakeClusterStore) annotation(key string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cluster == nil {
		return ""
	}
	return f.cluster.Annotations[key]
}

func (f *fakeClusterStore) annotations() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cluster == nil {
		return nil
	}
	out := make(map[string]string, len(f.cluster.Annotations))
	for key, value := range f.cluster.Annotations {
		out[key] = value
	}
	return out
}

func (f *fakeClusterStore) updateCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.updates
}

func (f *fakeClusterStore) condition(name string) *genericcondition.GenericCondition {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cluster == nil {
		return nil
	}
	for i := range f.cluster.Status.Conditions {
		if f.cluster.Status.Conditions[i].Type == name {
			return &f.cluster.Status.Conditions[i]
		}
	}
	return nil
}

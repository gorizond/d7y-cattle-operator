package cluster

import (
	"context"
	"testing"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
)

func TestReconcileSeedMissingSchedulerClusterID(t *testing.T) {
	controller, store, events := newTestController(t)
	cluster := testSeedCluster("")

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}

	if !store.has("fleet-default/c1-keydb") {
		t.Error("KeyDB HelmOp was not created")
	}
	for _, name := range []string{"c1-control", "c1-data", "c1-nydus-snapshotter"} {
		if store.has("fleet-default/" + name) {
			t.Errorf("%s was created without a scheduler cluster ID", name)
		}
	}
	if len(events.events) != 1 || events.events[0].Reason != "SchedulerClusterIDMissing" {
		t.Errorf("events = %+v, want one SchedulerClusterIDMissing event", events.events)
	}
}

func TestReconcileManagerContinuesWhenSchedulerClusterIDMissing(t *testing.T) {
	controller, store, events := newTestController(t)
	cluster := testSeedCluster("")
	cluster.Annotations[AnnotationManager] = "true"

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	for _, name := range []string{"c1-keydb", "c1-manager"} {
		if !store.has("fleet-default/" + name) {
			t.Errorf("%s HelmOp was not created", name)
		}
	}
	for _, name := range []string{"c1-control", "c1-data", "c1-nydus-snapshotter"} {
		if store.has("fleet-default/" + name) {
			t.Errorf("%s was created without a scheduler cluster ID", name)
		}
	}
	if len(events.events) != 1 || events.events[0].Reason != "SchedulerClusterIDMissing" {
		t.Errorf("events = %+v, want one SchedulerClusterIDMissing event", events.events)
	}
}

func TestReconcileSeedCreatesHelmOps(t *testing.T) {
	controller, store, events := newTestController(t)
	cluster := testSeedCluster("42")

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	if len(events.events) != 0 {
		t.Errorf("events = %+v, want none", events.events)
	}

	for _, name := range []string{"c1-keydb", "c1-control", "c1-data", "c1-nydus-snapshotter"} {
		if !store.has("fleet-default/" + name) {
			t.Errorf("%s HelmOp was not created", name)
		}
	}

	control := store.object("fleet-default/c1-control")
	if got := dependsOn(control); len(got) != 1 || got[0] != "c1-keydb" {
		t.Errorf("control dependsOn = %v", got)
	}
	data := store.object("fleet-default/c1-data")
	if got := dependsOn(data); len(got) != 2 || got[0] != "c1-control" || got[1] != "c1-keydb" {
		t.Errorf("data dependsOn = %v", got)
	}
	nydus := store.object("fleet-default/c1-nydus-snapshotter")
	if got := dependsOn(nydus); len(got) != 1 || got[0] != "c1-keydb" {
		t.Errorf("nydus dependsOn = %v", got)
	}

	values := helmValues(t, data)
	if got := valueAtPath(t, values, "externalManager", "host"); got != "" {
		t.Errorf("data externalManager.host = %v, want empty", got)
	}
	for _, component := range []string{"seedClient", "client"} {
		if got := valueAtPath(t, values, component, "host", "schedulerClusterID"); got != int64(42) {
			t.Errorf("%s schedulerClusterID = %v", component, got)
		}
	}
}

func TestReconcileSeedRemovalAndLabelRemoval(t *testing.T) {
	controller, store, _ := newTestController(t)
	cluster := testSeedCluster("42")
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile(seed) error = %v", err)
	}

	cluster.Annotations = nil
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile(seed removed) error = %v", err)
	}
	for _, name := range []string{"c1-control", "c1-data", "c1-nydus-snapshotter"} {
		if store.has("fleet-default/" + name) {
			t.Errorf("%s was not deleted when the seed role was removed", name)
		}
	}
	if !store.has("fleet-default/c1-keydb") {
		t.Error("KeyDB was deleted with the seed role")
	}

	cluster.Labels = nil
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile(label removed) error = %v", err)
	}
	if len(store.objects) != 0 {
		t.Errorf("objects left after label removal: %v", store.names())
	}
}

func newTestController(t *testing.T) (*Controller, *fakeHelmOpStore, *fakeEventStore) {
	t.Helper()
	secrets := newFakeSecretStore()
	secrets.put(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "dragonfly-p2p-mtls", Namespace: "dragonfly-system"},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{"tls.crt": []byte("cert")},
	})
	store := newFakeHelmOpStore()
	events := &fakeEventStore{}
	controller := &Controller{
		ctx:     context.Background(),
		secrets: secrets,
		events:  events,
		helmops: store,
		opts: Options{
			OperatorNamespace: "dragonfly-system",
			MTLSSecretName:    "dragonfly-p2p-mtls",
			ManagerGRPCDomain: "grpc.example.com",
			DragonflyChart:    helmop.Chart{Repo: "https://charts.example.com", Name: "dragonfly", Version: "1.10.1"},
		},
	}
	return controller, store, events
}

func testSeedCluster(schedulerID string) *provv1.Cluster {
	annotations := map[string]string{AnnotationSeed: "true"}
	if schedulerID != "" {
		annotations[AnnotationSchedulerClusterID] = schedulerID
	}
	return &provv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "c1",
			Namespace:   "fleet-default",
			Labels:      map[string]string{LabelEnabled: "true"},
			Annotations: annotations,
		},
	}
}

func dependsOn(op *unstructured.Unstructured) []string {
	spec := op.Object["spec"].(map[string]any)
	raw, _ := spec["dependsOn"].([]any)
	names := make([]string, 0, len(raw))
	for _, item := range raw {
		names = append(names, item.(map[string]any)["name"].(string))
	}
	return names
}

func helmValues(t *testing.T, op *unstructured.Unstructured) map[string]any {
	t.Helper()
	spec := op.Object["spec"].(map[string]any)
	helm := spec["helm"].(map[string]any)
	return helm["values"].(map[string]any)
}

func valueAtPath(t *testing.T, value any, keys ...string) any {
	t.Helper()
	current := value
	for _, key := range keys {
		m, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("value at %v is %T, want map[string]any", keys, current)
		}
		current, ok = m[key]
		if !ok {
			t.Fatalf("missing key %q in %v", key, keys)
		}
	}
	return current
}

type fakeEventStore struct {
	events []*corev1.Event
}

func (f *fakeEventStore) Create(event *corev1.Event) (*corev1.Event, error) {
	f.events = append(f.events, event)
	return event, nil
}

type fakeHelmOpStore struct {
	objects map[string]*unstructured.Unstructured
}

func newFakeHelmOpStore() *fakeHelmOpStore {
	return &fakeHelmOpStore{objects: map[string]*unstructured.Unstructured{}}
}

func (f *fakeHelmOpStore) has(key string) bool {
	_, ok := f.objects[key]
	return ok
}

func (f *fakeHelmOpStore) object(key string) *unstructured.Unstructured {
	return f.objects[key]
}

func (f *fakeHelmOpStore) names() []string {
	names := make([]string, 0, len(f.objects))
	for key := range f.objects {
		names = append(names, key)
	}
	return names
}

func (f *fakeHelmOpStore) Get(_ context.Context, namespace, name string, result runtime.Object, _ metav1.GetOptions) error {
	stored, ok := f.objects[namespace+"/"+name]
	if !ok {
		return apierrors.NewNotFound(schema.GroupResource{Resource: "helmops"}, name)
	}
	result.(*unstructured.Unstructured).Object = runtime.DeepCopyJSON(stored.Object)
	return nil
}

func (f *fakeHelmOpStore) Create(_ context.Context, namespace string, obj, result runtime.Object, _ metav1.CreateOptions) error {
	created := obj.(*unstructured.Unstructured).DeepCopy()
	f.objects[namespace+"/"+created.GetName()] = created
	result.(*unstructured.Unstructured).Object = runtime.DeepCopyJSON(created.Object)
	return nil
}

func (f *fakeHelmOpStore) Update(_ context.Context, namespace string, obj, result runtime.Object, _ metav1.UpdateOptions) error {
	updated := obj.(*unstructured.Unstructured).DeepCopy()
	f.objects[namespace+"/"+updated.GetName()] = updated
	result.(*unstructured.Unstructured).Object = runtime.DeepCopyJSON(updated.Object)
	return nil
}

func (f *fakeHelmOpStore) Delete(_ context.Context, namespace, name string, _ metav1.DeleteOptions) error {
	delete(f.objects, namespace+"/"+name)
	return nil
}

type fakeSecretStore struct {
	secrets map[string]*corev1.Secret
}

func newFakeSecretStore() *fakeSecretStore {
	return &fakeSecretStore{secrets: map[string]*corev1.Secret{}}
}

func (f *fakeSecretStore) put(secret *corev1.Secret) {
	f.secrets[secret.Namespace+"/"+secret.Name] = secret.DeepCopy()
}

func (f *fakeSecretStore) Get(namespace, name string, _ metav1.GetOptions) (*corev1.Secret, error) {
	secret, ok := f.secrets[namespace+"/"+name]
	if !ok {
		return nil, apierrors.NewNotFound(corev1.Resource("secrets"), name)
	}
	return secret.DeepCopy(), nil
}

func (f *fakeSecretStore) Create(secret *corev1.Secret) (*corev1.Secret, error) {
	key := secret.Namespace + "/" + secret.Name
	if _, ok := f.secrets[key]; ok {
		return nil, apierrors.NewAlreadyExists(corev1.Resource("secrets"), secret.Name)
	}
	f.secrets[key] = secret.DeepCopy()
	return secret.DeepCopy(), nil
}

func (f *fakeSecretStore) Update(secret *corev1.Secret) (*corev1.Secret, error) {
	key := secret.Namespace + "/" + secret.Name
	if _, ok := f.secrets[key]; !ok {
		return nil, apierrors.NewNotFound(corev1.Resource("secrets"), secret.Name)
	}
	f.secrets[key] = secret.DeepCopy()
	return secret.DeepCopy(), nil
}

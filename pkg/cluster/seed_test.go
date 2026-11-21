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
	capiv1beta2 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
)

func TestReconcileSeedCreatesSharedHelmOps(t *testing.T) {
	controller, store, events := newTestController(t)
	cluster := testSeedCluster("42")
	cluster.Annotations[AnnotationSchedulerIPs] = "203.0.113.10"

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	if len(events.events) != 0 {
		t.Errorf("events = %+v, want none", events.events)
	}

	for _, name := range []string{
		helmop.SharedKeyDBName(),
		helmop.SharedControlName(),
		helmop.SharedDataName(),
		helmop.SharedExtrasName(),
		helmop.SharedMirrorName(),
	} {
		if !store.has("fleet-default/" + name) {
			t.Errorf("%s shared HelmOp was not created", name)
		}
	}
	for _, legacy := range []string{
		"c1-keydb", "c1-control", "c1-data", "c1-extras",
	} {
		if store.has("fleet-default/" + legacy) {
			t.Errorf("legacy per-cluster HelmOp %s still exists", legacy)
		}
	}

	control := store.object("fleet-default/" + helmop.SharedControlName())
	if got := selectorLabels(control); got[helmop.LabelGate] != "true" || got[helmop.LabelRoleSeed] != "true" {
		t.Errorf("control selector = %v, want gate+seed", got)
	}
	if got := dependsOn(control); len(got) != 1 || got[0] != helmop.SharedKeyDBName() {
		t.Errorf("control dependsOn = %v, want shared keydb", got)
	}
	data := store.object("fleet-default/" + helmop.SharedDataName())
	if got := dependsOn(data); len(got) != 3 ||
		got[0] != helmop.SharedControlName() ||
		got[1] != helmop.SharedKeyDBName() ||
		got[2] != helmop.SharedExtrasName() {
		t.Errorf("data dependsOn = %v", got)
	}
	mirror := store.object("fleet-default/" + helmop.SharedMirrorName())
	if got := dependsOn(mirror); len(got) != 1 || got[0] != helmop.SharedDataName() {
		t.Errorf("mirror dependsOn = %v", got)
	}

	keydb := helmValues(t, store.object("fleet-default/"+helmop.SharedKeyDBName()))
	if got := valueAtPath(t, keydb, "fullnameOverride"); got != helmop.SharedClusterNameTemplate+"-keydb" {
		t.Errorf("keydb fullnameOverride = %v", got)
	}
	values := helmValues(t, data)
	if got := valueAtPath(t, values, "fullnameOverride"); got != helmop.SharedClusterNameTemplate+"-data" {
		t.Errorf("data fullnameOverride = %v", got)
	}
	if got := valueAtPath(t, values, "externalManager", "host"); got != "" {
		t.Errorf("data externalManager.host = %v, want empty", got)
	}
	for _, component := range []string{"seedClient", "client"} {
		if got := valueAtPath(t, values, component, "config", "host", "schedulerClusterID"); got != helmop.SharedSchedulerClusterIDTemplate {
			t.Errorf("%s schedulerClusterID = %v, want Fleet template", component, got)
		}
	}
	wantAddr := helmop.SchedulerHostTemplate("example.com") + ":443"
	if got := valueAtPath(t, values, "seedClient", "dynconfig", "scheduler", "addr"); got != wantAddr {
		t.Errorf("seedClient dynconfig addr = %v, want %v", got, wantAddr)
	}
}

func TestReconcileStampsClusterIDAndPublicIP(t *testing.T) {
	controller, _, _ := newTestController(t)
	cluster := testSeedCluster("42")
	clusterStore := controller.clusters.(*fakeClusterStore)
	clusterStore.set(cluster)

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	if got := clusterStore.label(helmop.LabelClusterID); got != "c-m-test" {
		t.Errorf("cluster-id label = %q, want c-m-test", got)
	}
	if got := clusterStore.annotation(AnnotationSchedulerIPs); got != "203.0.113.10" {
		t.Errorf("scheduler public IP annotation = %q, want the resolved IP", got)
	}
	if got := clusterStore.label(helmop.LabelRoleSeed); got != "" {
		t.Errorf("role label %s = %q, want untouched", helmop.LabelRoleSeed, got)
	}
}

func TestReconcileSeedMirrorIsDefaultOnly(t *testing.T) {
	controller, store, _ := newTestController(t)
	cluster := testSeedCluster("42")

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	mirror := store.object("fleet-default/" + helmop.SharedMirrorName())
	if mirror == nil {
		t.Fatal("shared mirror HelmOp was not created")
	}
	if got := dependsOn(mirror); len(got) != 1 || got[0] != helmop.SharedDataName() {
		t.Errorf("mirror dependsOn = %v, want shared data only", got)
	}
	values := helmValues(t, mirror)
	if _, ok := values["proxy"]; ok {
		t.Errorf("mirror values contain proxy: %v", values["proxy"])
	}
	if got := valueAtPath(t, values, "port"); got != int64(4001) {
		t.Errorf("mirror port = %v, want 4001", got)
	}

}

func TestReconcileSeedDeletesLegacyNydusHelmOp(t *testing.T) {
	controller, store, _ := newTestController(t)
	cluster := testSeedCluster("42")
	legacy := helmop.LegacyNydusName(cluster.Name)
	store.objects["fleet-default/"+legacy] = &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "fleet.cattle.io/v1alpha1",
		"kind":       "HelmOp",
		"metadata": map[string]any{
			"name":      legacy,
			"namespace": "fleet-default",
		},
	}}

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	if store.has("fleet-default/" + legacy) {
		t.Error("legacy in-cluster nydus HelmOp was not deleted")
	}
	for _, name := range []string{helmop.SharedControlName(), helmop.SharedDataName(), helmop.SharedMirrorName()} {
		if !store.has("fleet-default/" + name) {
			t.Errorf("%s shared HelmOp was not created", name)
		}
	}
}

func TestReconcileSeedRemovalAndLabelRemoval(t *testing.T) {
	controller, store, _ := newTestController(t)
	cluster := testSeedCluster("42")
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile(seed) error = %v", err)
	}

	delete(cluster.Annotations, AnnotationSeed)
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile(seed removed) error = %v", err)
	}
	if got := cluster.Annotations[AnnotationSchedulerClusterID]; got != "42" {
		t.Errorf("scheduler cluster ID annotation = %q after seed removal, want 42", got)
	}
	// The shared objects are global: removing the seed role must not delete
	// them for every other cluster. Only legacy per-cluster names are removed.
	for _, name := range []string{helmop.SharedControlName(), helmop.SharedDataName(), helmop.SharedMirrorName()} {
		if !store.has("fleet-default/" + name) {
			t.Errorf("%s shared HelmOp was deleted with the seed role", name)
		}
	}
	for _, name := range []string{"c1-control", "c1-data", "c1-containerd-mirror"} {
		if store.has("fleet-default/" + name) {
			t.Errorf("legacy %s was not deleted", name)
		}
	}
	if !store.has("fleet-default/" + helmop.SharedKeyDBName()) {
		t.Error("shared KeyDB was deleted with the seed role")
	}

	cluster.Labels = nil
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile(label removed) error = %v", err)
	}
	// cleanupAll removes the per-cluster objects; shared objects stay because
	// Fleet stops targeting the unlabeled cluster.
	for _, name := range []string{"c1-manager", "c1-keydb", "c1-control", "c1-data", "c1-extras", "c1-containerd-mirror"} {
		if store.has("fleet-default/" + name) {
			t.Errorf("per-cluster HelmOp %s left after label removal", name)
		}
	}
	for _, name := range []string{helmop.SharedKeyDBName(), helmop.SharedControlName(), helmop.SharedDataName(), helmop.SharedExtrasName(), helmop.SharedMirrorName()} {
		if !store.has("fleet-default/" + name) {
			t.Errorf("%s shared HelmOp was deleted with the gate label", name)
		}
	}
}

func newTestController(t *testing.T) (*Controller, *fakeHelmOpStore, *fakeEventStore) {
	t.Helper()
	secrets := newFakeSecretStore()
	secrets.put(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "dragonfly-p2p-mtls", Namespace: "dragonfly-system"},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{"ca.crt": []byte("ca"), "tls.crt": []byte("cert")},
	})
	store := newFakeHelmOpStore()
	bundles := newFakeHelmOpStore()
	events := &fakeEventStore{}
	controller := &Controller{
		ctx:        context.Background(),
		secrets:    secrets,
		events:     events,
		helmops:    store,
		bundles:    bundles,
		clusters:   newFakeClusterStore(),
		configmaps: newFakeConfigMapStore(),
		machines:   &fakeMachineCache{machines: []*capiv1beta2.Machine{seedMachine("c1", "c1-0", "203.0.113.10")}},
		registrar:  newFakeRegistrar(),
		allocator:  newFakeAllocator(),
		opts: Options{
			OperatorNamespace:           "dragonfly-system",
			MTLSSecretName:              "dragonfly-p2p-mtls",
			ManagerGRPCDomain:           "grpc.example.com",
			ManagerGRPCPort:             443,
			ContainerdMirrorPort:        4001,
			ManagerAPIHost:              "api.example.com",
			ManagerAPIURL:               "https://manager-api.example.com",
			ManagerAPICredentialsSecret: "manager-api-credentials",
			P2PDomain:                   "example.com",
			DragonflyChart:              helmop.Chart{Repo: "https://charts.example.com", Name: "dragonfly", Version: "1.10.1-patch.2"},
			ExtrasChart:                 helmop.Chart{Repo: "https://charts.example.com", Name: "dragonfly-extras", Version: "0.1.10"},
			MirrorChart:                 helmop.Chart{Repo: "https://charts.example.com", Name: "containerd-mirror", Version: "0.1.0"},
			ExtrasIngressController:     "nginx",
			ExtrasIngressGRPCEnabled:    true,
			ExtrasIngressRESTEnabled:    true,
			ExtrasTraefikTransport:      true,
			VPAEnabled:                  true,
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
		Status: provv1.ClusterStatus{ClusterName: "c-m-test"},
	}
}

func selectorLabels(op *unstructured.Unstructured) map[string]string {
	spec := op.Object["spec"].(map[string]any)
	targets, _ := spec["targets"].([]any)
	if len(targets) == 0 {
		return nil
	}
	selector, _ := targets[0].(map[string]any)["clusterSelector"].(map[string]any)
	matchLabels, _ := selector["matchLabels"].(map[string]any)
	labels := make(map[string]string, len(matchLabels))
	for key, value := range matchLabels {
		labels[key], _ = value.(string)
	}
	return labels
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

func (f *fakeClusterStore) label(key string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cluster == nil {
		return ""
	}
	return f.cluster.Labels[key]
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

func TestHelmopClusterCarriesSchedulerIdentityAndCertRevision(t *testing.T) {
	controller, _, _ := newTestController(t)
	cluster := testSeedCluster("42")

	first := controller.helmopCluster(cluster)
	if first.SchedulerHost != "" {
		t.Errorf("SchedulerHost = %q, want empty until the certify path runs", first.SchedulerHost)
	}
	if first.MTLSCertRevision == "" {
		t.Fatal("MTLSCertRevision is empty")
	}

	controller.secrets.(*fakeSecretStore).put(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "dragonfly-p2p-mtls", Namespace: "dragonfly-system"},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{"ca.crt": []byte("ca"), "tls.crt": []byte("rotated-cert")},
	})
	second := controller.helmopCluster(cluster)
	if second.MTLSCertRevision == first.MTLSCertRevision {
		t.Error("MTLSCertRevision did not change after certificate rotation")
	}
}

func TestEnsureMTLSSecretRequiresCA(t *testing.T) {
	controller, _, events := newTestController(t)
	controller.secrets.(*fakeSecretStore).put(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "dragonfly-p2p-mtls", Namespace: "dragonfly-system"},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{"tls.crt": []byte("cert")},
	})
	cluster := testSeedCluster("42")

	if err := controller.ensureMTLSSecret(cluster); err == nil {
		t.Fatal("ensureMTLSSecret() error = nil, want missing ca.crt error")
	}
	if len(events.events) == 0 || events.events[0].Reason != "MTLSSecretUnavailable" {
		t.Errorf("events = %+v, want MTLSSecretUnavailable", events.events)
	}
}

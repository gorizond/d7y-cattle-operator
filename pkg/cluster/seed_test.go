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

func TestReconcileSeedCreatesHelmOps(t *testing.T) {
	controller, store, events := newTestController(t)
	cluster := testSeedCluster("42")

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	if len(events.events) != 0 {
		t.Errorf("events = %+v, want none", events.events)
	}

	for _, name := range []string{"c1-keydb", "c1-control", "c1-data", "c1-containerd-mirror"} {
		if !store.has("fleet-default/" + name) {
			t.Errorf("%s HelmOp was not created", name)
		}
	}

	control := store.object("fleet-default/c1-control")
	if got := dependsOn(control); len(got) != 1 || got[0] != "c1-keydb" {
		t.Errorf("control dependsOn = %v", got)
	}
	data := store.object("fleet-default/c1-data")
	if got := dependsOn(data); len(got) != 3 || got[0] != "c1-control" || got[1] != "c1-keydb" || got[2] != "c1-extras" {
		t.Errorf("data dependsOn = %v", got)
	}
	mirror := store.object("fleet-default/c1-containerd-mirror")
	if got := dependsOn(mirror); len(got) != 1 || got[0] != "c1-data" {
		t.Errorf("mirror dependsOn = %v", got)
	}

	values := helmValues(t, data)
	if got := valueAtPath(t, values, "externalManager", "host"); got != "" {
		t.Errorf("data externalManager.host = %v, want empty", got)
	}
	for _, component := range []string{"seedClient", "client"} {
		if got := valueAtPath(t, values, component, "config", "host", "schedulerClusterID"); got != int64(42) {
			t.Errorf("%s config.host.schedulerClusterID = %v", component, got)
		}
	}
}

func TestReconcileSeedMirrorIsDefaultOnly(t *testing.T) {
	controller, store, _ := newTestController(t)
	controller.opts.ProxyHarborURL = "https://external.example.com"
	cluster := testSeedCluster("42")
	cluster.Annotations[AnnotationProxy] = "true"

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	mirror := store.object("fleet-default/c1-containerd-mirror")
	if mirror == nil {
		t.Fatal("mirror HelmOp was not created")
	}
	if got := dependsOn(mirror); len(got) != 1 || got[0] != "c1-data" {
		t.Errorf("mirror dependsOn = %v, want data only", got)
	}
	values := helmValues(t, mirror)
	if _, ok := values["proxy"]; ok {
		t.Errorf("mirror values contain proxy: %v", values["proxy"])
	}
	if got := valueAtPath(t, values, "port"); got != int64(4001) {
		t.Errorf("mirror port = %v, want 4001", got)
	}
	if !store.has("fleet-default/c1-registry-proxy") {
		t.Error("proxy HelmOp was not created independently of the mirror")
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
	for _, name := range []string{"c1-control", "c1-data", "c1-containerd-mirror"} {
		if !store.has("fleet-default/" + name) {
			t.Errorf("%s HelmOp was not created", name)
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
	for _, name := range []string{"c1-control", "c1-data", "c1-containerd-mirror"} {
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
		Data:       map[string][]byte{"ca.crt": []byte("ca"), "tls.crt": []byte("cert")},
	})
	secrets.put(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "harbor-admin", Namespace: "dragonfly-system"},
		Data:       map[string][]byte{"HARBOR_ADMIN_PASSWORD": []byte("admin-password")},
	})
	store := newFakeHelmOpStore()
	bundles := newFakeHelmOpStore()
	events := &fakeEventStore{}
	controller := &Controller{
		ctx:       context.Background(),
		secrets:   secrets,
		events:    events,
		helmops:   store,
		bundles:   bundles,
		clusters:  newFakeClusterStore(),
		registrar: newFakeRegistrar(),
		allocator: newFakeAllocator(),
		opts: Options{
			OperatorNamespace:        "dragonfly-system",
			MTLSSecretName:           "dragonfly-p2p-mtls",
			ManagerGRPCDomain:        "grpc.example.com",
			ManagerGRPCPort:          443,
			ManagerAPIHost:           "api.example.com",
			ManagerAPIURL:            "https://manager-api.example.com",
			P2PDomain:                "example.com",
			Resolver:                 &fakeResolver{ips: []string{"203.0.113.10"}},
			DragonflyChart:           helmop.Chart{Repo: "https://charts.example.com", Name: "dragonfly", Version: "1.10.1-patch.2"},
			ExtrasChart:              helmop.Chart{Repo: "https://charts.example.com", Name: "dragonfly-extras", Version: "0.1.8"},
			MirrorChart:              helmop.Chart{Repo: "https://charts.example.com", Name: "containerd-mirror", Version: "0.1.0"},
			HarborChart:              helmop.Chart{Repo: "https://helm.example.com", Name: "harbor", Version: "1.19.4"},
			RegistryProxyChart:       helmop.Chart{Repo: "https://charts.example.com", Name: "registry-proxy", Version: "0.1.0"},
			HarborIngressClass:       "traefik",
			ProxyDomain:              "proxy.example.com",
			ProxyBasePath:            "/proxy",
			ProxyTLSSecret:           "poligon-harbor-tls",
			ProxyHarborAdminSecret:   "harbor-admin",
			ProxyRegistries:          []string{"docker.io"},
			ExtrasIngressController:  "nginx",
			ExtrasIngressGRPCEnabled: true,
			ExtrasIngressRESTEnabled: true,
			ExtrasTraefikTransport:   true,
			VPAEnabled:               true,
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

type fakeResolver struct {
	ips   []string
	err   error
	hosts []string
}

func (f *fakeResolver) LookupHost(_ context.Context, host string) ([]string, error) {
	f.hosts = append(f.hosts, host)
	return f.ips, f.err
}

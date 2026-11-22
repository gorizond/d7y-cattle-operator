package cluster

import (
	"context"
	"strings"
	"testing"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	capiv1beta2 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"

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
		t.Errorf("control dependsOn names = %v, want shared keydb", got)
	}
	refs := dependsOnRefs(control)
	if len(refs) != 2 || refs[1].Selector == nil || refs[1].Selector.MatchLabels[helmop.SchedulerCertLabel] != "true" {
		t.Errorf("control dependsOn = %+v, want scheduler cert selector", refs)
	}
	if got := downstreamSecretNames(control); len(got) != 1 || got[0] != "dragonfly-p2p-mtls" {
		t.Errorf("control downstreamSecretNames = %v, want the client Secret", got)
	}
	data := store.object("fleet-default/" + helmop.SharedDataName())
	if got := dependsOn(data); len(got) != 3 ||
		got[0] != helmop.SharedControlName() ||
		got[1] != helmop.SharedKeyDBName() ||
		got[2] != helmop.SharedExtrasName() {
		t.Errorf("data dependsOn = %v", got)
	}
	if got := downstreamSecretNames(data); len(got) != 0 {
		t.Errorf("data downstreamSecretNames = %v, want none: control owns the client Secret", got)
	}
	hostSync := store.object("fleet-default/" + helmop.HostSyncName("c1"))
	if hostSync == nil {
		t.Fatal("host-sync HelmOp was not created")
	}
	if got := dependsOn(hostSync); len(got) != 1 || got[0] != helmop.SharedControlName() {
		t.Errorf("host-sync dependsOn = %v, want shared control", got)
	}
	if got := downstreamSecretNames(hostSync); len(got) != 0 {
		t.Errorf("host-sync downstreamSecretNames = %v, want none: control owns the client Secret", got)
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
	templateValues, found, err := unstructured.NestedStringMap(data.Object, "spec", "helm", "templateValues")
	if err != nil || !found {
		t.Fatalf("data HelmOp templateValues = %v, found=%v, err=%v", templateValues, found, err)
	}
	if got := templateValues["seedClient"]; !strings.Contains(got, "addrs:") || !strings.Contains(got, `:8002`) || !strings.Contains(got, "scheduler-ips") {
		t.Errorf("seedClient dynconfig template = %q, want scheduler addrs on :8002", got)
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
	for _, name := range []string{"c1-manager", "c1-keydb", "c1-control", "c1-data", "c1-extras", "c1-containerd-mirror", helmop.SchedulerCertName("c1")} {
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
		ObjectMeta: metav1.ObjectMeta{Name: "c1-kubeconfig", Namespace: "fleet-default"},
		Data:       map[string][]byte{"value": []byte("fake-kubeconfig")},
	})
	store := newFakeHelmOpStore()
	bundles := newFakeHelmOpStore()
	events := &fakeEventStore{}
	controller := &Controller{
		ctx:         context.Background(),
		secrets:     secrets,
		secretCache: secrets,
		events:      events,
		helmops:     store,
		bundles:     bundles,
		clusters:    newFakeClusterStore(),
		configmaps:  newFakeConfigMapStore(),
		machines:    &fakeMachineCache{machines: []*capiv1beta2.Machine{seedMachine("c1", "c1-0", "203.0.113.10")}},
		downstream:  &fakeSchedulerPodLister{pods: []schedulerPod{{NodeName: "c1-node", HostIP: "203.0.113.10"}}},
		registrar:   newFakeRegistrar(),
		allocator:   newFakeAllocator(),
		opts: Options{
			OperatorNamespace:            "dragonfly-system",
			MTLSSecretName:               "dragonfly-p2p-mtls",
			ManagerGRPCDomain:            "grpc.example.com",
			ManagerGRPCPort:              443,
			ContainerdMirrorPort:         4001,
			ManagerAPIHost:               "api.example.com",
			ManagerAPIURL:                "https://manager-api.example.com",
			ManagerAPICredentialsSecret:  "manager-api-credentials",
			P2PDomain:                    "example.com",
			DragonflyChart:               helmop.Chart{Repo: "https://charts.example.com", Name: "dragonfly", Version: "1.10.1-patch.2"},
			ExtrasChart:                  helmop.Chart{Repo: "https://charts.example.com", Name: "dragonfly-extras", Version: "0.1.11"},
			MirrorChart:                  helmop.Chart{Repo: "https://charts.example.com", Name: "containerd-mirror", Version: "0.1.0"},
			HostSyncChart:                helmop.Chart{Repo: "https://charts.example.com", Name: "p2p-host-sync", Version: "0.1.0"},
			SchedulerCertChart:           helmop.Chart{Repo: "https://charts.example.com", Name: "p2p-scheduler-cert", Version: "0.1.0"},
			StaticChart:                  helmop.Chart{Repo: "https://charts.example.com", Name: "p2p-static", Version: "0.1.11"},
			StaticReleaseName:            "p2p-static",
			StaticClientRepo:             "dragonflyoss/client",
			StaticNydusRepo:              "dragonflyoss/nydus",
			StaticSnapshotterRepo:        "containerd/nydus-snapshotter",
			ClientVersion:                "v1.5.14",
			NydusVersion:                 "v2.4.5",
			SnapshotterVersion:           "v0.16.0",
			ClientSHA256AMD64:            "client-amd64",
			ClientSHA256ARM64:            "client-arm64",
			NydusSHA256AMD64:             "nydus-amd64",
			NydusSHA256ARM64:             "nydus-arm64",
			SnapshotterSHA256AMD64:       "snapshotter-amd64",
			SnapshotterSHA256ARM64:       "snapshotter-arm64",
			SnapshotterBinarySHA256AMD64: "snapshotter-bin-amd64",
			SnapshotterBinarySHA256ARM64: "snapshotter-bin-arm64",
			StaticURL:                    "https://static.example.com",
			ExtrasIngressController:      "nginx",
			ExtrasIngressGRPCEnabled:     true,
			ExtrasIngressRESTEnabled:     true,
			ExtrasTraefikTransport:       true,
			VPAEnabled:                   true,
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

func downstreamSecretNames(op *unstructured.Unstructured) []string {
	raw, _, _ := unstructured.NestedSlice(op.Object, "spec", "downstreamResources")
	names := make([]string, 0, len(raw))
	for _, item := range raw {
		resource, _ := item.(map[string]any)
		kind, _ := resource["kind"].(string)
		name, _ := resource["name"].(string)
		if kind == "Secret" {
			names = append(names, name)
		}
	}
	return names
}

func dependsOn(op *unstructured.Unstructured) []string {
	refs := dependsOnRefs(op)
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref.Name != "" {
			names = append(names, ref.Name)
		}
	}
	return names
}

func dependsOnRefs(op *unstructured.Unstructured) []fleetv1alpha1.BundleRef {
	spec := op.Object["spec"].(map[string]any)
	raw, _ := spec["dependsOn"].([]any)
	refs := make([]fleetv1alpha1.BundleRef, 0, len(raw))
	for _, item := range raw {
		ref := fleetv1alpha1.BundleRef{}
		rawRef, _ := item.(map[string]any)
		ref.Name, _ = rawRef["name"].(string)
		if selector, ok := rawRef["selector"].(map[string]any); ok {
			labels, _ := selector["matchLabels"].(map[string]any)
			ref.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{}}
			for key, value := range labels {
				ref.Selector.MatchLabels[key], _ = value.(string)
			}
		}
		refs = append(refs, ref)
	}
	return refs
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
	secrets   map[string]*corev1.Secret
	deleteErr error
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

func (f *fakeSecretStore) List(namespace string, selector labels.Selector) ([]*corev1.Secret, error) {
	result := make([]*corev1.Secret, 0, len(f.secrets))
	for key, secret := range f.secrets {
		if secret.Namespace != namespace {
			continue
		}
		if selector != nil && !selector.Matches(labels.Set(secret.Labels)) {
			continue
		}
		result = append(result, secret.DeepCopy())
		_ = key
	}
	return result, nil
}

func (f *fakeSecretStore) Delete(namespace, name string, _ *metav1.DeleteOptions) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	key := namespace + "/" + name
	if _, ok := f.secrets[key]; !ok {
		return apierrors.NewNotFound(corev1.Resource("secrets"), name)
	}
	delete(f.secrets, key)
	return nil
}

func TestHelmopClusterCarriesSnapshotterBinaryChecksums(t *testing.T) {
	controller, _, _ := newTestController(t)
	hc := controller.helmopCluster(testSeedCluster("42"))
	if hc.StaticSnapshotterBinarySHA256AMD64 != "snapshotter-bin-amd64" {
		t.Errorf("StaticSnapshotterBinarySHA256AMD64 = %q", hc.StaticSnapshotterBinarySHA256AMD64)
	}
	if hc.StaticSnapshotterBinarySHA256ARM64 != "snapshotter-bin-arm64" {
		t.Errorf("StaticSnapshotterBinarySHA256ARM64 = %q", hc.StaticSnapshotterBinarySHA256ARM64)
	}
}

func TestHelmopClusterCarriesSchedulerIdentityAndCertRevision(t *testing.T) {
	controller, _, _ := newTestController(t)
	cluster := testSeedCluster("42")
	cluster.Annotations[AnnotationManager] = "true"
	controller.clusters.(*fakeClusterStore).set(cluster)
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}

	first := controller.helmopCluster(cluster)
	if first.MTLSCertRevision == "" || first.ManagerCertRevision == "" || first.SchedulerCertRevision == "" {
		t.Fatalf("certificate revisions = client %q manager %q scheduler %q", first.MTLSCertRevision, first.ManagerCertRevision, first.SchedulerCertRevision)
	}
	client, _ := controller.secrets.Get("dragonfly-system", "dragonfly-p2p-mtls", metav1.GetOptions{})
	client.Data[corev1.TLSCertKey] = []byte("rotated-cert")
	controller.secrets.(*fakeSecretStore).put(client)
	second := controller.helmopCluster(cluster)
	if second.MTLSCertRevision == first.MTLSCertRevision {
		t.Error("MTLSCertRevision did not change after client certificate rotation")
	}
}

func hasEventReason(events []*corev1.Event, reason string) bool {
	for _, event := range events {
		if event.Reason == reason {
			return true
		}
	}
	return false
}

func TestSeedHostSyncFollowsSchedulerIPs(t *testing.T) {
	controller, store, _ := newTestController(t)
	cluster := testSeedCluster("42")
	controller.machines = &fakeMachineCache{machines: []*capiv1beta2.Machine{
		seedMachine("c1", "c1-0", "203.0.113.10"),
		seedMachine("c1", "c1-1", "203.0.113.11"),
	}}
	controller.downstream = &fakeSchedulerPodLister{pods: []schedulerPod{
		{NodeName: "c1-node", HostIP: "203.0.113.10"},
		{NodeName: "c1-node-2", HostIP: "203.0.113.11"},
	}}
	controller.clusters.(*fakeClusterStore).set(cluster)
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	if !store.has("fleet-default/" + helmop.HostSyncName("c1")) {
		t.Fatal("host-sync HelmOp was not created")
	}
	op := store.object("fleet-default/" + helmop.HostSyncName("c1"))
	if got := dependsOn(op); len(got) != 1 || got[0] != helmop.SharedControlName() {
		t.Errorf("host-sync dependsOn = %v, want shared control", got)
	}
	if got := downstreamSecretNames(op); len(got) != 0 {
		t.Errorf("host-sync downstreamSecretNames = %v, want none: control owns the client Secret", got)
	}
	hostValues := helmValues(t, op)
	if got := valueAtPath(t, hostValues, "checksums", "snapshotterBinary", "amd64"); got != "snapshotter-bin-amd64" {
		t.Errorf("host-sync snapshotter binary amd64 = %v", got)
	}
	if got := valueAtPath(t, hostValues, "checksums", "snapshotterBinary", "arm64"); got != "snapshotter-bin-arm64" {
		t.Errorf("host-sync snapshotter binary arm64 = %v", got)
	}
	ips, _ := helmValues(t, op)["schedulerIPs"].([]any)
	if len(ips) != 2 || ips[0] != "203.0.113.10:8002" || ips[1] != "203.0.113.11:8002" {
		t.Errorf("host-sync schedulerIPs = %v", ips)
	}

	controller.machines = &fakeMachineCache{machines: []*capiv1beta2.Machine{
		seedMachine("c1", "c1-0", "203.0.113.12"),
	}}
	controller.downstream = &fakeSchedulerPodLister{pods: []schedulerPod{{NodeName: "c1-node", HostIP: "203.0.113.12"}}}
	controller.clusters.(*fakeClusterStore).set(cluster)
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile(IP change) error = %v", err)
	}
	op = store.object("fleet-default/" + helmop.HostSyncName("c1"))
	ips, _ = helmValues(t, op)["schedulerIPs"].([]any)
	if len(ips) != 1 || ips[0] != "203.0.113.12:8002" {
		t.Errorf("host-sync schedulerIPs after change = %v", ips)
	}

	controller.machines = &fakeMachineCache{}
	controller.downstream = &fakeSchedulerPodLister{}
	controller.clusters.(*fakeClusterStore).set(cluster)
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile(empty IPs) error = %v", err)
	}
	if store.has("fleet-default/" + helmop.HostSyncName("c1")) {
		t.Error("host-sync HelmOp was not removed for empty scheduler IPs")
	}
}

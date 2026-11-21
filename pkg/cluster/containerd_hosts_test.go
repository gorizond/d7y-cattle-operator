package cluster

import (
	"errors"
	"strings"
	"testing"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	rkev1 "github.com/rancher/rancher/pkg/apis/rke.cattle.io/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"

	"github.com/gorizond/d7y-cattle-operator/pkg/apply"
	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
)

type fakeConfigMapStore struct {
	objects   map[string]*corev1.ConfigMap
	creates   int
	updates   int
	getErr    error
	createErr error
	updateErr error
}

func newFakeConfigMapStore() *fakeConfigMapStore {
	return &fakeConfigMapStore{objects: map[string]*corev1.ConfigMap{}}
}

func (f *fakeConfigMapStore) Get(namespace, name string, _ metav1.GetOptions) (*corev1.ConfigMap, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	obj, ok := f.objects[namespace+"/"+name]
	if !ok {
		return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, name)
	}
	return obj.DeepCopy(), nil
}

func (f *fakeConfigMapStore) Create(obj *corev1.ConfigMap) (*corev1.ConfigMap, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.creates++
	f.objects[obj.Namespace+"/"+obj.Name] = obj.DeepCopy()
	return obj.DeepCopy(), nil
}

func (f *fakeConfigMapStore) Update(obj *corev1.ConfigMap) (*corev1.ConfigMap, error) {
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	f.updates++
	f.objects[obj.Namespace+"/"+obj.Name] = obj.DeepCopy()
	return obj.DeepCopy(), nil
}

func (f *fakeConfigMapStore) get(namespace, name string) *corev1.ConfigMap {
	obj := f.objects[namespace+"/"+name]
	if obj == nil {
		return nil
	}
	return obj.DeepCopy()
}

func TestReconcileCreatesContainerdHostsForK3s(t *testing.T) {
	controller, store, _ := newTestController(t)
	cluster := testSeedCluster("42")
	cluster.Spec.KubernetesVersion = "v1.35.9+k3s1"
	cluster.Annotations[AnnotationSchedulerIPs] = "203.0.113.10"
	clusterStore := controller.clusters.(*fakeClusterStore)
	clusterStore.set(cluster)

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}

	cm := controller.configmaps.(*fakeConfigMapStore).get(cluster.Namespace, containerdHostsName(cluster.Name))
	if cm == nil {
		t.Fatal("containerd hosts ConfigMap was not created")
	}
	if got := cm.Annotations[containerdHostsAuthorized]; got != cluster.Name {
		t.Errorf("authorization annotation = %q, want %q", got, cluster.Name)
	}
	if got := cm.Labels[apply.ManagedByLabel]; got != "d7y-cattle-operator" {
		t.Errorf("managed-by label = %q, want d7y-cattle-operator", got)
	}
	if len(cm.OwnerReferences) != 1 || cm.OwnerReferences[0].Kind != "Cluster" || cm.OwnerReferences[0].Name != cluster.Name {
		t.Errorf("ownerReferences = %+v, want the provisioning Cluster", cm.OwnerReferences)
	}
	for _, key := range []string{"_default", "docker.io", "index.docker.io"} {
		if _, ok := cm.Data[key]; !ok {
			t.Errorf("ConfigMap data missing key %q", key)
		}
	}
	if got := cm.Data["_default"]; got != "server = \"http://127.0.0.1:4001\"\n\n[host.\"http://127.0.0.1:4001\"]\n  capabilities = [\"pull\", \"resolve\"]\n" {
		t.Errorf("_default hosts.toml = %q", got)
	}
	for _, key := range []string{"docker.io", "index.docker.io"} {
		for _, want := range []string{
			`server = "https://registry-1.docker.io"`,
			`[host."http://127.0.0.1:4001"]`,
			`X-Dragonfly-Registry = ["https://registry-1.docker.io"]`,
			`[host."https://registry-1.docker.io"]`,
		} {
			if !strings.Contains(cm.Data[key], want) {
				t.Errorf("%s hosts.toml missing %q: %q", key, want, cm.Data[key])
			}
		}
	}

	stored := clusterStore.get()
	if stored.Spec.RKEConfig == nil {
		t.Fatal("rkeConfig is nil")
	}
	files := stored.Spec.RKEConfig.MachineSelectorFiles
	if len(files) != 1 {
		t.Fatalf("machineSelectorFiles = %+v, want one entry", files)
	}
	entry := files[0]
	if entry.MachineLabelSelector == nil || len(entry.MachineLabelSelector.MatchLabels) != 0 {
		t.Errorf("machineLabelSelector = %+v, want {}", entry.MachineLabelSelector)
	}
	if len(entry.FileSources) != 1 || entry.FileSources[0].ConfigMap.Name != cm.Name {
		t.Fatalf("fileSources = %+v, want ConfigMap %q", entry.FileSources, cm.Name)
	}
	wantPaths := map[string]string{
		"_default":        "/var/lib/rancher/k3s/agent/etc/containerd/certs.d/_default/hosts.toml",
		"docker.io":       "/var/lib/rancher/k3s/agent/etc/containerd/certs.d/docker.io/hosts.toml",
		"index.docker.io": "/var/lib/rancher/k3s/agent/etc/containerd/certs.d/index.docker.io/hosts.toml",
	}
	for _, item := range entry.FileSources[0].ConfigMap.Items {
		if got := wantPaths[item.Key]; got != item.Path {
			t.Errorf("item %q path = %q, want %q", item.Key, item.Path, got)
		}
	}
	if got := entry.FileSources[0].ConfigMap.DefaultPermissions; got != containerdHostsPermissions {
		t.Errorf("defaultPermissions = %q, want %q", got, containerdHostsPermissions)
	}

	raw, err := yaml.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal machineSelectorFiles entry: %v", err)
	}
	t.Logf("rendered machineSelectorFiles entry:\n%s", raw)

	// A second reconcile must not duplicate the entry or rewrite the ConfigMap.
	if _, err := controller.reconcile("", stored); err != nil {
		t.Fatalf("second reconcile() error = %v", err)
	}
	if got := len(clusterStore.get().Spec.RKEConfig.MachineSelectorFiles); got != 1 {
		t.Errorf("machineSelectorFiles after second reconcile = %d, want 1", got)
	}
	cmStore := controller.configmaps.(*fakeConfigMapStore)
	if cmStore.creates != 1 || cmStore.updates != 0 {
		t.Errorf("ConfigMap writes = create:%d update:%d, want create:1 update:0", cmStore.creates, cmStore.updates)
	}
	if !store.has("fleet-default/" + helmop.SharedKeyDBName()) {
		t.Error("reconcile did not reach the shared HelmOps")
	}
}

func TestReconcileUsesRKEPathsAndPreservesUserEntries(t *testing.T) {
	controller, _, _ := newTestController(t)
	cluster := testSeedCluster("42")
	cluster.Spec.KubernetesVersion = "v1.35.9+rke2r1"
	cluster.Annotations[AnnotationSchedulerIPs] = "203.0.113.10"
	cluster.Spec.RKEConfig = &provv1.RKEConfig{}
	cluster.Spec.RKEConfig.MachineSelectorFiles = []rkev1.RKEProvisioningFiles{{
		MachineLabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"role": "user"}},
		FileSources: []rkev1.ProvisioningFileSource{{
			ConfigMap: rkev1.K8sObjectFileSource{Name: "user-files"},
		}},
	}}
	clusterStore := controller.clusters.(*fakeClusterStore)
	clusterStore.set(cluster)

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	files := clusterStore.get().Spec.RKEConfig.MachineSelectorFiles
	if len(files) != 2 {
		t.Fatalf("machineSelectorFiles = %+v, want the user entry plus one operator entry", files)
	}
	if got := files[0].FileSources[0].ConfigMap.Name; got != "user-files" {
		t.Errorf("user entry was changed: first ConfigMap = %q", got)
	}
	if got := files[0].MachineLabelSelector.MatchLabels["role"]; got != "user" {
		t.Errorf("user selector was changed: role = %q", got)
	}
	operatorEntry := files[1]
	if len(operatorEntry.FileSources) != 1 || operatorEntry.FileSources[0].ConfigMap.Name != containerdHostsName(cluster.Name) {
		t.Fatalf("operator entry = %+v", operatorEntry)
	}
	if got := operatorEntry.FileSources[0].ConfigMap.Items[0].Path; got != "/var/lib/rancher/rke2/agent/etc/containerd/certs.d/_default/hosts.toml" {
		t.Errorf("rke2 _default path = %q", got)
	}
}

func TestReconcileContainerdHostsFailureSetsCondition(t *testing.T) {
	controller, _, events := newTestController(t)
	store := controller.configmaps.(*fakeConfigMapStore)
	store.createErr = errors.New("configmap create denied")
	cluster := &provv1.Cluster{ObjectMeta: metav1.ObjectMeta{
		Name:      "c1",
		Namespace: "fleet-default",
		Labels:    map[string]string{LabelEnabled: "true"},
	}}
	clusterStore := controller.clusters.(*fakeClusterStore)
	clusterStore.set(cluster)

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v, want nil (ConfigMap failures are non-fatal)", err)
	}
	if got := clusterStore.condition(string(conditionContainerdHostsReady)); got == nil || got.Status != corev1.ConditionFalse || got.Reason != "ApplyFailed" {
		t.Errorf("ContainerdHostsReady = %+v, want False/ApplyFailed", got)
	}
	if len(events.events) == 0 || events.events[0].Reason != "ContainerdHostsFailed" {
		t.Errorf("events = %+v, want ContainerdHostsFailed", events.events)
	}
}

func TestContainerdCertsDirDefaultsToK3s(t *testing.T) {
	for _, tc := range []struct {
		version string
		want    string
	}{
		{"", "/var/lib/rancher/k3s/agent/etc/containerd/certs.d"},
		{"v1.35.9+k3s1", "/var/lib/rancher/k3s/agent/etc/containerd/certs.d"},
		{"v1.35.9+rke2r1", "/var/lib/rancher/rke2/agent/etc/containerd/certs.d"},
	} {
		if got := containerdCertsDir(tc.version); got != tc.want {
			t.Errorf("containerdCertsDir(%q) = %q, want %q", tc.version, got, tc.want)
		}
	}
}

func TestContainerdHostsConfigMapUpdatesOnPortChange(t *testing.T) {
	controller, _, _ := newTestController(t)
	cluster := &provv1.Cluster{ObjectMeta: metav1.ObjectMeta{
		Name:      "c1",
		Namespace: "fleet-default",
		Labels:    map[string]string{LabelEnabled: "true"},
	}}
	clusterStore := controller.clusters.(*fakeClusterStore)
	clusterStore.set(cluster)

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("first reconcile() error = %v", err)
	}
	controller.opts.ContainerdMirrorPort = 4101
	if _, err := controller.reconcile("", clusterStore.get()); err != nil {
		t.Fatalf("second reconcile() error = %v", err)
	}
	cmStore := controller.configmaps.(*fakeConfigMapStore)
	cm := cmStore.get(cluster.Namespace, containerdHostsName(cluster.Name))
	if cm == nil {
		t.Fatal("containerd hosts ConfigMap missing")
	}
	if got := cm.Data["_default"]; !strings.Contains(got, "127.0.0.1:4101") {
		t.Errorf("_default hosts.toml = %q, want the updated port", got)
	}
	if cmStore.updates != 1 {
		t.Errorf("ConfigMap updates = %d, want 1", cmStore.updates)
	}
	gotFiles := 0
	if cfg := clusterStore.get().Spec.RKEConfig; cfg != nil {
		gotFiles = len(cfg.MachineSelectorFiles)
	}
	if gotFiles != 0 {
		t.Errorf("machineSelectorFiles = %d, want 0 without scheduler IPs", gotFiles)
	}
}

func TestContainerdHostsClusterPatchFailureSetsCondition(t *testing.T) {
	controller, _, events := newTestController(t)
	clusterStore := controller.clusters.(*fakeClusterStore)
	clusterStore.updateErr = errors.New("cluster update denied")
	cluster := &provv1.Cluster{ObjectMeta: metav1.ObjectMeta{
		Name:        "c1",
		Namespace:   "fleet-default",
		Labels:      map[string]string{LabelEnabled: "true"},
		Annotations: map[string]string{AnnotationSchedulerIPs: "203.0.113.10"},
	}}
	clusterStore.set(cluster)

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v, want nil (Cluster patch failures are non-fatal)", err)
	}
	if got := clusterStore.condition(string(conditionContainerdHostsReady)); got == nil || got.Status != corev1.ConditionFalse || got.Reason != "ApplyFailed" {
		t.Errorf("ContainerdHostsReady = %+v, want False/ApplyFailed", got)
	}
	found := false
	for _, event := range events.events {
		if event.Reason == "ContainerdHostsFailed" {
			found = true
		}
	}
	if !found {
		t.Errorf("events = %+v, want ContainerdHostsFailed", events.events)
	}
}

func TestContainerdHostsEntryIsGatedBySchedulerIPs(t *testing.T) {
	controller, _, _ := newTestController(t)
	cluster := &provv1.Cluster{ObjectMeta: metav1.ObjectMeta{
		Name:      "c1",
		Namespace: "fleet-default",
		Labels:    map[string]string{LabelEnabled: "true"},
	}, Spec: provv1.ClusterSpec{KubernetesVersion: "v1.35.9+k3s1"}}
	clusterStore := controller.clusters.(*fakeClusterStore)
	clusterStore.set(cluster)

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("empty annotation reconcile() error = %v", err)
	}
	gotFiles := 0
	if cfg := clusterStore.get().Spec.RKEConfig; cfg != nil {
		gotFiles = len(cfg.MachineSelectorFiles)
	}
	if gotFiles != 0 {
		t.Fatalf("empty scheduler IPs machineSelectorFiles = %d, want 0", gotFiles)
	}

	stored := clusterStore.get()
	if stored.Annotations == nil {
		stored.Annotations = map[string]string{}
	}
	stored.Annotations[AnnotationSchedulerIPs] = "203.0.113.10"
	if _, err := controller.reconcile("", stored); err != nil {
		t.Fatalf("non-empty annotation reconcile() error = %v", err)
	}
	entry := operatorSelectorEntry(t, clusterStore.get(), containerdHostsName(cluster.Name))
	for _, key := range []string{"_default", "docker.io", "index.docker.io"} {
		if !containsItem(entry, key) {
			t.Errorf("non-empty scheduler IPs missing %q", key)
		}
	}

	stored = clusterStore.get()
	delete(stored.Annotations, AnnotationSchedulerIPs)
	if _, err := controller.reconcile("", stored); err != nil {
		t.Fatalf("removed annotation reconcile() error = %v", err)
	}
	if got := len(clusterStore.get().Spec.RKEConfig.MachineSelectorFiles); got != 0 {
		t.Fatalf("removed scheduler IPs machineSelectorFiles = %d, want 0", got)
	}
}

func operatorSelectorEntry(t *testing.T, cluster *provv1.Cluster, name string) rkev1.RKEProvisioningFiles {
	t.Helper()
	if cluster.Spec.RKEConfig == nil {
		t.Fatal("rkeConfig is nil")
	}
	for _, entry := range cluster.Spec.RKEConfig.MachineSelectorFiles {
		for _, source := range entry.FileSources {
			if source.ConfigMap.Name == name {
				return entry
			}
		}
	}
	t.Fatalf("machineSelectorFiles has no entry for %q", name)
	return rkev1.RKEProvisioningFiles{}
}

func containsItem(entry rkev1.RKEProvisioningFiles, key string) bool {
	for _, source := range entry.FileSources {
		for _, item := range source.ConfigMap.Items {
			if item.Key == key {
				return true
			}
		}
	}
	return false
}

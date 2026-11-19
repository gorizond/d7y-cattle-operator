package cluster

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
)

func TestEnsureGeneratedSecretHonorsKeyLength(t *testing.T) {
	controller, _, _ := newTestController(t)
	cluster := &provv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "fleet-default"}}

	if err := controller.ensureGeneratedSecret(cluster, "c1-harbor-secret", "secretKey", 16); err != nil {
		t.Fatalf("ensureGeneratedSecret() error = %v", err)
	}
	secret, err := controller.secrets.(*fakeSecretStore).Get("fleet-default", "c1-harbor-secret", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("generated Secret was not created: %v", err)
	}
	if got := len(secret.Data["secretKey"]); got != 32 {
		t.Fatalf("secretKey length = %d, want 32 hex chars (16 raw bytes)", got)
	}
}

func TestReconcileHarborAndProxy(t *testing.T) {
	controller, store, events := newTestController(t)
	clusterStore := controller.clusters.(*fakeClusterStore)
	cluster := testHarborCluster()
	clusterStore.set(cluster)

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	if len(events.events) != 0 {
		t.Errorf("events = %+v, want none", events.events)
	}
	for _, name := range []string{helmop.SharedHarborName(), helmop.SharedRegistryProxyName()} {
		if !store.has("fleet-default/" + name) {
			t.Errorf("%s HelmOp was not created", name)
		}
	}
	if got := dependsOn(store.object("fleet-default/" + helmop.SharedRegistryProxyName())); len(got) != 0 {
		t.Errorf("proxy dependsOn = %v, want none (independent toggles)", got)
	}
	admin, err := controller.secrets.(*fakeSecretStore).Get("fleet-default", helmop.SharedHarborAdminSecretName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Harbor admin Secret was not copied: %v", err)
	}
	if string(admin.Data["HARBOR_ADMIN_PASSWORD"]) != "admin-password" {
		t.Error("copied Harbor admin Secret data is wrong")
	}
	if got := clusterStore.condition(string(conditionHarborReady)); got == nil || got.Status != corev1.ConditionTrue {
		t.Errorf("HarborReady = %+v", got)
	}
	if got := clusterStore.condition(string(conditionRegistryProxyReady)); got == nil || got.Status != corev1.ConditionFalse || got.Reason != "Progressing" {
		t.Errorf("RegistryProxyReady before HelmOp status = %+v, want False/Progressing", got)
	}

	proxyOp := store.object("fleet-default/" + helmop.SharedRegistryProxyName())
	if proxyOp == nil {
		t.Fatal("registry proxy HelmOp is missing")
	}
	if err := unstructured.SetNestedSlice(proxyOp.Object, []any{
		map[string]any{"type": "Ready", "status": "True", "message": "Ready"},
	}, "status", "conditions"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile(ready HelmOp) error = %v", err)
	}
	if got := clusterStore.condition(string(conditionRegistryProxyReady)); got == nil || got.Status != corev1.ConditionTrue || got.Reason != "Ready" {
		t.Errorf("RegistryProxyReady after HelmOp Ready = %+v, want True/Ready", got)
	}
}

func TestReconcileCreatesRegistryProxyRobotSecret(t *testing.T) {
	controller, store, events := newTestController(t)
	controller.opts.ProxyRegistries = []string{"docker.io", "ghcr.io", "*"}
	cluster := testHarborCluster()
	controller.clusters.(*fakeClusterStore).set(cluster)

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	if len(events.events) != 0 {
		t.Errorf("events = %+v, want none", events.events)
	}

	ensurer := controller.robotEnsurer.(*fakeRobotEnsurer)
	if len(ensurer.calls) != 1 {
		t.Fatalf("robot ensure calls = %d, want 1", len(ensurer.calls))
	}
	call := ensurer.calls[0]
	if call.HarborURL != "https://harbor.example.com" {
		t.Errorf("harbor URL = %q", call.HarborURL)
	}
	if call.Username != "admin" || call.Password != "admin-password" {
		t.Error("robot ensurer did not receive the Harbor admin credentials")
	}
	if call.SecretName != helmop.SharedRegistryProxyRobotSecretName() || call.MirrorProject != "mirror" {
		t.Errorf("robot Secret/mirror = %q/%q", call.SecretName, call.MirrorProject)
	}
	if want := []string{"docker.io", "ghcr.io", "*"}; !reflect.DeepEqual(call.Registries, want) {
		t.Errorf("registries = %v, want %v", call.Registries, want)
	}

	robot, err := controller.secrets.(*fakeSecretStore).Get("fleet-default", helmop.SharedRegistryProxyRobotSecretName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("robot Secret was not created: %v", err)
	}
	for _, key := range []string{"username", "password", "permissions-fingerprint"} {
		if len(robot.Data[key]) == 0 {
			t.Errorf("robot Secret is missing key %q", key)
		}
	}

	proxy := store.object("fleet-default/" + helmop.SharedRegistryProxyName())
	if proxy == nil {
		t.Fatal("registry proxy HelmOp is missing")
	}
	spec, _ := proxy.Object["spec"].(map[string]any)
	raw, _ := spec["downstreamResources"].([]any)
	names := make([]string, 0, len(raw))
	for _, item := range raw {
		resource, _ := item.(map[string]any)
		names = append(names, resource["name"].(string))
	}
	want := []string{helmop.SharedHarborAdminSecretName(), helmop.SharedRegistryProxyWebhookSecretName(), helmop.SharedRegistryProxyRobotSecretName()}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("downstreamResources = %v, want %v", names, want)
	}

	firstRevision := proxyRobotRevision(t, store)
	if firstRevision == "" {
		t.Fatal("proxy robot Secret revision annotation is empty")
	}
	// A rotated robot password must change the pod-template annotation so the
	// proxy re-reads the mounted Secret.
	ensurer.password = "rotated-robot-password"
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile(rotation) error = %v", err)
	}
	if got := proxyRobotRevision(t, store); got == firstRevision {
		t.Error("proxy robot Secret revision did not change after rotation")
	}
}

func proxyRobotRevision(t *testing.T, store *fakeHelmOpStore) string {
	t.Helper()
	op := store.object("fleet-default/" + helmop.SharedRegistryProxyName())
	if op == nil {
		t.Fatal("registry proxy HelmOp is missing")
	}
	annotations, ok := valueAtPath(t, helmValues(t, op), "podAnnotations").(map[string]any)
	if !ok {
		t.Fatal("proxy podAnnotations are missing")
	}
	revision, _ := annotations["p2p.dragonfly.io/robot-secret-revision"].(string)
	return revision
}

func TestRegistryProxySourceProjects(t *testing.T) {
	want := []string{"dockerio", "ghcrio", "*"}
	if got := registryProxySourceProjects([]string{"docker.io", " ghcr.io ", "", "*"}); !reflect.DeepEqual(got, want) {
		t.Errorf("source projects = %v, want %v", got, want)
	}
}

func TestReconcileRegistryProxyRobotFailureSkipsHelmOp(t *testing.T) {
	controller, store, events := newTestController(t)
	controller.robotEnsurer.(*fakeRobotEnsurer).err = errors.New("harbor unavailable")
	cluster := testHarborCluster()
	controller.clusters.(*fakeClusterStore).set(cluster)

	if _, err := controller.reconcile("", cluster); err == nil {
		t.Fatal("reconcile() error = nil, want robot provisioning failure")
	}
	if store.has("fleet-default/" + helmop.SharedRegistryProxyName()) {
		t.Error("registry proxy HelmOp was applied without the robot Secret")
	}
	found := false
	for _, event := range events.events {
		if event.Reason == "RegistryProxyRobotFailed" {
			found = true
		}
	}
	if !found {
		t.Errorf("events = %+v, want RegistryProxyRobotFailed", events.events)
	}
}

func TestReconcileExternalHarborSkipsHarborHelmOp(t *testing.T) {
	controller, store, _ := newTestController(t)
	cluster := testHarborCluster()
	cluster.Annotations[AnnotationHarborURL] = "https://external.example.com"

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	if store.has("fleet-default/" + helmop.SharedHarborName()) {
		t.Error("Harbor HelmOp was created for an external Harbor")
	}
	proxy := store.object("fleet-default/" + helmop.SharedRegistryProxyName())
	if proxy == nil {
		t.Fatal("proxy HelmOp was not created for an external Harbor")
	}
	if got := dependsOn(proxy); len(got) != 0 {
		t.Errorf("proxy dependsOn = %v, want none", got)
	}
	if got := valueAtPath(t, helmValues(t, proxy), "harbor", "url"); got != "https://external.example.com" {
		t.Errorf("proxy harbor.url = %v", got)
	}
}

func TestReconcileProxyIndependentOfHarbor(t *testing.T) {
	controller, store, _ := newTestController(t)
	controller.opts.ProxyHarborURL = "https://external.example.com"
	cluster := testHarborCluster()
	delete(cluster.Annotations, AnnotationHarbor)
	delete(cluster.Annotations, AnnotationHarborDomain)

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	if store.has("fleet-default/" + helmop.SharedHarborName()) {
		t.Error("Harbor HelmOp was created without the Harbor annotation")
	}
	if !store.has("fleet-default/" + helmop.SharedRegistryProxyName()) {
		t.Error("independent proxy HelmOp was not created")
	}
}

func TestReconcileHarborDomainMissing(t *testing.T) {
	controller, store, events := newTestController(t)
	clusterStore := controller.clusters.(*fakeClusterStore)
	cluster := testHarborCluster()
	delete(cluster.Annotations, AnnotationHarborDomain)
	clusterStore.set(cluster)

	if _, err := controller.reconcile("", cluster); err == nil {
		t.Fatal("reconcile() error = nil, want HarborDomainMissing")
	}
	if store.has("fleet-default/"+helmop.SharedHarborName()) || store.has("fleet-default/"+helmop.SharedRegistryProxyName()) {
		t.Error("Harbor/proxy HelmOps were created without a valid domain")
	}
	if got := clusterStore.condition(string(conditionHarborDomainMissing)); got == nil || got.Status != corev1.ConditionTrue {
		t.Errorf("HarborDomainMissing = %+v", got)
	}
	if len(events.events) == 0 {
		t.Error("expected a HarborDomainMissing event")
	}
}

func TestReconcileHarborAdminSecretMissing(t *testing.T) {
	controller, store, events := newTestController(t)
	secrets := controller.secrets.(*fakeSecretStore)
	delete(secrets.secrets, "dragonfly-system/harbor-admin")
	cluster := testHarborCluster()

	if _, err := controller.reconcile("", cluster); err == nil {
		t.Fatal("reconcile() error = nil, want missing admin Secret")
	}
	if store.has("fleet-default/"+helmop.SharedHarborName()) || store.has("fleet-default/"+helmop.SharedRegistryProxyName()) {
		t.Error("Harbor/proxy HelmOps were created without the admin Secret")
	}
	found := false
	for _, event := range events.events {
		if event.Reason == "HarborAdminSecretMissing" {
			found = true
		}
	}
	if !found {
		t.Errorf("events = %+v, want HarborAdminSecretMissing", events.events)
	}
}

func TestReconcileRemovesHarborAndProxy(t *testing.T) {
	controller, store, _ := newTestController(t)
	cluster := testHarborCluster()
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	delete(cluster.Annotations, AnnotationHarbor)
	delete(cluster.Annotations, AnnotationHarborDomain)
	delete(cluster.Annotations, AnnotationProxy)
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile(remove) error = %v", err)
	}
	if store.has("fleet-default/" + helmop.HarborName(cluster.Name)) {
		t.Error("legacy per-cluster Harbor HelmOp was not removed")
	}
	// The shared Harbor/proxy objects are global; removing the roles only
	// stops Fleet from targeting this Cluster.
	if !store.has("fleet-default/" + helmop.SharedHarborName()) {
		t.Error("shared Harbor HelmOp was removed with the role")
	}
	// The shared proxy is global; removing the role only stops Fleet from
	// targeting this Cluster, so the object remains.
	if !store.has("fleet-default/" + helmop.SharedRegistryProxyName()) {
		t.Error("shared registry proxy HelmOp was removed with the role")
	}
	if store.has("fleet-default/" + helmop.RegistryProxyName(cluster.Name)) {
		t.Error("legacy per-cluster registry proxy HelmOp was not removed")
	}
}

func testHarborCluster() *provv1.Cluster {
	cluster := testSeedCluster("42")
	delete(cluster.Annotations, AnnotationSeed)
	delete(cluster.Annotations, AnnotationSchedulerClusterID)
	cluster.Annotations[AnnotationHarbor] = "true"
	cluster.Annotations[AnnotationHarborDomain] = "harbor.example.com"
	cluster.Annotations[AnnotationProxy] = "true"
	return cluster
}

// The operator must forward the public ingress path/TLS Secret and the
// same-cluster webhook address into the registry-proxy Helm values.
func TestReconcileRegistryProxyIngressAndWebhook(t *testing.T) {
	controller, store, _ := newTestController(t)
	controller.opts.ProxyHostAliases = []helmop.HostAlias{{
		IP:        "10.43.0.10",
		Hostnames: []string{"harbor.example.test"},
	}}
	cluster := testHarborCluster()
	controller.clusters.(*fakeClusterStore).set(cluster)

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	proxy := store.object("fleet-default/" + helmop.SharedRegistryProxyName())
	if proxy == nil {
		t.Fatal("registry proxy HelmOp is missing")
	}
	values := helmValues(t, proxy)
	for _, tc := range []struct {
		path []string
		want any
	}{
		{[]string{"ingress", "path"}, "/proxy"},
		{[]string{"ingress", "tlsSecret"}, helmop.SharedProxyTLSSecretTemplate},
		{[]string{"proxy", "basePath"}, "/proxy"},
		{[]string{"webhook", "address"}, helmop.SharedProxyWebhookAddressTemplate},
	} {
		if got := valueAtPath(t, values, tc.path...); got != tc.want {
			t.Errorf("%v = %v, want %v", tc.path, got, tc.want)
		}
	}
	if got := controller.clusters.(*fakeClusterStore).annotation(helmop.AnnotationProxyTLSSecret); got != "poligon-harbor-tls" {
		t.Errorf("proxy-tls-secret annotation = %q, want the operator default", got)
	}
	aliases, ok := valueAtPath(t, values, "proxy", "hostAliases").(string)
	if !ok || !strings.Contains(aliases, `{"ip":"10.43.0.10","hostnames":["harbor.example.test"]}`) {
		t.Errorf("proxy.hostAliases = %v, want the static fallback in the Fleet template", aliases)
	}
}

func TestProxyPublicURL(t *testing.T) {
	cases := []struct {
		domain string
		base   string
		want   string
	}{
		{"", "", ""},
		{"proxy.example.com", "", "https://proxy.example.com"},
		{"proxy.example.com", "/proxy", "https://proxy.example.com/proxy"},
		{"proxy.example.com", "proxy", "https://proxy.example.com/proxy"},
		{"proxy.example.com", "/proxy/", "https://proxy.example.com/proxy"},
	}
	for _, tc := range cases {
		if got := proxyPublicURL(tc.domain, tc.base); got != tc.want {
			t.Errorf("proxyPublicURL(%q, %q) = %q, want %q", tc.domain, tc.base, got, tc.want)
		}
	}
}

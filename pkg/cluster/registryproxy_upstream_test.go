package cluster

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
)

func putUpstreamCredentials(t *testing.T, controller *Controller, name string, data map[string][]byte) {
	t.Helper()
	if _, err := controller.secrets.(*fakeSecretStore).Create(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "dragonfly-system"},
		Type:       corev1.SecretTypeOpaque,
		Data:       data,
	}); err != nil {
		t.Fatalf("create upstream credentials Secret: %v", err)
	}
}

func TestReconcileSharedRegistryProxyUpstreamCredentials(t *testing.T) {
	controller, store, _ := newTestController(t)
	controller.opts.ProxyHarborURL = "https://harbor.example.com"
	controller.opts.ProxyUpstreamCredentialsSecret = "upstream-creds"
	credentials := []byte("registry.gitlab.com:\n  access_key: user\n  access_secret: token\n")
	putUpstreamCredentials(t, controller, "upstream-creds", map[string][]byte{"credentials.yaml": credentials})
	cluster := testProxyCluster()

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	op := store.object("fleet-default/" + helmop.SharedRegistryProxyName())
	if op == nil {
		t.Fatal("shared registry proxy HelmOp was not created")
	}
	values := helmValues(t, op)
	for path, want := range map[string]any{
		"upstream.credentialsSecret": "upstream-creds",
		"upstream.credentialsFile":   "/etc/registry-proxy/secrets/upstream/credentials.yaml",
	} {
		keys := strings.Split(path, ".")
		if got := valueAtPath(t, values, keys...); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
	wantDownstream := []string{
		helmop.SharedHarborAdminSecretName(),
		helmop.SharedRegistryProxyWebhookSecretName(),
		helmop.SharedRegistryProxyRobotSecretName(),
		"upstream-creds",
	}
	if got := registryProxyDownstreamNames(t, op); !equalStrings(got, wantDownstream) {
		t.Errorf("downstreamResources = %v, want %v", got, wantDownstream)
	}
	replicated, err := controller.secrets.(*fakeSecretStore).Get("fleet-default", "upstream-creds", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get replicated upstream credentials Secret: %v", err)
	}
	if string(replicated.Data["credentials.yaml"]) != string(credentials) {
		t.Errorf("replicated credentials.yaml = %q", replicated.Data["credentials.yaml"])
	}
}

func TestReconcileSharedRegistryProxyUpstreamCredentialsMissing(t *testing.T) {
	controller, store, _ := newTestController(t)
	controller.opts.ProxyHarborURL = "https://harbor.example.com"
	controller.opts.ProxyUpstreamCredentialsSecret = "missing-creds"
	cluster := testProxyCluster()

	_, err := controller.reconcile("", cluster)
	if err == nil || !strings.Contains(err.Error(), "missing-creds") {
		t.Fatalf("reconcile() error = %v, want missing credentials Secret", err)
	}
	if store.has("fleet-default/" + helmop.SharedRegistryProxyName()) {
		t.Error("registry proxy HelmOp was applied without the upstream credentials Secret")
	}
}

func TestReconcileSharedRegistryProxyUpstreamCredentialsMissingKey(t *testing.T) {
	controller, store, _ := newTestController(t)
	controller.opts.ProxyHarborURL = "https://harbor.example.com"
	controller.opts.ProxyUpstreamCredentialsSecret = "upstream-creds"
	putUpstreamCredentials(t, controller, "upstream-creds", map[string][]byte{"other": []byte("x")})
	cluster := testProxyCluster()

	_, err := controller.reconcile("", cluster)
	if err == nil || !strings.Contains(err.Error(), "credentials.yaml") {
		t.Fatalf("reconcile() error = %v, want missing credentials.yaml error", err)
	}
	if store.has("fleet-default/" + helmop.SharedRegistryProxyName()) {
		t.Error("registry proxy HelmOp was applied without credentials.yaml")
	}
}

func TestReconcileSharedRegistryProxyUpstreamCredentialsDisabled(t *testing.T) {
	controller, store, _ := newTestController(t)
	controller.opts.ProxyHarborURL = "https://harbor.example.com"
	cluster := testProxyCluster()

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	op := store.object("fleet-default/" + helmop.SharedRegistryProxyName())
	if op == nil {
		t.Fatal("shared registry proxy HelmOp was not created")
	}
	if _, ok := helmValues(t, op)["upstream"]; ok {
		t.Error("upstream values rendered while credentials are disabled")
	}
	if got := registryProxyDownstreamNames(t, op); len(got) != 3 {
		t.Errorf("downstreamResources = %v, want no upstream Secret", got)
	}
	if _, err := controller.secrets.(*fakeSecretStore).Get("fleet-default", "upstream-creds", metav1.GetOptions{}); err == nil {
		t.Error("upstream credentials Secret was replicated while disabled")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

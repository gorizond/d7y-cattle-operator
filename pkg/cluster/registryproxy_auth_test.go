package cluster

import (
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
)

func TestReconcileCreatesSharedRegistryProxyBasicAuth(t *testing.T) {
	controller, store, _ := newTestController(t)
	controller.opts.ProxyHarborURL = "https://harbor.example.com"
	controller.opts.ProxyBasicAuthEnabled = true
	controller.opts.ProxyBasicAuthUsername = "d7y-proxy"
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
		"auth.mode":           "basic",
		"auth.username":       "d7y-proxy",
		"auth.passwordSecret": helmop.SharedRegistryProxyBasicAuthSecretName(),
	} {
		if got := valueAtPath(t, values, path[:len("auth")], path[len("auth."):]); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
	wantDownstream := []string{
		helmop.SharedHarborAdminSecretName(),
		helmop.SharedRegistryProxyWebhookSecretName(),
		helmop.SharedRegistryProxyRobotSecretName(),
		helmop.SharedRegistryProxyBasicAuthSecretName(),
	}
	if got := registryProxyDownstreamNames(t, op); !reflect.DeepEqual(got, wantDownstream) {
		t.Errorf("downstreamResources = %v, want %v", got, wantDownstream)
	}

	secret, err := controller.secrets.(*fakeSecretStore).Get("dragonfly-system", helmop.SharedRegistryProxyBasicAuthSecretName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get basic auth Secret: %v", err)
	}
	if got := string(secret.Data["username"]); got != "d7y-proxy" {
		t.Errorf("username = %q, want d7y-proxy", got)
	}
	if len(secret.Data["password"]) < 32 {
		t.Errorf("password length = %d, want at least 32", len(secret.Data["password"]))
	}
	firstPassword := string(secret.Data["password"])

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("second reconcile() error = %v", err)
	}
	again, err := controller.secrets.(*fakeSecretStore).Get("dragonfly-system", helmop.SharedRegistryProxyBasicAuthSecretName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get basic auth Secret after second reconcile: %v", err)
	}
	if string(again.Data["password"]) != firstPassword {
		t.Error("basic auth password changed on a second reconcile")
	}
	replicated, err := controller.secrets.(*fakeSecretStore).Get("fleet-default", helmop.SharedRegistryProxyBasicAuthSecretName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get replicated basic auth Secret: %v", err)
	}
	if !reflect.DeepEqual(replicated.Data, again.Data) {
		t.Error("replicated basic auth Secret does not match the operator copy")
	}
}

func TestReconcileSharedRegistryProxyBasicAuthDisabled(t *testing.T) {
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
	auth, ok := valueAtPath(t, helmValues(t, op), "auth").(map[string]any)
	if !ok {
		t.Fatal("auth values are missing")
	}
	if auth["mode"] != "network-only" {
		t.Errorf("auth.mode = %v, want network-only", auth["mode"])
	}
	if _, ok := auth["username"]; ok {
		t.Error("auth.username rendered while basic auth is disabled")
	}
	if _, ok := auth["passwordSecret"]; ok {
		t.Error("auth.passwordSecret rendered while basic auth is disabled")
	}
	if got := registryProxyDownstreamNames(t, op); len(got) != 3 {
		t.Errorf("downstreamResources = %v, want no basic auth Secret", got)
	}
	for _, namespace := range []string{"dragonfly-system", "fleet-default"} {
		if _, err := controller.secrets.(*fakeSecretStore).Get(namespace, helmop.SharedRegistryProxyBasicAuthSecretName(), metav1.GetOptions{}); err == nil {
			t.Errorf("basic auth Secret exists in %s while disabled", namespace)
		}
	}
}

func registryProxyDownstreamNames(t *testing.T, op *unstructured.Unstructured) []string {
	t.Helper()
	spec, _ := op.Object["spec"].(map[string]any)
	raw, _ := spec["downstreamResources"].([]any)
	names := make([]string, 0, len(raw))
	for _, item := range raw {
		resource, _ := item.(map[string]any)
		name, _ := resource["name"].(string)
		names = append(names, name)
	}
	return names
}

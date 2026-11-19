package cluster

import (
	"encoding/base64"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const testManagerCredentialsSecret = "dragonfly-manager-api"

func TestEnsureManagerCredentialsSecretCreatesDefaults(t *testing.T) {
	controller, _, _ := newTestController(t)
	controller.opts.ManagerAPICredentialsSecret = testManagerCredentialsSecret

	if _, err := controller.ensureManagerCredentialsSecret(); err != nil {
		t.Fatalf("ensureManagerCredentialsSecret() error = %v", err)
	}
	secret, err := controller.secrets.Get("dragonfly-system", testManagerCredentialsSecret, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get generated Secret: %v", err)
	}
	if secret.Type != corev1.SecretTypeOpaque {
		t.Errorf("Secret type = %q, want Opaque", secret.Type)
	}
	if got := string(secret.Data[managerCredentialsUsernameKey]); got != managerRootUsername {
		t.Errorf("username = %q, want %q", got, managerRootUsername)
	}
	password := string(secret.Data[managerCredentialsPasswordKey])
	if len(password) < managerPasswordMinLen || len(password) > managerPasswordMaxLen {
		t.Errorf("password length = %d, want %d-%d", len(password), managerPasswordMinLen, managerPasswordMaxLen)
	}
	jwtKey := string(secret.Data[managerCredentialsJWTKeyKey])
	raw, err := base64.RawURLEncoding.DecodeString(jwtKey)
	if err != nil {
		t.Fatalf("jwt-key is not base64.RawURLEncoding: %v", err)
	}
	if len(raw) != managerJWTKeyByteCount {
		t.Errorf("jwt-key decoded length = %d, want %d", len(raw), managerJWTKeyByteCount)
	}
}

func TestEnsureManagerCredentialsSecretPreservesValidValues(t *testing.T) {
	controller, _, _ := newTestController(t)
	controller.opts.ManagerAPICredentialsSecret = testManagerCredentialsSecret
	const password = "custom-pass-1234"
	controller.secrets.(*fakeSecretStore).put(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: testManagerCredentialsSecret, Namespace: "dragonfly-system"},
		Data: map[string][]byte{
			managerCredentialsUsernameKey: []byte(managerRootUsername),
			managerCredentialsPasswordKey: []byte(password),
		},
	})

	if _, err := controller.ensureManagerCredentialsSecret(); err != nil {
		t.Fatalf("ensureManagerCredentialsSecret() error = %v", err)
	}
	secret, err := controller.secrets.Get("dragonfly-system", testManagerCredentialsSecret, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Secret: %v", err)
	}
	if got := string(secret.Data[managerCredentialsPasswordKey]); got != password {
		t.Errorf("password was overwritten, want the existing valid value preserved")
	}
	if got := string(secret.Data[managerCredentialsUsernameKey]); got != managerRootUsername {
		t.Errorf("username = %q, want preserved %q", got, managerRootUsername)
	}
	if len(secret.Data[managerCredentialsJWTKeyKey]) == 0 {
		t.Error("jwt-key was not generated for an existing Secret")
	}
}

func TestEnsureManagerCredentialsSecretRepairsInvalidValues(t *testing.T) {
	controller, _, _ := newTestController(t)
	controller.opts.ManagerAPICredentialsSecret = testManagerCredentialsSecret
	controller.secrets.(*fakeSecretStore).put(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: testManagerCredentialsSecret, Namespace: "dragonfly-system"},
		Data: map[string][]byte{
			managerCredentialsUsernameKey: []byte("  "),
			managerCredentialsPasswordKey: []byte("short"),
		},
	})

	if _, err := controller.ensureManagerCredentialsSecret(); err != nil {
		t.Fatalf("ensureManagerCredentialsSecret() error = %v", err)
	}
	secret, err := controller.secrets.Get("dragonfly-system", testManagerCredentialsSecret, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Secret: %v", err)
	}
	if got := string(secret.Data[managerCredentialsUsernameKey]); got != managerRootUsername {
		t.Errorf("username = %q, want %q for an empty username", got, managerRootUsername)
	}
	password := string(secret.Data[managerCredentialsPasswordKey])
	if password == "short" {
		t.Error("invalid short password was not regenerated")
	}
	if len(password) < managerPasswordMinLen || len(password) > managerPasswordMaxLen {
		t.Errorf("password length = %d, want %d-%d", len(password), managerPasswordMinLen, managerPasswordMaxLen)
	}
	if len(secret.Data[managerCredentialsJWTKeyKey]) == 0 {
		t.Error("jwt-key was not generated")
	}
}

func TestEnsureManagerCredentialsReplicatesToClusterNamespace(t *testing.T) {
	controller, _, _ := newTestController(t)
	controller.opts.ManagerAPICredentialsSecret = testManagerCredentialsSecret
	cluster := testSeedCluster("")

	if err := controller.ensureManagerCredentials(cluster); err != nil {
		t.Fatalf("ensureManagerCredentials() error = %v", err)
	}
	source, err := controller.secrets.Get("dragonfly-system", testManagerCredentialsSecret, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get source Secret: %v", err)
	}
	copied, err := controller.secrets.Get(cluster.Namespace, testManagerCredentialsSecret, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get replicated Secret: %v", err)
	}
	if copied.Type != source.Type {
		t.Errorf("replicated type = %q, want %q", copied.Type, source.Type)
	}
	for _, key := range []string{managerCredentialsUsernameKey, managerCredentialsPasswordKey, managerCredentialsJWTKeyKey} {
		if string(copied.Data[key]) != string(source.Data[key]) {
			t.Errorf("replicated key %q does not match the source", key)
		}
	}
}

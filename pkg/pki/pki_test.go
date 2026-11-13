package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"slices"
	"testing"
	"time"

	wranglercorev1 "github.com/rancher/wrangler/v3/pkg/generated/controllers/core/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Wrangler's generated SecretController must satisfy SecretStore.
var _ SecretStore = (wranglercorev1.SecretController)(nil)

const (
	testGRPCDomain = "manager-grpc.example.com"
	testAPIURL     = "https://manager-api.example.com"
)

func testBundle(t *testing.T) *Bundle {
	t.Helper()
	bundle, err := Generate(Options{ManagerGRPCDomain: testGRPCDomain, ManagerAPIURL: testAPIURL})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	return bundle
}

func TestGenerate(t *testing.T) {
	bundle := testBundle(t)

	caCerts, err := ParseCertificates(bundle.CACertPEM)
	if err != nil {
		t.Fatalf("ParseCertificates(CA) error = %v", err)
	}
	ca := caCerts[0]
	if !ca.IsCA {
		t.Error("CA certificate IsCA = false")
	}
	if ca.PublicKeyAlgorithm != x509.ECDSA {
		t.Errorf("CA public key algorithm = %v, want ECDSA", ca.PublicKeyAlgorithm)
	}
	if err := ca.CheckSignatureFrom(ca); err != nil {
		t.Errorf("CA is not self-signed: %v", err)
	}

	leafCerts, err := ParseCertificates(bundle.CertPEM)
	if err != nil {
		t.Fatalf("ParseCertificates(leaf) error = %v", err)
	}
	leaf := leafCerts[0]
	if err := leaf.CheckSignatureFrom(ca); err != nil {
		t.Errorf("leaf is not signed by the CA: %v", err)
	}
	if leaf.PublicKeyAlgorithm != x509.ECDSA {
		t.Errorf("leaf public key algorithm = %v, want ECDSA", leaf.PublicKeyAlgorithm)
	}
	if leaf.IsCA {
		t.Error("leaf certificate IsCA = true")
	}

	wantNotAfter := time.Now().Add(validity)
	if delta := leaf.NotAfter.Sub(wantNotAfter); delta < -time.Minute || delta > time.Minute {
		t.Errorf("leaf NotAfter = %v, want about %v", leaf.NotAfter, wantNotAfter)
	}
	if got := ca.NotAfter.Sub(ca.NotBefore); got < validity {
		t.Errorf("CA lifetime = %v, want at least %v", got, validity)
	}

	for _, eku := range []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth} {
		if !slices.Contains(leaf.ExtKeyUsage, eku) {
			t.Errorf("leaf is missing EKU %v (got %v)", eku, leaf.ExtKeyUsage)
		}
	}

	for _, name := range []string{
		testGRPCDomain,
		"manager-api.example.com",
		"*.dragonfly-system.svc",
		"dragonfly-manager.dragonfly-system.svc",
		"localhost",
	} {
		if !slices.Contains(leaf.DNSNames, name) {
			t.Errorf("leaf SANs %v do not contain %q", leaf.DNSNames, name)
		}
	}
	if len(leaf.IPAddresses) == 0 {
		t.Error("leaf has no IP SANs")
	}

	key, err := ParsePrivateKey(bundle.KeyPEM)
	if err != nil {
		t.Fatalf("ParsePrivateKey() error = %v", err)
	}
	if key.Curve != elliptic.P256() {
		t.Errorf("private key curve = %v, want P-256", key.Curve)
	}
	leafKey, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		t.Fatalf("leaf public key type = %T, want *ecdsa.PublicKey", leaf.PublicKey)
	}
	if !key.PublicKey.Equal(leafKey) {
		t.Error("private key does not match the leaf certificate")
	}
}

func TestGenerateRejectsInvalidOptions(t *testing.T) {
	tests := map[string]Options{
		"missing gRPC domain": {ManagerAPIURL: testAPIURL},
		"missing API URL":     {ManagerGRPCDomain: testGRPCDomain},
		"invalid API URL":     {ManagerGRPCDomain: testGRPCDomain, ManagerAPIURL: "not-a-url"},
	}
	for name, opts := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Generate(opts); err == nil {
				t.Fatal("Generate() error = nil, want an error")
			}
		})
	}
}

func TestParsePrivateKeyRejectsInvalidPEM(t *testing.T) {
	if _, err := ParsePrivateKey([]byte("not pem")); err == nil {
		t.Fatal("ParsePrivateKey() error = nil, want an error")
	}
	if _, err := ParseCertificates([]byte("not pem")); err == nil {
		t.Fatal("ParseCertificates() error = nil, want an error")
	}
}

func TestEnsureSecret(t *testing.T) {
	store := newFakeSecretStore()
	bundle := testBundle(t)

	created, err := EnsureSecret(store, "dragonfly-system", "dragonfly-p2p-mtls", bundle)
	if err != nil {
		t.Fatalf("EnsureSecret() error = %v", err)
	}
	if !created {
		t.Error("EnsureSecret() created = false, want true")
	}

	secret, err := store.Get("dragonfly-system", "dragonfly-p2p-mtls", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if secret.Type != corev1.SecretTypeTLS {
		t.Errorf("secret type = %q, want %q", secret.Type, corev1.SecretTypeTLS)
	}
	if len(secret.Data) != 3 {
		t.Errorf("secret keys = %v, want 3 keys", keysOf(secret.Data))
	}
	for _, key := range []string{"ca.crt", corev1.TLSCertKey, corev1.TLSPrivateKeyKey} {
		if len(secret.Data[key]) == 0 {
			t.Errorf("secret key %q is missing or empty", key)
		}
	}

	// A second call must not overwrite the existing Secret.
	replacement := &Bundle{
		CACertPEM: []byte("new-ca"),
		CertPEM:   []byte("new-cert"),
		KeyPEM:    []byte("new-key"),
	}
	created, err = EnsureSecret(store, "dragonfly-system", "dragonfly-p2p-mtls", replacement)
	if err != nil {
		t.Fatalf("EnsureSecret(second) error = %v", err)
	}
	if created {
		t.Error("EnsureSecret(second) created = true, want false")
	}

	unchanged, err := store.Get("dragonfly-system", "dragonfly-p2p-mtls", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if string(unchanged.Data[corev1.TLSCertKey]) != string(bundle.CertPEM) {
		t.Error("existing Secret tls.crt was overwritten")
	}
}

func TestEnsureSecretNilBundle(t *testing.T) {
	if _, err := EnsureSecret(newFakeSecretStore(), "ns", "name", nil); err == nil {
		t.Fatal("EnsureSecret(nil) error = nil, want an error")
	}
}

type fakeSecretStore struct {
	secrets map[string]*corev1.Secret
}

func newFakeSecretStore() *fakeSecretStore {
	return &fakeSecretStore{secrets: make(map[string]*corev1.Secret)}
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

func keysOf(data map[string][]byte) []string {
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	return keys
}

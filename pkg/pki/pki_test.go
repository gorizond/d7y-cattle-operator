package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"net"
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
	testGRPCDomain         = "manager-grpc.example.com"
	testAPIURL             = "https://manager-api.example.com"
	testSchedulerPublicIP  = "62.83.79.230"
	testSchedulerServiceIP = "10.43.79.230"
)

func testBundle(t *testing.T) *Bundle {
	t.Helper()
	bundle, err := Generate(Options{
		ManagerGRPCDomain:  testGRPCDomain,
		ManagerAPIURL:      testAPIURL,
		SchedulerPublicIP:  testSchedulerPublicIP,
		SchedulerServiceIP: testSchedulerServiceIP,
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	return bundle
}

func hasIP(ips []net.IP, want string) bool {
	for _, ip := range ips {
		if ip.String() == want {
			return true
		}
	}
	return false
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
	for _, want := range []string{testSchedulerPublicIP, testSchedulerServiceIP, "127.0.0.1", "::1"} {
		if !hasIP(leaf.IPAddresses, want) {
			t.Errorf("leaf IP SANs %v do not contain %q", leaf.IPAddresses, want)
		}
	}
	// Node IPs and the DNS alias are deliberately not certificate identities.
	for _, unwanted := range []string{"62.83.78.181", "62.83.78.222", "62.83.79.236"} {
		if hasIP(leaf.IPAddresses, unwanted) {
			t.Errorf("leaf IP SANs %v unexpectedly contain node IP %q", leaf.IPAddresses, unwanted)
		}
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
		"invalid public IP": {
			ManagerGRPCDomain: testGRPCDomain,
			ManagerAPIURL:     testAPIURL,
			SchedulerPublicIP: "not-an-ip",
		},
		"invalid service IP": {
			ManagerGRPCDomain:  testGRPCDomain,
			ManagerAPIURL:      testAPIURL,
			SchedulerServiceIP: "10.43.0.999",
		},
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

	result, err := EnsureSecret(store, "dragonfly-system", "dragonfly-p2p-mtls", bundle)
	if err != nil {
		t.Fatalf("EnsureSecret() error = %v", err)
	}
	if !result.Created || result.Updated {
		t.Errorf("EnsureSecret() result = %+v, want created only", result)
	}
	if result.Revision != CertRevision(bundle.CertPEM) {
		t.Errorf("revision = %q, want leaf hash", result.Revision)
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

	// A stable SAN set is a no-op even when the caller generated a fresh
	// bundle: the existing certificate and key must stay in place.
	fresh := testBundle(t)
	result, err = EnsureSecret(store, "dragonfly-system", "dragonfly-p2p-mtls", fresh)
	if err != nil {
		t.Fatalf("EnsureSecret(stable) error = %v", err)
	}
	if result.Created || result.Updated {
		t.Errorf("EnsureSecret(stable) result = %+v, want no-op", result)
	}
	if result.Revision != CertRevision(bundle.CertPEM) {
		t.Errorf("stable revision = %q, want existing leaf hash", result.Revision)
	}

	unchanged, err := store.Get("dragonfly-system", "dragonfly-p2p-mtls", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if string(unchanged.Data[corev1.TLSCertKey]) != string(bundle.CertPEM) {
		t.Error("existing Secret tls.crt was overwritten")
	}
}

func TestEnsureSecretRemovesLastAppliedAnnotation(t *testing.T) {
	store := newFakeSecretStore()
	bundle := testBundle(t)
	if _, err := EnsureSecret(store, "dragonfly-system", "dragonfly-p2p-mtls", bundle); err != nil {
		t.Fatalf("EnsureSecret() error = %v", err)
	}

	secret, err := store.Get("dragonfly-system", "dragonfly-p2p-mtls", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	secret.Annotations = map[string]string{lastAppliedAnnotation: `{"data":{"tls.key":"secret"}}`}
	if _, err := store.Update(secret); err != nil {
		t.Fatalf("seed annotation: %v", err)
	}

	result, err := EnsureSecret(store, "dragonfly-system", "dragonfly-p2p-mtls", testBundle(t))
	if err != nil {
		t.Fatalf("EnsureSecret() error = %v", err)
	}
	if !result.Updated {
		t.Error("EnsureSecret() updated = false, want metadata update")
	}
	cleaned, err := store.Get("dragonfly-system", "dragonfly-p2p-mtls", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if _, ok := cleaned.Annotations[lastAppliedAnnotation]; ok {
		t.Error("last-applied-configuration annotation was not removed")
	}
	if string(cleaned.Data[corev1.TLSCertKey]) != string(bundle.CertPEM) {
		t.Error("certificate changed while removing the annotation")
	}
}

func TestEnsureSecretRotatesWhenSANsChange(t *testing.T) {
	store := newFakeSecretStore()
	first := testBundle(t)
	if _, err := EnsureSecret(store, "dragonfly-system", "dragonfly-p2p-mtls", first); err != nil {
		t.Fatalf("EnsureSecret() error = %v", err)
	}

	rotated, err := Generate(Options{
		ManagerGRPCDomain:  testGRPCDomain,
		ManagerAPIURL:      testAPIURL,
		SchedulerPublicIP:  "62.83.79.231",
		SchedulerServiceIP: testSchedulerServiceIP,
	})
	if err != nil {
		t.Fatalf("Generate(rotated) error = %v", err)
	}
	result, err := EnsureSecret(store, "dragonfly-system", "dragonfly-p2p-mtls", rotated)
	if err != nil {
		t.Fatalf("EnsureSecret(rotated) error = %v", err)
	}
	if !result.Updated || result.Created {
		t.Errorf("EnsureSecret(rotated) result = %+v, want update", result)
	}
	if result.Revision != CertRevision(rotated.CertPEM) {
		t.Errorf("rotated revision = %q, want new leaf hash", result.Revision)
	}

	secret, err := store.Get("dragonfly-system", "dragonfly-p2p-mtls", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if string(secret.Data[corev1.TLSCertKey]) != string(rotated.CertPEM) {
		t.Error("rotated leaf was not written")
	}
	if string(secret.Data["ca.crt"]) != string(rotated.CACertPEM) {
		t.Error("CA was not regenerated with the leaf")
	}
}

func TestSANsMatchIgnoresOrderAndIPForms(t *testing.T) {
	leaf := testBundle(t)
	certs, err := ParseCertificates(leaf.CertPEM)
	if err != nil {
		t.Fatalf("ParseCertificates() error = %v", err)
	}
	if !SANsMatch(certs[0], leaf.DNSNames, leaf.IPAddresses) {
		t.Error("SANsMatch() = false for the generated SAN set")
	}
	if SANsMatch(certs[0], leaf.DNSNames, append(leaf.IPAddresses, net.ParseIP("10.43.79.231"))) {
		t.Error("SANsMatch() = true after adding an IP")
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

func (f *fakeSecretStore) Update(secret *corev1.Secret) (*corev1.Secret, error) {
	key := secret.Namespace + "/" + secret.Name
	if _, ok := f.secrets[key]; !ok {
		return nil, apierrors.NewNotFound(corev1.Resource("secrets"), secret.Name)
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

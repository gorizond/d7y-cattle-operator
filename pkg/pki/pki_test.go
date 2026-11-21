package pki

import (
	"crypto/x509"
	"errors"
	"net"
	"slices"
	"testing"

	wranglercorev1 "github.com/rancher/wrangler/v3/pkg/generated/controllers/core/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Wrangler's generated SecretController must satisfy SecretStore.
var _ SecretStore = (wranglercorev1.SecretController)(nil)

const (
	testGRPCDomain = "manager-grpc.example.com"
	testAPIHost    = "manager-api.example.com"
	testSeedIP     = "203.0.113.10"
)

func testCA(t *testing.T) *CA {
	t.Helper()
	store := newFakeSecretStore()
	ca, created, err := EnsureCA(store, "dragonfly-system", CASecretName, true, nil)
	if err != nil {
		t.Fatalf("EnsureCA() error = %v", err)
	}
	if !created {
		t.Fatal("EnsureCA() did not create the CA")
	}
	return ca
}

func TestEnsureCAStableAndCreateOnly(t *testing.T) {
	store := newFakeSecretStore()
	first, created, err := EnsureCA(store, "dragonfly-system", CASecretName, true, nil)
	if err != nil || !created {
		t.Fatalf("EnsureCA() = created %v, error %v", created, err)
	}
	if len(first.BundlePEM) == 0 {
		t.Fatal("CA bundle is empty")
	}
	secret, err := store.Get("dragonfly-system", CASecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get CA secret: %v", err)
	}
	if secret.Type != corev1.SecretTypeOpaque {
		t.Errorf("CA secret type = %q, want Opaque", secret.Type)
	}
	for _, key := range []string{"ca.crt", CAKey, CABundleKey} {
		if len(secret.Data[key]) == 0 {
			t.Errorf("CA secret key %q is empty", key)
		}
	}

	second, created, err := EnsureCA(store, "dragonfly-system", CASecretName, true, []byte("ignored legacy"))
	if err != nil || created {
		t.Fatalf("EnsureCA(existing) = created %v, error %v", created, err)
	}
	if string(second.CertPEM) != string(first.CertPEM) || string(second.KeyPEM) != string(first.KeyPEM) {
		t.Error("EnsureCA rotated a stable CA")
	}

	secret.Data["ca.key"] = []byte("invalid")
	store.secrets["dragonfly-system/"+CASecretName] = secret
	if _, _, err := EnsureCA(store, "dragonfly-system", CASecretName, true, nil); err == nil {
		t.Fatal("EnsureCA accepted invalid key material")
	}
}

func TestEnsureCARefusesCreateWhenLeavesExist(t *testing.T) {
	store := newFakeSecretStore()
	_, _, err := EnsureCA(store, "dragonfly-system", CASecretName, false, nil)
	if !errors.Is(err, ErrCAUnavailable) {
		t.Fatalf("EnsureCA() error = %v, want ErrCAUnavailable", err)
	}
}

func TestNewCAIncludesLegacyTrust(t *testing.T) {
	legacy, err := NewCA(nil)
	if err != nil {
		t.Fatalf("NewCA(legacy) error = %v", err)
	}
	ca, err := NewCA(legacy.CertPEM)
	if err != nil {
		t.Fatalf("NewCA() error = %v", err)
	}
	certs, err := ParseCertificates(ca.BundlePEM)
	if err != nil {
		t.Fatalf("parse bundle: %v", err)
	}
	if len(certs) != 2 {
		t.Fatalf("bundle certificates = %d, want new CA + legacy CA", len(certs))
	}
	if string(certs[0].Raw) != string(mustCert(t, ca.CertPEM).Raw) {
		t.Error("new CA is not first in the trust bundle")
	}
	if string(certs[1].Raw) != string(legacy.Cert.Raw) {
		t.Error("legacy CA is not present in the trust bundle")
	}
}

func TestCASignClientHasNoSANs(t *testing.T) {
	ca := testCA(t)
	leaf, err := ca.Sign(IssueOptions{CommonName: "d7y-cattle-operator-client", ClientAuth: true})
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	cert := mustCert(t, leaf.CertPEM)
	if len(cert.DNSNames) != 0 || len(cert.IPAddresses) != 0 {
		t.Errorf("client SANs = %v / %v, want none", cert.DNSNames, cert.IPAddresses)
	}
	if !slices.Equal(cert.ExtKeyUsage, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}) {
		t.Errorf("client EKU = %v, want ClientAuth only", cert.ExtKeyUsage)
	}
	if err := cert.CheckSignatureFrom(ca.Cert); err != nil {
		t.Errorf("client is not signed by the CA: %v", err)
	}
}

func TestCASignManagerSANs(t *testing.T) {
	ca := testCA(t)
	opts := IssueOptions{
		CommonName:  testGRPCDomain,
		DNSNames:    []string{testGRPCDomain, testAPIHost, "dragonfly-manager.dragonfly-system.svc", "localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		ServerAuth:  true,
		ClientAuth:  true,
	}
	leaf, err := ca.Sign(opts)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	cert := mustCert(t, leaf.CertPEM)
	if !SANsMatch(cert, opts.DNSNames, opts.IPAddresses) {
		t.Errorf("manager SANs = %v / %v", cert.DNSNames, cert.IPAddresses)
	}
	for _, eku := range []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth} {
		if !slices.Contains(cert.ExtKeyUsage, eku) {
			t.Errorf("manager EKU = %v, missing %v", cert.ExtKeyUsage, eku)
		}
	}
}

func TestCASignSchedulerSANs(t *testing.T) {
	ca := testCA(t)
	opts := IssueOptions{
		CommonName:  "d7y-scheduler.c-m-a.example.com",
		DNSNames:    []string{"d7y-scheduler.c-m-a.example.com", "c1-control-scheduler.dragonfly-system.svc"},
		IPAddresses: []net.IP{net.ParseIP(testSeedIP), net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		ServerAuth:  true,
		ClientAuth:  true,
	}
	leaf, err := ca.Sign(opts)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	cert := mustCert(t, leaf.CertPEM)
	if !SANsMatch(cert, opts.DNSNames, opts.IPAddresses) {
		t.Errorf("scheduler SANs = %v / %v", cert.DNSNames, cert.IPAddresses)
	}
}

func TestEnsureLeafStableThenRotates(t *testing.T) {
	store := newFakeSecretStore()
	ca := testCA(t)
	opts := IssueOptions{CommonName: "d7y-cattle-operator-client", ClientAuth: true}
	first, err := EnsureLeaf(store, "dragonfly-system", "dragonfly-p2p-mtls", ca, opts, false)
	if err != nil || !first.Created {
		t.Fatalf("EnsureLeaf() = %+v, %v", first, err)
	}
	secret, err := store.Get("dragonfly-system", "dragonfly-p2p-mtls", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get leaf: %v", err)
	}
	if secret.Type != corev1.SecretTypeTLS {
		t.Errorf("leaf type = %q", secret.Type)
	}
	firstCert := string(secret.Data[corev1.TLSCertKey])

	second, err := EnsureLeaf(store, "dragonfly-system", "dragonfly-p2p-mtls", ca, opts, false)
	if err != nil {
		t.Fatalf("EnsureLeaf(stable) error = %v", err)
	}
	if second.Updated || second.Created || second.Revision != first.Revision {
		t.Errorf("stable EnsureLeaf = %+v, want no-op", second)
	}
	secret, _ = store.Get("dragonfly-system", "dragonfly-p2p-mtls", metav1.GetOptions{})
	if string(secret.Data[corev1.TLSCertKey]) != firstCert {
		t.Error("stable leaf was rewritten")
	}

	rotatedOpts := opts
	rotatedOpts.DNSNames = []string{"new.example.com"}
	rotated, err := EnsureLeaf(store, "dragonfly-system", "dragonfly-p2p-mtls", ca, rotatedOpts, false)
	if err != nil || !rotated.Updated {
		t.Fatalf("EnsureLeaf(rotated) = %+v, %v", rotated, err)
	}
	secret, _ = store.Get("dragonfly-system", "dragonfly-p2p-mtls", metav1.GetOptions{})
	if string(secret.Data[corev1.TLSCertKey]) == firstCert {
		t.Error("leaf did not rotate after a SAN change")
	}
}

func TestEnsureLeafReusesLegacyIdentity(t *testing.T) {
	store := newFakeSecretStore()
	ca := testCA(t)
	legacyCA, err := NewCA(nil)
	if err != nil {
		t.Fatalf("NewCA(legacy) error = %v", err)
	}
	legacyLeaf, err := legacyCA.Sign(IssueOptions{CommonName: "legacy-client", ServerAuth: true, ClientAuth: true})
	if err != nil {
		t.Fatalf("Sign(legacy) error = %v", err)
	}
	legacy := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "dragonfly-p2p-mtls", Namespace: "dragonfly-system"},
		Type:       corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"ca.crt":                legacyCA.CertPEM,
			corev1.TLSCertKey:       legacyLeaf.CertPEM,
			corev1.TLSPrivateKeyKey: legacyLeaf.KeyPEM,
		},
	}
	if _, err := store.Create(legacy); err != nil {
		t.Fatalf("create legacy: %v", err)
	}
	result, err := EnsureLeaf(store, "dragonfly-system", "dragonfly-p2p-mtls", ca,
		IssueOptions{CommonName: "d7y-cattle-operator-client", ClientAuth: true}, true)
	if err != nil || !result.Updated {
		t.Fatalf("EnsureLeaf(reuse) = %+v, %v", result, err)
	}
	got, _ := store.Get("dragonfly-system", "dragonfly-p2p-mtls", metav1.GetOptions{})
	if string(got.Data[corev1.TLSCertKey]) != string(legacyLeaf.CertPEM) || string(got.Data[corev1.TLSPrivateKeyKey]) != string(legacyLeaf.KeyPEM) {
		t.Error("Phase A did not preserve legacy identity")
	}
	if string(got.Data["ca.crt"]) != string(ca.BundlePEM) {
		t.Error("Phase A did not install the new trust bundle")
	}
}

func TestSANsMatchOrderAndIPForms(t *testing.T) {
	ca := testCA(t)
	leaf, err := ca.Sign(IssueOptions{
		CommonName:  "manager",
		DNSNames:    []string{"b.example.com", "a.example.com"},
		IPAddresses: []net.IP{net.ParseIP(testSeedIP)},
		ServerAuth:  true,
	})
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	cert := mustCert(t, leaf.CertPEM)
	if !SANsMatch(cert, []string{"a.example.com", "b.example.com"}, []net.IP{net.ParseIP(testSeedIP)}) {
		t.Error("SANsMatch() = false for reordered SANs")
	}
	if SANsMatch(cert, []string{"a.example.com"}, []net.IP{net.ParseIP(testSeedIP)}) {
		t.Error("SANsMatch() = true for a missing DNS SAN")
	}
}

func TestSecretRevision(t *testing.T) {
	base := map[string][]byte{"ca.crt": []byte("ca"), corev1.TLSCertKey: []byte("cert"), corev1.TLSPrivateKeyKey: []byte("key")}
	first := SecretRevision(base)
	if first == "" || first != SecretRevision(base) {
		t.Fatal("SecretRevision is not stable")
	}
	changed := map[string][]byte{"ca.crt": []byte("ca"), corev1.TLSCertKey: []byte("new"), corev1.TLSPrivateKeyKey: []byte("key")}
	if SecretRevision(changed) == first {
		t.Error("SecretRevision did not change with tls.crt")
	}
}

func TestIsPublicIP(t *testing.T) {
	tests := map[string]struct {
		ip   net.IP
		want bool
	}{
		"public ipv4":  {net.ParseIP(testSeedIP), true},
		"public ipv6":  {net.ParseIP("2001:db8::1"), true},
		"private ipv4": {net.ParseIP("10.0.0.1"), false},
		"loopback":     {net.ParseIP("127.0.0.1"), false},
		"link local":   {net.ParseIP("169.254.1.1"), false},
		"multicast":    {net.ParseIP("224.0.0.1"), false},
		"nil":          {nil, false},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := IsPublicIP(tc.ip); got != tc.want {
				t.Errorf("IsPublicIP(%v) = %v, want %v", tc.ip, got, tc.want)
			}
		})
	}
}

func mustCert(t *testing.T, pemBytes []byte) *x509.Certificate {
	t.Helper()
	certs, err := ParseCertificates(pemBytes)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	return certs[0]
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

func TestLoadCARejectsBundleMissingActiveCA(t *testing.T) {
	active, err := NewCA(nil)
	if err != nil {
		t.Fatalf("NewCA(active) error = %v", err)
	}
	other, err := NewCA(nil)
	if err != nil {
		t.Fatalf("NewCA(other) error = %v", err)
	}
	if _, err := LoadCA(active.CertPEM, active.KeyPEM, other.CertPEM); err == nil {
		t.Fatal("LoadCA accepted a bundle missing the active CA")
	}
}

func TestEnsureLeafRepairsCorruptKey(t *testing.T) {
	store := newFakeSecretStore()
	ca := testCA(t)
	opts := IssueOptions{CommonName: "manager", DNSNames: []string{"manager.example.com"}, ServerAuth: true}
	if _, err := EnsureLeaf(store, "dragonfly-system", "leaf", ca, opts, false); err != nil {
		t.Fatalf("EnsureLeaf() error = %v", err)
	}
	secret, _ := store.Get("dragonfly-system", "leaf", metav1.GetOptions{})
	before := string(secret.Data[corev1.TLSCertKey])
	secret.Data[corev1.TLSPrivateKeyKey] = []byte("corrupt")
	if _, err := store.Update(secret); err != nil {
		t.Fatalf("corrupt key: %v", err)
	}
	result, err := EnsureLeaf(store, "dragonfly-system", "leaf", ca, opts, false)
	if err != nil || !result.Updated {
		t.Fatalf("EnsureLeaf(repair) = %+v, %v", result, err)
	}
	repaired, _ := store.Get("dragonfly-system", "leaf", metav1.GetOptions{})
	if string(repaired.Data[corev1.TLSCertKey]) == before {
		t.Error("corrupt key was not repaired with a new leaf")
	}
	if !KeyMatches(repaired.Data[corev1.TLSCertKey], repaired.Data[corev1.TLSPrivateKeyKey]) {
		t.Error("repaired leaf key does not match the certificate")
	}
}

func TestEnsureLeafLabelsV2(t *testing.T) {
	store := newFakeSecretStore()
	ca := testCA(t)
	if _, err := EnsureLeaf(store, "dragonfly-system", "leaf", ca, IssueOptions{CommonName: "client", ClientAuth: true}, false); err != nil {
		t.Fatalf("EnsureLeaf() error = %v", err)
	}
	secret, _ := store.Get("dragonfly-system", "leaf", metav1.GetOptions{})
	if secret.Labels[LeafGenerationLabel] != LeafGenerationV2 || secret.Labels[ManagedByLabel] != ManagedByValue {
		t.Errorf("leaf labels = %v", secret.Labels)
	}
}

func TestSANsCoverWildcardService(t *testing.T) {
	ca := testCA(t)
	leaf, err := ca.Sign(IssueOptions{
		CommonName: "legacy",
		DNSNames:   []string{"*.dragonfly-system.svc"},
		ServerAuth: true,
	})
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	cert := mustCert(t, leaf.CertPEM)
	if !SANsCover(cert, []string{"c1-control-scheduler.dragonfly-system.svc"}, nil) {
		t.Error("wildcard service SAN did not cover the scheduler Service")
	}
	if SANsCover(cert, []string{"a.b.dragonfly-system.svc"}, nil) {
		t.Error("wildcard service SAN covered a multi-label prefix")
	}
}

package machinebootstrap

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/gorizond/d7y-cattle-operator/pkg/bootstrap"
)

func testECDSAIdentity(t *testing.T) (caPEM, certPEM, keyPEM []byte) {
	t.Helper()
	now := time.Now()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "d7y-test-ca"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "d7y-test-client"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caTemplate, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create client certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		t.Fatalf("marshal client key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

func TestShortBootstrapSecretFitsHetznerUserData(t *testing.T) {
	controller, secrets, _, clusters, _, _ := testController()
	caPEM, certPEM, keyPEM := testECDSAIdentity(t)
	secrets.mtls.Data = map[string][]byte{
		"ca.crt":                caPEM,
		corev1.TLSCertKey:       certPEM,
		corev1.TLSPrivateKeyKey: keyPEM,
	}
	setPreinstall(clusters, "echo pre")

	reconcileNow(t, controller, secrets)
	value := string(secrets.secret.Data[bootstrapValueKey])
	raw, err := json.Marshal(secrets.secret)
	if err != nil {
		t.Fatalf("json.Marshal(secret) error = %v", err)
	}
	if len(raw) >= 32*1024 {
		t.Fatalf("serialized Secret = %d bytes, want < 32 KiB", len(raw))
	}
	if strings.Contains(value, "?cfg=") {
		t.Fatalf("bootstrap value still carries a cfg query: %q", value)
	}
	if !strings.Contains(value, "set -a\n") || !strings.Contains(value, "set +a\n") {
		t.Fatalf("bootstrap value does not export the env around the p2p fetch: %q", value)
	}
	if !strings.Contains(value, `curl -sfL "http://static.example.com/p2p-install.sh" | bash || true`) {
		t.Fatalf("bootstrap value is missing the plain static script fetch: %q", value)
	}
	if !strings.Contains(value, shortInstallSignature) {
		t.Fatalf("short p2p signature is missing: %q", value)
	}
	if strings.Contains(value, "D7Y_P2P_EOF") || strings.Contains(value, "Bootstrap a new RKE2/k3s node") {
		t.Fatalf("inline p2p script body leaked into the Secret: %q", value)
	}
	for _, want := range []string{
		"P2P_DISTRO='k3s'\n",
		"SCHEDULER_IPS=''\n",
		"CA_CRT_B64='",
		"CLIENT_CRT_B64='",
		"CLIENT_KEY_B64='",
	} {
		if !strings.Contains(value, want) {
			t.Fatalf("bootstrap env is missing %q", want)
		}
	}
	preAt := strings.Index(value, bootstrap.PreinstallMarker)
	p2pAt := strings.Index(value, bootstrap.Marker)
	if preAt < 0 || p2pAt < 0 || preAt > p2pAt {
		t.Fatalf("pre-install block is not before the p2p block: %q", value)
	}
}

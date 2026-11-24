package machinebootstrap

import (
	"bytes"
	"compress/gzip"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
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

// realisticRKE2Bootstrap returns a synthetic Rancher node-driver bootstrap of
// roughly the size seen on a live RKE2 node (~38 KiB). The content is
// deliberately varied shell/cloud-init text so the gzip ratio is not trivial.
func realisticRKE2Bootstrap() string {
	var builder strings.Builder
	builder.WriteString("#!/bin/bash\nset -euo pipefail\n")
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&builder, "# RKE2 bootstrap step %d: render /etc/rancher/rke2/config.yaml and start the agent\n", i)
		fmt.Fprintf(&builder, "echo 'step %d: installing rke2 version v1.35.%d with the registry mirror'\n", i, i%10)
		fmt.Fprintf(&builder, "curl -fsSL https://registry.example.test/v2/rke2/releases/%d/rke2-images-%d.tar.gz -o /tmp/rke2-%d.tar.gz\n", i, i, i)
		fmt.Fprintf(&builder, "install -m 0644 /tmp/rke2-%d.tar.gz /var/lib/rancher/rke2/agent/images/%d.tar.gz\n", i, i)
	}
	builder.WriteString("do_install \"$@\"\nexit 0\n")
	return builder.String()
}

func effectiveUserData(t *testing.T, value string) int {
	t.Helper()
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write([]byte(value)); err != nil {
		t.Fatalf("gzip bootstrap value: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	return len(base64.StdEncoding.EncodeToString(compressed.Bytes()))
}

func TestShortBootstrapFitsEffectiveUserDataLimit(t *testing.T) {
	fixture := newWebhookFixture()
	caPEM, certPEM, keyPEM := testECDSAIdentity(t)
	fixture.secrets["operator-system/p2p-mtls"].Data = map[string][]byte{
		"ca.crt":                caPEM,
		corev1.TLSCertKey:       certPEM,
		corev1.TLSPrivateKeyKey: keyPEM,
	}
	fixture.secret.Data[bootstrapValueKey] = []byte(realisticRKE2Bootstrap())
	fixture.clusters["cluster-ns/cluster-1"].Annotations[bootstrap.PreinstallAnnotation] = "echo pre"

	response := mutate(t, fixture.webhook, fixture.secret)
	if !response.Allowed || len(response.Patch) == 0 {
		t.Fatalf("response = %+v, want an allowed patch", response)
	}
	value := string(applyPatch(t, fixture.secret, response.Patch).Data[bootstrapValueKey])
	if len(value) <= 32*1024 {
		t.Fatalf("fixture bootstrap value = %d bytes, want > 32 KiB so the effective user_data metric is exercised", len(value))
	}
	if effective := effectiveUserData(t, value); effective >= 32*1024 {
		t.Fatalf("effective user_data (gzip+b64) = %d bytes, want < 32 KiB", effective)
	} else {
		t.Logf("bootstrap value = %d bytes; effective user_data (gzip+b64) = %d bytes", len(value), effective)
	}
	if strings.Contains(value, "?cfg=") {
		t.Fatalf("bootstrap value still carries a cfg query: %q", value)
	}
	if !strings.Contains(value, "set -a\n") || !strings.Contains(value, "set +a\n") {
		t.Fatalf("bootstrap value does not export the env around the p2p fetch: %q", value)
	}
	if !strings.Contains(value, `curl -sfL "https://static.example.com/p2p-install.sh" | bash || true`) {
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

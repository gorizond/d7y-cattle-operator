// Package webhook wires the admission server, serving certificate, and
// needacert lifecycle.
package webhook

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	admissioncontrollers "github.com/rancher/wrangler/v3/pkg/generated/controllers/admissionregistration.k8s.io/v1"
	apiextcontrollers "github.com/rancher/wrangler/v3/pkg/generated/controllers/apiextensions.k8s.io/v1"
	corecontrollers "github.com/rancher/wrangler/v3/pkg/generated/controllers/core/v1"
	"github.com/rancher/wrangler/v3/pkg/needacert"
)

// Register starts the needacert controller. The webhook Service carries the
// need-a-cert.cattle.io/secret-name annotation; needacert creates/rotates the
// TLS Secret and injects its CA bundle into the MutatingWebhookConfiguration.
func Register(ctx context.Context,
	secrets corecontrollers.SecretController,
	services corecontrollers.ServiceController,
	mutating admissioncontrollers.MutatingWebhookConfigurationController,
	validating admissioncontrollers.ValidatingWebhookConfigurationController,
	crds apiextcontrollers.CustomResourceDefinitionController) {
	needacert.Register(ctx, secrets, services, mutating, validating, crds)
}

// CertLoader serves the needacert Secret when it is mounted and falls back to
// an in-memory self-signed certificate only while the Secret is unavailable.
// The fallback is not trusted by the webhook caBundle, but failurePolicy=Ignore
// keeps node creation unblocked.
type CertLoader struct {
	dir      string
	fallback *tls.Certificate
	once     sync.Once
}

// NewCertLoader prepares the loader and its self-signed fallback.
func NewCertLoader(dir, serviceName, namespace string) (*CertLoader, error) {
	certificate, err := selfSignedCertificate(serviceName, namespace)
	if err != nil {
		return nil, err
	}
	return &CertLoader{dir: dir, fallback: certificate}, nil
}

// GetCertificate reloads the mounted needacert files on each handshake, so a
// Secret created or rotated after startup is picked up without a restart.
func (l *CertLoader) GetCertificate(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
	certificate, err := tls.LoadX509KeyPair(
		filepath.Join(l.dir, "tls.crt"),
		filepath.Join(l.dir, "tls.key"),
	)
	if err == nil {
		return &certificate, nil
	}
	l.once.Do(func() {
		slog.Warn("webhook serving certificate is not available; using self-signed fallback until needacert Secret is mounted",
			"dir", l.dir,
		)
	})
	return l.fallback, nil
}

func selfSignedCertificate(serviceName, namespace string) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("webhook: generate fallback key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		return nil, fmt.Errorf("webhook: generate fallback serial: %w", err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: serviceName},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames: []string{
			serviceName,
			serviceName + "." + namespace,
			serviceName + "." + namespace + ".svc",
			serviceName + "." + namespace + ".svc.cluster.local",
			"localhost",
		},
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("webhook: create fallback certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("webhook: marshal fallback key: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("webhook: load fallback certificate: %w", err)
	}
	return &certificate, nil
}

// EnsureCertDir exists for chart/documentation symmetry and validates the
// mount path early without requiring the Secret to exist.
func EnsureCertDir(dir string) error {
	if dir == "" {
		return fmt.Errorf("webhook: certificate directory is empty")
	}
	if info, err := os.Stat(dir); err == nil && !info.IsDir() {
		return fmt.Errorf("webhook: %s is not a directory", dir)
	}
	return nil
}

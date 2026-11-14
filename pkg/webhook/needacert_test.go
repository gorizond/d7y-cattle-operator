package webhook

import (
	"crypto/x509"
	"testing"
)

func TestCertLoaderFallback(t *testing.T) {
	loader, err := NewCertLoader("/nonexistent-webhook-certs", "d7y-cattle-operator-webhook", "dragonfly-system")
	if err != nil {
		t.Fatalf("NewCertLoader() error = %v", err)
	}
	certificate, err := loader.GetCertificate(nil)
	if err != nil {
		t.Fatalf("GetCertificate() error = %v", err)
	}
	if certificate == nil || len(certificate.Certificate) == 0 {
		t.Fatal("fallback certificate is empty")
	}
	parsed, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		t.Fatalf("parse fallback certificate: %v", err)
	}
	found := false
	for _, name := range parsed.DNSNames {
		if name == "d7y-cattle-operator-webhook.dragonfly-system.svc" {
			found = true
		}
	}
	if !found {
		t.Errorf("fallback DNS names = %v", parsed.DNSNames)
	}
}

func TestEnsureCertDir(t *testing.T) {
	if err := EnsureCertDir("/tmp"); err != nil {
		t.Errorf("EnsureCertDir(/tmp) error = %v", err)
	}
	if err := EnsureCertDir(""); err == nil {
		t.Error("EnsureCertDir(\"\") error = nil, want an error")
	}
}

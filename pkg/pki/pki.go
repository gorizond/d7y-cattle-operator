// Package pki manages the stable P2P CA and the client, manager, and per-seed
// scheduler leaf certificates.
package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
)

const (
	// CASecretName is the operator-namespace Secret holding the stable CA.
	CASecretName = "dragonfly-p2p-ca"
	// CAKey is the PEM private key for the stable CA.
	CAKey = "ca.key"
	// CABundleKey is the trust bundle used by leaf Secrets.
	CABundleKey = "ca-bundle.crt"
	// LeafGenerationLabel marks leaves managed by the v2 split-identity PKI.
	LeafGenerationLabel = "p2p.dragonfly.io/pki-generation"
	// LeafGenerationV2 is the current leaf generation.
	LeafGenerationV2 = "v2"
	// ManagedByLabel marks operator-managed PKI Secrets.
	ManagedByLabel = "app.kubernetes.io/managed-by"
	// ManagedByValue is the operator's managed-by value.
	ManagedByValue = "d7y-cattle-operator"
	// CAAnnotationLegacyTrustDeadline stores the global Phase A deadline on the
	// CA Secret.
	CAAnnotationLegacyTrustDeadline = "p2p.dragonfly.io/legacy-trust-deadline"

	// validity is the lifetime of the CA and leaf certificates.
	validity = 100 * 365 * 24 * time.Hour
)

// ErrCAUnavailable means the stable CA is missing but leaf Secrets already
// exist. The operator must not generate a replacement CA in that state.
var ErrCAUnavailable = errors.New("pki: CA Secret is missing while leaf Secrets exist")

// IssueOptions describes one leaf identity.
type IssueOptions struct {
	CommonName  string
	DNSNames    []string
	IPAddresses []net.IP
	ServerAuth  bool
	ClientAuth  bool
}

// CA is the stable operator CA. KeyPEM is persisted in the CA Secret and is
// never regenerated automatically.
type CA struct {
	CertPEM   []byte
	KeyPEM    []byte
	BundlePEM []byte
	Cert      *x509.Certificate
	Key       *ecdsa.PrivateKey
}

// Bundle is a leaf certificate and its trust bundle.
type Bundle struct {
	CACertPEM []byte
	CertPEM   []byte
	KeyPEM    []byte
}

// NewCA creates a CA and, when legacyCACertPEM is present, writes a trust
// bundle containing the new CA followed by the legacy CA.
func NewCA(legacyCACertPEM []byte) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("pki: generate CA key: %w", err)
	}
	serial, err := newSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "d7y-cattle-operator CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(validity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("pki: create CA certificate: %w", err)
	}
	certPEM := encodePEM("CERTIFICATE", der)
	bundle := append([]byte(nil), certPEM...)
	if len(legacyCACertPEM) > 0 {
		if _, err := ParseCertificates(legacyCACertPEM); err != nil {
			return nil, fmt.Errorf("pki: parse legacy CA: %w", err)
		}
		bundle = append(bundle, legacyCACertPEM...)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("pki: marshal CA key: %w", err)
	}
	return loadCA(certPEM, encodePEM("PRIVATE KEY", keyDER), bundle)
}

// LoadCA validates an existing CA bundle. Invalid material is returned as an
// error and is never regenerated.
func LoadCA(certPEM, keyPEM, bundlePEM []byte) (*CA, error) {
	if len(bundlePEM) == 0 {
		bundlePEM = certPEM
	}
	if len(bundlePEM) > 0 {
		bundleCerts, err := ParseCertificates(bundlePEM)
		if err != nil {
			return nil, fmt.Errorf("pki: parse CA bundle: %w", err)
		}
		active, err := ParseCertificates(certPEM)
		if err != nil {
			return nil, fmt.Errorf("pki: parse active CA: %w", err)
		}
		found := false
		for _, bundled := range bundleCerts {
			if string(bundled.Raw) == string(active[0].Raw) {
				found = true
				break
			}
		}
		if !found {
			return nil, errors.New("pki: CA bundle does not contain the active CA in ca.crt")
		}
	}
	return loadCA(certPEM, keyPEM, bundlePEM)
}

func loadCA(certPEM, keyPEM, bundlePEM []byte) (*CA, error) {
	certs, err := ParseCertificates(certPEM)
	if err != nil {
		return nil, fmt.Errorf("pki: parse CA certificate: %w", err)
	}
	cert := certs[0]
	key, err := ParsePrivateKey(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("pki: parse CA key: %w", err)
	}
	public, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !public.Equal(&key.PublicKey) {
		return nil, errors.New("pki: CA certificate and private key do not match")
	}
	if !cert.IsCA {
		return nil, errors.New("pki: CA certificate does not have IsCA")
	}
	if cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		return nil, errors.New("pki: CA certificate does not have KeyUsageCertSign")
	}
	return &CA{
		CertPEM:   append([]byte(nil), certPEM...),
		KeyPEM:    append([]byte(nil), keyPEM...),
		BundlePEM: append([]byte(nil), bundlePEM...),
		Cert:      cert,
		Key:       key,
	}, nil
}

// SecretData returns the Opaque CA Secret data.
func (ca *CA) SecretData() map[string][]byte {
	return map[string][]byte{
		"ca.crt":    append([]byte(nil), ca.CertPEM...),
		CAKey:       append([]byte(nil), ca.KeyPEM...),
		CABundleKey: append([]byte(nil), ca.BundlePEM...),
	}
}

// Sign issues a leaf certificate from the stable CA.
func (ca *CA) Sign(opts IssueOptions) (*Bundle, error) {
	if ca == nil || ca.Cert == nil || ca.Key == nil {
		return nil, errors.New("pki: CA is not loaded")
	}
	commonName := strings.TrimSpace(opts.CommonName)
	if commonName == "" {
		return nil, errors.New("pki: common name is required")
	}
	var eku []x509.ExtKeyUsage
	if opts.ServerAuth {
		eku = append(eku, x509.ExtKeyUsageServerAuth)
	}
	if opts.ClientAuth {
		eku = append(eku, x509.ExtKeyUsageClientAuth)
	}
	if len(eku) == 0 {
		return nil, errors.New("pki: at least one extended key usage is required")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("pki: generate leaf key: %w", err)
	}
	serial, err := newSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	notAfter := now.Add(validity)
	if ca.Cert.NotAfter.Before(notAfter) {
		notAfter = ca.Cert.NotAfter
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              notAfter,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           eku,
		DNSNames:              NormalizeDNSNames(opts.DNSNames),
		IPAddresses:           NormalizeIPs(opts.IPAddresses),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		return nil, fmt.Errorf("pki: create leaf certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("pki: marshal leaf key: %w", err)
	}
	return &Bundle{
		CACertPEM: append([]byte(nil), ca.BundlePEM...),
		CertPEM:   encodePEM("CERTIFICATE", der),
		KeyPEM:    encodePEM("PRIVATE KEY", keyDER),
	}, nil
}

// ParseCertificates decodes all certificates from a PEM bundle.
func ParseCertificates(pemBytes []byte) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	rest := pemBytes
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("pki: parse certificate: %w", err)
		}
		certs = append(certs, cert)
	}
	if len(certs) == 0 {
		return nil, errors.New("pki: no certificates found")
	}
	return certs, nil
}

// ParsePrivateKey decodes an ECDSA private key from PKCS#8 or SEC 1 PEM.
func ParsePrivateKey(pemBytes []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("pki: no PEM block found")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		ecKey, ok := key.(*ecdsa.PrivateKey)
		if !ok {
			return nil, errors.New("pki: private key is not ECDSA")
		}
		return ecKey, nil
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("pki: parse private key: %w", err)
	}
	return key, nil
}

// NormalizeDNSNames trims and deduplicates DNS SANs.
func NormalizeDNSNames(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		name := strings.TrimRight(strings.TrimSpace(value), ".")
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, name)
	}
	return result
}

// NormalizeIPs deduplicates IP SANs.
func NormalizeIPs(values []net.IP) []net.IP {
	seen := make(map[string]struct{}, len(values))
	result := make([]net.IP, 0, len(values))
	for _, value := range values {
		if value == nil {
			continue
		}
		key := value.String()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, append(net.IP(nil), value...))
	}
	return result
}

// SANsCover reports whether the certificate carries every desired DNS and IP
// SAN. Extra SANs are allowed; this is the Phase A legacy-server check.
func SANsCover(cert *x509.Certificate, dnsNames []string, ips []net.IP) bool {
	if cert == nil {
		return false
	}
	gotDNS := map[string]struct{}{}
	for _, name := range cert.DNSNames {
		gotDNS[strings.ToLower(name)] = struct{}{}
	}
	for _, name := range NormalizeDNSNames(dnsNames) {
		if _, ok := gotDNS[strings.ToLower(name)]; ok {
			continue
		}
		if !dnsWildcardCovered(gotDNS, name) {
			return false
		}
	}
	gotIPs := map[string]struct{}{}
	for _, ip := range cert.IPAddresses {
		gotIPs[ip.String()] = struct{}{}
	}
	for _, ip := range NormalizeIPs(ips) {
		if _, ok := gotIPs[ip.String()]; !ok {
			return false
		}
	}
	return true
}

// KeyMatches reports whether the certificate public key matches the private key.
func KeyMatches(certPEM, keyPEM []byte) bool {
	return keyMatches(certPEM, keyPEM)
}

// HasServerAuth reports whether the certificate carries ServerAuth EKU.
func HasServerAuth(cert *x509.Certificate) bool {
	return cert != nil && containsEKU(cert.ExtKeyUsage, x509.ExtKeyUsageServerAuth)
}

// HasClientAuth reports whether the certificate carries ClientAuth EKU.
func HasClientAuth(cert *x509.Certificate) bool {
	return cert != nil && containsEKU(cert.ExtKeyUsage, x509.ExtKeyUsageClientAuth)
}

func dnsWildcardCovered(got map[string]struct{}, name string) bool {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	for candidate := range got {
		if !strings.HasPrefix(candidate, "*.") {
			continue
		}
		suffix := candidate[1:]
		if !strings.HasSuffix(name, suffix) {
			continue
		}
		prefix := strings.TrimSuffix(name, suffix)
		if prefix != "" && !strings.Contains(prefix, ".") {
			return true
		}
	}
	return false
}

func containsEKU(values []x509.ExtKeyUsage, want x509.ExtKeyUsage) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// SANsMatch reports whether the certificate carries exactly the desired DNS
// and IP SAN sets, ignoring order and IPv4-mapped IPv6 forms.
func SANsMatch(cert *x509.Certificate, dnsNames []string, ips []net.IP) bool {
	if cert == nil {
		return false
	}
	wantDNS := make(map[string]struct{}, len(dnsNames))
	for _, name := range NormalizeDNSNames(dnsNames) {
		wantDNS[strings.ToLower(name)] = struct{}{}
	}
	gotDNS := make(map[string]struct{}, len(cert.DNSNames))
	for _, name := range cert.DNSNames {
		gotDNS[strings.ToLower(name)] = struct{}{}
	}
	if len(wantDNS) != len(gotDNS) {
		return false
	}
	for name := range wantDNS {
		if _, ok := gotDNS[name]; !ok {
			return false
		}
	}
	wantIPs := make(map[string]struct{}, len(ips))
	for _, ip := range NormalizeIPs(ips) {
		wantIPs[ip.String()] = struct{}{}
	}
	gotIPs := make(map[string]struct{}, len(cert.IPAddresses))
	for _, ip := range cert.IPAddresses {
		gotIPs[ip.String()] = struct{}{}
	}
	if len(wantIPs) != len(gotIPs) {
		return false
	}
	for ip := range wantIPs {
		if _, ok := gotIPs[ip]; !ok {
			return false
		}
	}
	return true
}

// IsPublicIP reports whether ip is a globally routable unicast address that
// may be used as a public P2P certificate SAN.
func IsPublicIP(ip net.IP) bool {
	if ip == nil || !ip.IsGlobalUnicast() {
		return false
	}
	return !ip.IsLoopback() && !ip.IsUnspecified() && !ip.IsPrivate() &&
		!ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !ip.IsMulticast()
}

// SecretRevision returns a stable hash of ca.crt, tls.crt, and tls.key.
func SecretRevision(data map[string][]byte) string {
	hash := sha256.New()
	for _, key := range []string{"ca.crt", corev1.TLSCertKey, corev1.TLSPrivateKeyKey} {
		value := data[key]
		hash.Write([]byte(key))
		hash.Write([]byte{0})
		hash.Write(value)
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func newSerial() (*big.Int, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("pki: generate serial number: %w", err)
	}
	return serial, nil
}

func encodePEM(blockType string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
}

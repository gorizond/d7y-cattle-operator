// Package pki generates the mTLS material shared by the operator and Dragonfly
// and stores it in a Kubernetes TLS Secret.
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
	"net/url"
	"strings"
	"time"
)

const (
	// validity is the lifetime of both the CA and the leaf certificate.
	validity = 100 * 365 * 24 * time.Hour

	// Dragonfly service DNS names are always in the Dragonfly namespace,
	// regardless of where the operator itself is installed.
	dragonflyNamespace = "dragonfly-system"
)

// Options describes the certificate identities.
type Options struct {
	// ManagerGRPCDomain is the public gRPC hostname (without scheme).
	ManagerGRPCDomain string
	// ManagerAPIURL is the public REST API URL; its hostname becomes a SAN.
	ManagerAPIURL string
	// SchedulerPublicIPs are the IPs resolved from the per-cluster scheduler
	// host. They are public Traefik LB addresses used by bootstrap clients.
	SchedulerPublicIPs []string
	// SchedulerServiceIP is the pinned in-cluster ClusterIP of the scheduler
	// client Service used by DaemonSets.
	SchedulerServiceIP string
}

// Bundle holds PEM-encoded mTLS material. The CA private key is intentionally
// discarded: when the SAN set changes the operator generates a new CA and
// leaf instead of re-signing the existing leaf.
type Bundle struct {
	CACertPEM   []byte
	CertPEM     []byte
	KeyPEM      []byte
	DNSNames    []string
	IPAddresses []net.IP
}

// Generate creates a self-signed CA and a leaf certificate valid for the
// manager gRPC and REST endpoints plus the Dragonfly service DNS names.
func Generate(opts Options) (*Bundle, error) {
	if opts.ManagerGRPCDomain == "" {
		return nil, errors.New("pki: manager gRPC domain is required")
	}
	apiURL, err := url.Parse(opts.ManagerAPIURL)
	if err != nil || apiURL.Hostname() == "" {
		return nil, fmt.Errorf("pki: invalid manager API URL %q", opts.ManagerAPIURL)
	}

	ips, err := schedulerIPs(opts.SchedulerPublicIPs, opts.SchedulerServiceIP)
	if err != nil {
		return nil, err
	}
	dnsNames := leafDNSNames(opts.ManagerGRPCDomain, apiURL.Hostname())

	now := time.Now()
	notBefore := now.Add(-time.Hour)
	notAfter := now.Add(validity)

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("pki: generate CA key: %w", err)
	}
	caSerial, err := newSerial()
	if err != nil {
		return nil, err
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          caSerial,
		Subject:               pkix.Name{CommonName: "d7y-cattle-operator CA"},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("pki: create CA certificate: %w", err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("pki: generate leaf key: %w", err)
	}
	leafSerial, err := newSerial()
	if err != nil {
		return nil, err
	}
	leafTemplate := &x509.Certificate{
		SerialNumber:          leafSerial,
		Subject:               pkix.Name{CommonName: opts.ManagerGRPCDomain},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:              dnsNames,
		IPAddresses:           ips,
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caTemplate, &leafKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("pki: create leaf certificate: %w", err)
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		return nil, fmt.Errorf("pki: marshal leaf key: %w", err)
	}

	return &Bundle{
		CACertPEM:   encodePEM("CERTIFICATE", caDER),
		CertPEM:     encodePEM("CERTIFICATE", leafDER),
		KeyPEM:      encodePEM("PRIVATE KEY", keyDER),
		DNSNames:    dnsNames,
		IPAddresses: ips,
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

func leafDNSNames(grpcDomain, apiHost string) []string {
	candidates := []string{
		grpcDomain,
		apiHost,
		"*." + dragonflyNamespace + ".svc",
		"dragonfly-manager." + dragonflyNamespace + ".svc",
		"localhost",
	}

	seen := make(map[string]struct{}, len(candidates))
	names := make([]string, 0, len(candidates))
	for _, name := range candidates {
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names
}

// schedulerIPs returns the loopback addresses plus the configured scheduler
// identities. Only the public LB IP and the internal ClusterIP are included:
// node IPs are deliberately never part of the P2P certificate.
func schedulerIPs(publicIPs []string, serviceIP string) ([]net.IP, error) {
	ips := []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}
	for _, raw := range append(append([]string(nil), publicIPs...), serviceIP) {
		if raw == "" {
			continue
		}
		ip := net.ParseIP(raw)
		if ip == nil {
			return nil, fmt.Errorf("pki: invalid scheduler IP %q", raw)
		}
		ips = append(ips, ip)
	}

	seen := make(map[string]struct{}, len(ips))
	result := make([]net.IP, 0, len(ips))
	for _, ip := range ips {
		key := ip.String()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, ip)
	}
	return result, nil
}

// SANsMatch reports whether the certificate already carries exactly the
// desired DNS and IP SAN sets. Ordering and IPv4-mapped IPv6 forms are
// normalized so a stable configuration never rewrites the Secret.
func SANsMatch(cert *x509.Certificate, dnsNames []string, ips []net.IP) bool {
	if cert == nil {
		return false
	}

	wantDNS := make(map[string]struct{}, len(dnsNames))
	for _, name := range uniqueStrings(dnsNames) {
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
	for _, ip := range uniqueIPs(ips) {
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

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}

func uniqueIPs(values []net.IP) []net.IP {
	seen := make(map[string]struct{}, len(values))
	result := make([]net.IP, 0, len(values))
	for _, value := range values {
		key := value.String()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}

// CertRevision returns a stable identifier for a leaf certificate. It is used
// as a pod template annotation so only a real certificate change rolls pods.
func CertRevision(certPEM []byte) string {
	sum := sha256.Sum256(certPEM)
	return hex.EncodeToString(sum[:])
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

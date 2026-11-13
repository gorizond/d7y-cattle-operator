// Package pki generates the mTLS material shared by the operator and Dragonfly
// and stores it in a Kubernetes TLS Secret.
package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
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
}

// Bundle holds PEM-encoded mTLS material. The CA private key is intentionally
// discarded: the Secret is created once and never rotated by the operator.
type Bundle struct {
	CACertPEM []byte
	CertPEM   []byte
	KeyPEM    []byte
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
		DNSNames:              leafDNSNames(opts.ManagerGRPCDomain, apiURL.Hostname()),
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
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
		CACertPEM: encodePEM("CERTIFICATE", caDER),
		CertPEM:   encodePEM("CERTIFICATE", leafDER),
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

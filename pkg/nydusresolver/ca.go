package nydusresolver

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const caCommonName = "nydus-resolver local CA"

// CA is a local certificate authority used to mint leaf certificates for
// registry hosts intercepted by the resolver.
type CA struct {
	cert    *x509.Certificate
	key     crypto.Signer
	certPEM []byte
}

// CertPEM returns the PEM-encoded CA certificate.
func (ca *CA) CertPEM() []byte {
	return append([]byte(nil), ca.certPEM...)
}

// DefaultCAKeyPath derives the CA key path from the CA certificate path. A
// .crt suffix is replaced with .key; other names get a .key suffix.
func DefaultCAKeyPath(certPath string) string {
	if strings.EqualFold(filepath.Ext(certPath), ".crt") {
		return strings.TrimSuffix(certPath, filepath.Ext(certPath)) + ".key"
	}
	return certPath + ".key"
}

// LoadOrCreateCA loads a CA from certFile/keyFile when both are given. With
// neither flag it loads certOut and its derived key path, generating and
// persisting a new CA on first start. Partial CA material is an error: the
// resolver never silently replaces a CA that clients may already trust.
func LoadOrCreateCA(certFile, keyFile, certOut string) (*CA, error) {
	if certFile != "" || keyFile != "" {
		if certFile == "" || keyFile == "" {
			return nil, errors.New("--ca-cert and --ca-key must be set together")
		}
		return loadCA(certFile, keyFile)
	}
	if certOut == "" {
		return nil, errors.New("CA certificate output path is required")
	}
	keyOut := DefaultCAKeyPath(certOut)
	certExists, err := regularFileExists(certOut)
	if err != nil {
		return nil, err
	}
	keyExists, err := regularFileExists(keyOut)
	if err != nil {
		return nil, err
	}
	if certExists && keyExists {
		return loadCA(certOut, keyOut)
	}
	if certExists != keyExists {
		return nil, fmt.Errorf("incomplete CA material: %s exists=%t, %s exists=%t; remove both files to regenerate",
			certOut, certExists, keyOut, keyExists)
	}

	ca, keyPEM, err := generateCA()
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(keyOut, keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("write CA key: %w", err)
	}
	if err := writeFileAtomic(certOut, ca.certPEM, 0o644); err != nil {
		_ = os.Remove(keyOut)
		return nil, fmt.Errorf("write CA certificate: %w", err)
	}
	return ca, nil
}

func generateCA() (*CA, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate CA key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: caCommonName, Organization: []string{"nydus-resolver"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("create CA certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, fmt.Errorf("parse CA certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal CA key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return &CA{
		cert:    cert,
		key:     key,
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
	}, keyPEM, nil
}

func loadCA(certFile, keyFile string) (*CA, error) {
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return nil, fmt.Errorf("read CA certificate: %w", err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("CA certificate %s is not PEM", certFile)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse CA certificate: %w", err)
	}
	if !cert.IsCA || cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		return nil, fmt.Errorf("certificate %s is not a CA signing certificate", certFile)
	}

	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("read CA key: %w", err)
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, fmt.Errorf("CA key %s is not PEM", keyFile)
	}
	key, err := parsePrivateKey(keyBlock, keyFile)
	if err != nil {
		return nil, err
	}
	if !publicKeysEqual(cert.PublicKey, key.Public()) {
		return nil, fmt.Errorf("CA certificate %s does not match key %s", certFile, keyFile)
	}
	return &CA{
		cert:    cert,
		key:     key,
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}),
	}, nil
}

func parsePrivateKey(block *pem.Block, path string) (crypto.Signer, error) {
	switch block.Type {
	case "EC PRIVATE KEY":
		key, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse CA key %s: %w", path, err)
		}
		return key, nil
	case "RSA PRIVATE KEY":
		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse CA key %s: %w", path, err)
		}
		return key, nil
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse CA key %s: %w", path, err)
		}
		signer, ok := key.(crypto.Signer)
		if !ok {
			return nil, fmt.Errorf("CA key %s is not a signing key", path)
		}
		return signer, nil
	default:
		return nil, fmt.Errorf("unsupported CA key type %q in %s", block.Type, path)
	}
}

func publicKeysEqual(a, b any) bool {
	aDER, err := x509.MarshalPKIXPublicKey(a)
	if err != nil {
		return false
	}
	bDER, err := x509.MarshalPKIXPublicKey(b)
	if err != nil {
		return false
	}
	return bytes.Equal(aDER, bDER)
}

func randomSerial() (*big.Int, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generate serial number: %w", err)
	}
	return serial, nil
}

func regularFileExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.IsDir() {
		return false, fmt.Errorf("%s is a directory, expected a file", path)
	}
	return true, nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return nil
}

type cachedLeaf struct {
	cert    *tls.Certificate
	expires time.Time
}

// leafIssuer mints and caches short-lived leaf certificates for registry
// hosts. All hosts share one leaf key; only the certificate differs.
type leafIssuer struct {
	ca    *CA
	key   *ecdsa.PrivateKey
	mu    sync.Mutex
	cache map[string]cachedLeaf
	now   func() time.Time
}

func newLeafIssuer(ca *CA) (*leafIssuer, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate leaf key: %w", err)
	}
	return &leafIssuer{ca: ca, key: key, cache: make(map[string]cachedLeaf), now: time.Now}, nil
}

func (l *leafIssuer) Certificate(host string) (*tls.Certificate, error) {
	host = NormalizeRegistryHost(host)
	if host == "" {
		return nil, errors.New("certificate host is empty")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if cached, ok := l.cache[host]; ok && now.Before(cached.expires) {
		return cached.cert, nil
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: host},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, l.ca.cert, &l.key.PublicKey, l.ca.key)
	if err != nil {
		return nil, fmt.Errorf("sign leaf certificate for %s: %w", host, err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse leaf certificate for %s: %w", host, err)
	}
	cert := &tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  l.key,
		Leaf:        leaf,
	}
	l.cache[host] = cachedLeaf{cert: cert, expires: template.NotAfter.Add(-time.Hour)}
	return cert, nil
}

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
	"sync"
	"time"
)

const caCommonName = "nydus-resolver local CA"

// CAOptions names the local CA key, the combined trust bundle written for
// clients, and the system roots that must remain trusted alongside the local
// CA.
type CAOptions struct {
	KeyPath      string
	BundlePath   string
	SystemCAPath string
}

// CA is a local certificate authority used to mint leaf certificates for
// intercepted registry hosts.
type CA struct {
	cert    *x509.Certificate
	key     crypto.Signer
	certPEM []byte
}

// CertPEM returns the local CA certificate in PEM form.
func (ca *CA) CertPEM() []byte {
	return append([]byte(nil), ca.certPEM...)
}

// LoadOrCreateCA loads the local CA key and finds its certificate in the
// existing bundle, or creates a new key and CA on first start. It then rewrites
// the bundle as local CA + current system roots so SSL_CERT_FILE keeps other
// TLS working. Partial or mismatched material fails closed.
func LoadOrCreateCA(opts CAOptions) (*CA, error) {
	if opts.KeyPath == "" || opts.BundlePath == "" || opts.SystemCAPath == "" {
		return nil, errors.New("CA key, bundle, and system CA paths are required")
	}
	keyExists, err := regularFileExists(opts.KeyPath)
	if err != nil {
		return nil, err
	}
	bundleExists, err := regularFileExists(opts.BundlePath)
	if err != nil {
		return nil, err
	}
	if keyExists != bundleExists {
		return nil, fmt.Errorf("incomplete CA material: %s exists=%t, %s exists=%t; remove both files to regenerate",
			opts.KeyPath, keyExists, opts.BundlePath, bundleExists)
	}

	var ca *CA
	createdKey := false
	cleanupKey := func() {
		if createdKey {
			_ = os.Remove(opts.KeyPath)
		}
	}
	switch {
	case keyExists:
		key, err := readPrivateKey(opts.KeyPath)
		if err != nil {
			return nil, err
		}
		cert, err := findLocalCA(opts.BundlePath, key.Public())
		if err != nil {
			return nil, err
		}
		ca = &CA{
			cert:    cert,
			key:     key,
			certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}),
		}
	default:
		generated, keyPEM, err := generateCA()
		if err != nil {
			return nil, err
		}
		if err := writeFileAtomic(opts.KeyPath, keyPEM, 0o600); err != nil {
			return nil, fmt.Errorf("write CA key: %w", err)
		}
		ca = generated
		createdKey = true
	}

	systemCerts, err := readPEMCertificates(opts.SystemCAPath)
	if err != nil {
		cleanupKey()
		return nil, fmt.Errorf("read system CA bundle: %w", err)
	}
	if len(systemCerts) == 0 {
		cleanupKey()
		return nil, fmt.Errorf("system CA bundle %s contains no certificates", opts.SystemCAPath)
	}
	bundle := encodeBundle(ca.cert, systemCerts)
	if err := writeFileAtomic(opts.BundlePath, bundle, 0o644); err != nil {
		cleanupKey()
		return nil, fmt.Errorf("write CA bundle: %w", err)
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

func findLocalCA(bundlePath string, publicKey crypto.PublicKey) (*x509.Certificate, error) {
	certs, err := readPEMCertificates(bundlePath)
	if err != nil {
		return nil, fmt.Errorf("read CA bundle: %w", err)
	}
	for _, cert := range certs {
		if !cert.IsCA || cert.KeyUsage&x509.KeyUsageCertSign == 0 {
			continue
		}
		if publicKeysEqual(cert.PublicKey, publicKey) {
			return cert, nil
		}
	}
	return nil, fmt.Errorf("CA bundle %s has no signing certificate matching the local key", bundlePath)
}

func readPEMCertificates(path string) ([]*x509.Certificate, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var certs []*x509.Certificate
	rest := raw
	for {
		block, remaining := pem.Decode(rest)
		if block == nil {
			break
		}
		rest = remaining
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse certificate in %s: %w", path, err)
		}
		certs = append(certs, cert)
	}
	return certs, nil
}

func encodeBundle(local *x509.Certificate, system []*x509.Certificate) []byte {
	seen := make(map[string]struct{}, len(system)+1)
	var bundle bytes.Buffer
	add := func(cert *x509.Certificate) {
		key := string(cert.Raw)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		_ = pem.Encode(&bundle, &pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	}
	add(local)
	for _, cert := range system {
		add(cert)
	}
	return bundle.Bytes()
}

func readPrivateKey(path string) (crypto.Signer, error) {
	keyPEM, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read CA key: %w", err)
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, fmt.Errorf("CA key %s is not PEM", path)
	}
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

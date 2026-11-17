package nydusresolver

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type recordedRequest struct {
	Method        string
	Path          string
	RawQuery      string
	Host          string
	Authorization string
	Range         string
	Accept        string
	Body          string
}

type requestRecorder struct {
	mu    sync.Mutex
	items []recordedRequest
}

func (r *requestRecorder) add(item recordedRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append(r.items, item)
}

func (r *requestRecorder) all() []recordedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedRequest(nil), r.items...)
}

func testSystemCA(t *testing.T) string {
	t.Helper()
	ca, _, err := generateCA()
	if err != nil {
		t.Fatalf("generate system CA: %v", err)
	}
	path := filepath.Join(t.TempDir(), "system-ca.pem")
	if err := os.WriteFile(path, ca.CertPEM(), 0o644); err != nil {
		t.Fatalf("write system CA: %v", err)
	}
	return path
}

func newUpstream(t *testing.T, recorder *requestRecorder) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read upstream request body: %v", err)
		}
		recorder.add(recordedRequest{
			Method:        r.Method,
			Path:          r.URL.Path,
			RawQuery:      r.URL.RawQuery,
			Host:          r.Host,
			Authorization: r.Header.Get("Authorization"),
			Range:         r.Header.Get("Range"),
			Accept:        r.Header.Get("Accept"),
			Body:          string(body),
		})
		switch {
		case strings.HasSuffix(r.URL.Path, "/redirect"):
			w.Header().Set("Location", "https://storage.example.test/blob")
			w.WriteHeader(http.StatusTemporaryRedirect)
		case r.Method == http.MethodHead:
			w.Header().Set("Content-Length", "1234")
			w.Header().Set("Docker-Content-Digest", "sha256:deadbeef")
			w.WriteHeader(http.StatusOK)
		case r.Header.Get("Range") != "":
			w.Header().Set("Content-Range", "bytes 0-15/100")
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("0123456789abcdef"))
		default:
			w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("manifest-body"))
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func newTestProxy(t *testing.T, upstreamURL string) (*httptest.Server, *x509.CertPool) {
	t.Helper()
	dir := t.TempDir()
	bundlePath := filepath.Join(dir, "ca-bundle.pem")
	server, err := New(Options{
		ListenAddr:   "127.0.0.1:0",
		UpstreamURL:  upstreamURL,
		CAKeyFile:    filepath.Join(dir, "ca.key"),
		CABundleFile: bundlePath,
		SystemCAFile: testSystemCA(t),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	proxy := httptest.NewServer(server.Handler())
	t.Cleanup(proxy.Close)

	bundlePEM, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatalf("read CA bundle: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(bundlePEM) {
		t.Fatalf("append CA bundle")
	}
	return proxy, pool
}

func proxyClient(t *testing.T, proxyURL string, pool *x509.CertPool) *http.Client {
	t.Helper()
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		t.Fatalf("parse proxy URL: %v", err)
	}
	return &http.Client{Transport: &http.Transport{
		Proxy: http.ProxyURL(parsed),
		TLSClientConfig: &tls.Config{
			RootCAs:    pool,
			MinVersion: tls.VersionTLS12,
		},
	}}
}

func TestProxyConnectAuthRangeHeadAndMethod(t *testing.T) {
	recorder := &requestRecorder{}
	upstream := newUpstream(t, recorder)
	proxy, pool := newTestProxy(t, upstream.URL)
	client := proxyClient(t, proxy.URL, pool)

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}

	request, err := http.NewRequest(http.MethodGet,
		"https://registry.example.test/v2/library/alpine/manifests/latest?ns=registry.example.test&x=1", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("Authorization", "Basic dGVzdDp0ZXN0")
	request.Header.Set("Accept", "application/vnd.oci.image.index.v1+json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("GET through proxy: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if response.StatusCode != http.StatusOK || string(body) != "manifest-body" {
		t.Fatalf("GET response = %d %q", response.StatusCode, body)
	}
	if response.TLS == nil || len(response.TLS.PeerCertificates) == 0 {
		t.Fatal("TLS peer certificate is missing")
	}
	leaf := response.TLS.PeerCertificates[0]
	if err := leaf.VerifyHostname("registry.example.test"); err != nil {
		t.Fatalf("leaf certificate SAN: %v", err)
	}
	if !containsString(leaf.DNSNames, "registry.example.test") {
		t.Fatalf("leaf DNSNames = %v", leaf.DNSNames)
	}

	// A second request on the same client exercises keep-alive on the tunneled
	// connection: the inner HTTP server must keep serving after the first
	// response.
	secondResponse, err := client.Get("https://registry.example.test/v2/library/busybox/manifests/latest")
	if err != nil {
		t.Fatalf("second GET through proxy: %v", err)
	}
	secondBody, err := io.ReadAll(secondResponse.Body)
	secondResponse.Body.Close()
	if err != nil {
		t.Fatalf("read second response: %v", err)
	}
	if secondResponse.StatusCode != http.StatusOK || string(secondBody) != "manifest-body" {
		t.Fatalf("second GET response = %d %q", secondResponse.StatusCode, secondBody)
	}

	rangeRequest, err := http.NewRequest(http.MethodGet,
		"https://index.example.test/v2/library/nginx/blobs/sha256:abc", nil)
	if err != nil {
		t.Fatalf("new range request: %v", err)
	}
	rangeRequest.Header.Set("Authorization", "Bearer token-value")
	rangeRequest.Header.Set("Range", "bytes=0-15")
	rangeResponse, err := client.Do(rangeRequest)
	if err != nil {
		t.Fatalf("range request through proxy: %v", err)
	}
	rangeBody, err := io.ReadAll(rangeResponse.Body)
	rangeResponse.Body.Close()
	if err != nil {
		t.Fatalf("read range response: %v", err)
	}
	if rangeResponse.StatusCode != http.StatusPartialContent || len(rangeBody) != 16 {
		t.Fatalf("range response = %d %q", rangeResponse.StatusCode, rangeBody)
	}
	if got := rangeResponse.Header.Get("Content-Range"); got != "bytes 0-15/100" {
		t.Fatalf("Content-Range = %q", got)
	}

	headResponse, err := client.Head("https://head.example.test/v2/example/manifests/main")
	if err != nil {
		t.Fatalf("HEAD through proxy: %v", err)
	}
	headResponse.Body.Close()
	if headResponse.StatusCode != http.StatusOK || headResponse.ContentLength != 1234 {
		t.Fatalf("HEAD response = %d, Content-Length %d", headResponse.StatusCode, headResponse.ContentLength)
	}
	if got := headResponse.Header.Get("Docker-Content-Digest"); got != "sha256:deadbeef" {
		t.Fatalf("HEAD Docker-Content-Digest = %q", got)
	}

	postRequest, err := http.NewRequest(http.MethodPost,
		"https://post.example.test/v2/foo/bar/blobs/uploads/", strings.NewReader("chunk"))
	if err != nil {
		t.Fatalf("new POST request: %v", err)
	}
	postResponse, err := client.Do(postRequest)
	if err != nil {
		t.Fatalf("POST through proxy: %v", err)
	}
	postResponse.Body.Close()
	if postResponse.StatusCode != http.StatusOK {
		t.Fatalf("POST response = %d", postResponse.StatusCode)
	}

	noRedirect := proxyClient(t, proxy.URL, pool)
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	redirectResponse, err := noRedirect.Get("https://redirect.example.test/v2/foo/redirect")
	if err != nil {
		t.Fatalf("redirect through proxy: %v", err)
	}
	redirectResponse.Body.Close()
	if redirectResponse.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("redirect status = %d, want 307", redirectResponse.StatusCode)
	}
	if got := redirectResponse.Header.Get("Location"); got != "https://storage.example.test/blob" {
		t.Fatalf("redirect Location = %q", got)
	}

	records := recorder.all()
	if len(records) != 6 {
		t.Fatalf("upstream saw %d requests, want 6", len(records))
	}
	assertRecord(t, records[0], recordedRequest{
		Method:        http.MethodGet,
		Path:          "/registry.example.test/v2/library/alpine/manifests/latest",
		RawQuery:      "ns=registry.example.test&x=1",
		Host:          upstreamURL.Host,
		Authorization: "Basic dGVzdDp0ZXN0",
		Accept:        "application/vnd.oci.image.index.v1+json",
	})
	assertRecord(t, records[1], recordedRequest{
		Method: http.MethodGet,
		Path:   "/registry.example.test/v2/library/busybox/manifests/latest",
		Host:   upstreamURL.Host,
	})
	assertRecord(t, records[2], recordedRequest{
		Method:        http.MethodGet,
		Path:          "/index.example.test/v2/library/nginx/blobs/sha256:abc",
		Host:          upstreamURL.Host,
		Authorization: "Bearer token-value",
		Range:         "bytes=0-15",
	})
	assertRecord(t, records[3], recordedRequest{
		Method: http.MethodHead,
		Path:   "/head.example.test/v2/example/manifests/main",
		Host:   upstreamURL.Host,
	})
	assertRecord(t, records[4], recordedRequest{
		Method: http.MethodPost,
		Path:   "/post.example.test/v2/foo/bar/blobs/uploads/",
		Host:   upstreamURL.Host,
		Body:   "chunk",
	})
	assertRecord(t, records[5], recordedRequest{
		Method: http.MethodGet,
		Path:   "/redirect.example.test/v2/foo/redirect",
		Host:   upstreamURL.Host,
	})
}

func assertRecord(t *testing.T, got, want recordedRequest) {
	t.Helper()
	if got.Method != want.Method || got.Path != want.Path || got.RawQuery != want.RawQuery ||
		got.Host != want.Host || got.Authorization != want.Authorization ||
		got.Range != want.Range || got.Accept != want.Accept || got.Body != want.Body {
		t.Fatalf("upstream request = %+v, want %+v", got, want)
	}
}

func TestProxyForwardsConnectHost(t *testing.T) {
	recorder := &requestRecorder{}
	upstream := newUpstream(t, recorder)
	proxy, pool := newTestProxy(t, upstream.URL)
	client := proxyClient(t, proxy.URL, pool)

	response, err := client.Get("https://arbitrary.example.test/v2/library/alpine/manifests/latest")
	if err != nil {
		t.Fatalf("GET through proxy: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET response = %d", response.StatusCode)
	}
	records := recorder.all()
	if len(records) != 1 {
		t.Fatalf("upstream saw %d requests, want 1", len(records))
	}
	if records[0].Path != "/arbitrary.example.test/v2/library/alpine/manifests/latest" {
		t.Fatalf("upstream path = %q", records[0].Path)
	}
}

func TestProxyInvalidHostFailsClosed(t *testing.T) {
	recorder := &requestRecorder{}
	upstream := newUpstream(t, recorder)
	proxy, _ := newTestProxy(t, upstream.URL)

	conn, err := net.Dial("tcp", strings.TrimPrefix(proxy.URL, "http://"))
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "CONNECT bad_host:443 HTTP/1.1\r\nHost: bad_host:443\r\n\r\n"); err != nil {
		t.Fatalf("write CONNECT: %v", err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("read CONNECT response: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("invalid host status = %d, want 403", response.StatusCode)
	}
	if len(recorder.all()) != 0 {
		t.Fatal("invalid host reached upstream")
	}
}

func TestHealthz(t *testing.T) {
	proxy, _ := newTestProxy(t, "http://127.0.0.1:1")
	response, err := http.Get(proxy.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatalf("read /healthz: %v", err)
	}
	if response.StatusCode != http.StatusOK || string(body) != "ok\n" {
		t.Fatalf("/healthz = %d %q", response.StatusCode, body)
	}

	response, err = http.Get(proxy.URL + "/v2/")
	if err != nil {
		t.Fatalf("GET /v2/: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("plain /v2/ status = %d, want 405", response.StatusCode)
	}
}

func TestLoadOrCreateCA(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ca.key")
	bundlePath := filepath.Join(dir, "ca-bundle.pem")
	systemPath := testSystemCA(t)

	ca, err := LoadOrCreateCA(CAOptions{KeyPath: keyPath, BundlePath: bundlePath, SystemCAPath: systemPath})
	if err != nil {
		t.Fatalf("LoadOrCreateCA: %v", err)
	}
	if len(ca.CertPEM()) == 0 {
		t.Fatal("CA certificate is empty")
	}

	keyInfo, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat generated CA key: %v", err)
	}
	if perm := keyInfo.Mode().Perm(); perm != 0o600 {
		t.Fatalf("CA key mode = %o, want 600", perm)
	}
	bundlePEM, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}
	bundleInfo, err := os.Stat(bundlePath)
	if err != nil {
		t.Fatalf("stat bundle: %v", err)
	}
	if perm := bundleInfo.Mode().Perm(); perm != 0o644 {
		t.Fatalf("bundle mode = %o, want 644", perm)
	}
	certs := parseTestBundle(t, bundlePEM)
	if len(certs) != 2 {
		t.Fatalf("bundle has %d certificates, want local + system", len(certs))
	}
	if !bytes.Equal(certs[0].Raw, ca.cert.Raw) {
		t.Fatal("bundle does not start with the local CA")
	}
	systemPEM, err := os.ReadFile(systemPath)
	if err != nil {
		t.Fatalf("read system CA: %v", err)
	}
	if !bytes.Equal(certs[1].Raw, parseTestBundle(t, systemPEM)[0].Raw) {
		t.Fatal("bundle does not preserve the system CA")
	}

	reloaded, err := LoadOrCreateCA(CAOptions{KeyPath: keyPath, BundlePath: bundlePath, SystemCAPath: systemPath})
	if err != nil {
		t.Fatalf("reload CA: %v", err)
	}
	if !bytes.Equal(ca.CertPEM(), reloaded.CertPEM()) {
		t.Fatal("CA certificate changed across restarts")
	}
	reloadedBundle, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatalf("read reloaded bundle: %v", err)
	}
	if len(parseTestBundle(t, reloadedBundle)) != 2 {
		t.Fatal("reloaded bundle was duplicated")
	}

	if err := os.Remove(keyPath); err != nil {
		t.Fatalf("remove key: %v", err)
	}
	if _, err := LoadOrCreateCA(CAOptions{KeyPath: keyPath, BundlePath: bundlePath, SystemCAPath: systemPath}); err == nil {
		t.Fatal("incomplete CA material was silently regenerated")
	}
}

func TestLoadOrCreateCARequiresSystemRoots(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ca.key")
	bundlePath := filepath.Join(dir, "ca-bundle.pem")
	if _, err := LoadOrCreateCA(CAOptions{
		KeyPath: keyPath, BundlePath: bundlePath, SystemCAPath: filepath.Join(dir, "missing.pem"),
	}); err == nil {
		t.Fatal("missing system CA bundle did not fail")
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatal("failed first start left a partial CA key behind")
	}
}

func TestLoadOrCreateCARejectsMismatchedBundle(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ca.key")
	bundlePath := filepath.Join(dir, "ca-bundle.pem")
	systemPath := testSystemCA(t)
	if _, err := LoadOrCreateCA(CAOptions{KeyPath: keyPath, BundlePath: bundlePath, SystemCAPath: systemPath}); err != nil {
		t.Fatalf("initial CA: %v", err)
	}
	systemPEM, err := os.ReadFile(systemPath)
	if err != nil {
		t.Fatalf("read system CA: %v", err)
	}
	if err := os.WriteFile(bundlePath, systemPEM, 0o644); err != nil {
		t.Fatalf("overwrite bundle: %v", err)
	}
	if _, err := LoadOrCreateCA(CAOptions{KeyPath: keyPath, BundlePath: bundlePath, SystemCAPath: systemPath}); err == nil {
		t.Fatal("bundle without a certificate for the local key did not fail")
	}
}

func parseTestBundle(t *testing.T, raw []byte) []*x509.Certificate {
	t.Helper()
	var certs []*x509.Certificate
	for {
		block, rest := pem.Decode(raw)
		if block == nil {
			break
		}
		raw = rest
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatalf("parse bundle certificate: %v", err)
		}
		certs = append(certs, cert)
	}
	return certs
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

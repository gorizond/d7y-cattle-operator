// Package nydusresolver implements the HTTPS MITM forward proxy used by
// nydus-snapshotter. It terminates TLS with a locally trusted CA, rewrites
// /v2/... requests to the registry proxy path /<registry>/v2/..., and streams
// requests and responses without buffering.
package nydusresolver

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	connectPort        = "443"
	readHeaderTimeout  = 10 * time.Second
	handshakeTimeout   = 10 * time.Second
	tunnelIdleTimeout  = 2 * time.Minute
	responseHeaderWait = 60 * time.Second
	maxHeaderBytes     = 1 << 20
)

// Options configures the resolver. UpstreamURL must be an absolute http(s)
// URL; the command enforces https.
type Options struct {
	ListenAddr  string
	UpstreamURL string
	RegistryMap map[string]string
	CACertFile  string
	CAKeyFile   string
	CACertOut   string
}

// Server is an HTTPS forward proxy with a per-host MITM certificate.
type Server struct {
	listen   string
	upstream *url.URL
	registry map[string]string
	leaves   *leafIssuer
	client   *http.Client
	log      *slog.Logger
	tunnels  sync.Map // net.Conn -> struct{}
}

// New validates options, loads or creates the local CA, and builds the proxy.
func New(opts Options) (*Server, error) {
	if opts.ListenAddr == "" {
		return nil, errors.New("listen address is required")
	}
	upstream, err := url.Parse(opts.UpstreamURL)
	if err != nil || upstream.Host == "" || (upstream.Scheme != "http" && upstream.Scheme != "https") {
		return nil, errors.New("upstream must be an absolute http(s) URL")
	}
	if upstream.RawQuery != "" || upstream.Fragment != "" {
		return nil, errors.New("upstream URL must not contain a query or fragment")
	}
	registry, err := normalizeRegistryMap(opts.RegistryMap)
	if err != nil {
		return nil, err
	}
	if len(registry) == 0 {
		return nil, errors.New("registry map must not be empty")
	}
	ca, err := LoadOrCreateCA(opts.CACertFile, opts.CAKeyFile, opts.CACertOut)
	if err != nil {
		return nil, err
	}
	leaves, err := newLeafIssuer(ca)
	if err != nil {
		return nil, err
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableCompression = true
	transport.TLSHandshakeTimeout = handshakeTimeout
	transport.ResponseHeaderTimeout = responseHeaderWait
	transport.IdleConnTimeout = 90 * time.Second
	transport.ExpectContinueTimeout = time.Second

	return &Server{
		listen:   opts.ListenAddr,
		upstream: upstream,
		registry: registry,
		leaves:   leaves,
		client: &http.Client{
			Transport: transport,
			// A proxy relays redirects; following them would bypass the
			// registry mapping and fail-closed host set.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		log: slog.Default(),
	}, nil
}

// Handler returns the plain HTTP handler: CONNECT is proxied and /healthz is
// served locally.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.serve)
}

// Serve listens on the configured address until ctx is canceled.
func (s *Server) Serve(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.listen)
	if err != nil {
		return err
	}
	return s.ServeListener(ctx, listener)
}

// ServeListener serves on an existing listener until ctx is canceled.
func (s *Server) ServeListener(ctx context.Context, listener net.Listener) error {
	server := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       tunnelIdleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}
	serveCtx, cancelServe := context.WithCancel(ctx)
	defer cancelServe()
	go func() {
		<-serveCtx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		s.closeTunnels()
	}()
	defer s.closeTunnels()

	err := server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		s.handleConnect(w, r)
		return
	}
	if r.URL.Path == "/healthz" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte("ok\n"))
		}
		return
	}
	w.Header().Set("Allow", http.MethodConnect)
	http.Error(w, "CONNECT required", http.StatusMethodNotAllowed)
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	target := r.Host
	if target == "" {
		target = r.URL.Host
	}
	host, port, err := splitConnectTarget(target)
	if err != nil {
		http.Error(w, "invalid CONNECT target", http.StatusForbidden)
		return
	}
	if port != connectPort {
		http.Error(w, "only CONNECT host:443 is allowed", http.StatusForbidden)
		return
	}
	host = NormalizeRegistryHost(host)
	if _, ok := s.registry[host]; !ok {
		http.Error(w, "registry is not mapped", http.StatusForbidden)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking is not supported", http.StatusInternalServerError)
		return
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return
	}
	if _, err := rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		_ = conn.Close()
		return
	}
	if err := rw.Flush(); err != nil {
		_ = conn.Close()
		return
	}

	tlsConn := tls.Server(&bufferedConn{Conn: conn, reader: rw.Reader}, s.tlsConfig(host))
	handshakeCtx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
	err = tlsConn.HandshakeContext(handshakeCtx)
	cancel()
	if err != nil {
		s.log.Debug("MITM handshake failed", "host", host, "error", err)
		_ = conn.Close()
		return
	}

	tracked := newCloseNotifyConn(tlsConn)
	s.tunnels.Store(tracked, struct{}{})
	defer func() {
		s.tunnels.Delete(tracked)
		_ = tracked.Close()
		_ = conn.Close()
	}()

	inner := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.serveTunnel(w, r, host)
		}),
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       tunnelIdleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}
	listener := &singleConnListener{conn: tracked, done: tracked.done}
	_ = inner.Serve(listener)
}

func (s *Server) tlsConfig(connectHost string) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		// The registry client uses HTTP/1.1; advertising h2 without an h2
		// server would break keep-alive after ALPN negotiation.
		NextProtos: []string{"http/1.1"},
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			name := hello.ServerName
			if name == "" {
				name = connectHost
			}
			name = NormalizeRegistryHost(name)
			if _, ok := s.registry[name]; !ok {
				return nil, errors.New("registry is not mapped")
			}
			return s.leaves.Certificate(name)
		},
	}
}

func (s *Server) serveTunnel(w http.ResponseWriter, r *http.Request, connectHost string) {
	host := connectHost
	if r.Host != "" {
		host = NormalizeRegistryHost(r.Host)
	}
	if host != connectHost {
		http.Error(w, "host does not match CONNECT target", http.StatusForbidden)
		return
	}
	mapped, ok := s.registry[host]
	if !ok {
		http.Error(w, "registry is not mapped", http.StatusForbidden)
		return
	}
	targetPath, ok := RewriteRegistryPath(r.URL.Path, mapped)
	if !ok {
		http.NotFound(w, r)
		return
	}

	target := *s.upstream
	target.Path = joinURLPath(s.upstream.Path, targetPath)
	target.RawPath = ""
	if r.URL.RawPath != "" && r.URL.RawPath != r.URL.Path {
		if rawTarget, ok := RewriteRegistryPath(r.URL.RawPath, mapped); ok {
			target.RawPath = joinURLPath(s.upstream.EscapedPath(), rawTarget)
		}
	}
	target.RawQuery = r.URL.RawQuery

	out := r.Clone(r.Context())
	out.URL = &target
	out.RequestURI = ""
	out.Host = s.upstream.Host
	out.Close = false
	removeHopByHopHeaders(out.Header)

	response, err := s.client.Do(out)
	if err != nil {
		if r.Context().Err() == nil {
			s.log.Warn("upstream request failed", "host", host, "path", r.URL.Path, "error", err)
		}
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()

	removeHopByHopHeaders(response.Header)
	for name, values := range response.Header {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.Copy(w, response.Body); err != nil {
		s.log.Debug("stream upstream response failed", "host", host, "error", err)
	}
}

func (s *Server) closeTunnels() {
	s.tunnels.Range(func(key, _ any) bool {
		s.tunnels.Delete(key)
		if conn, ok := key.(net.Conn); ok {
			_ = conn.Close()
		}
		return true
	})
}

func splitConnectTarget(target string) (string, string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", "", errors.New("empty target")
	}
	if host, port, err := net.SplitHostPort(target); err == nil {
		if host == "" {
			return "", "", errors.New("empty host")
		}
		if port == "" {
			port = connectPort
		}
		return host, port, nil
	}
	if strings.Contains(target, ":") {
		return "", "", errors.New("target must be host or host:port")
	}
	return target, connectPort, nil
}

func joinURLPath(basePath, requestPath string) string {
	if basePath == "" || basePath == "/" {
		if requestPath == "" {
			return "/"
		}
		if strings.HasPrefix(requestPath, "/") {
			return requestPath
		}
		return "/" + requestPath
	}
	switch {
	case strings.HasSuffix(basePath, "/") && strings.HasPrefix(requestPath, "/"):
		return basePath + requestPath[1:]
	case !strings.HasSuffix(basePath, "/") && !strings.HasPrefix(requestPath, "/"):
		return basePath + "/" + requestPath
	default:
		return basePath + requestPath
	}
}

func removeHopByHopHeaders(header http.Header) {
	for _, value := range header.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			if name = strings.TrimSpace(name); name != "" {
				header.Del(name)
			}
		}
	}
	for _, name := range []string{
		"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
		"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
	} {
		header.Del(name)
	}
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

type closeNotifyConn struct {
	net.Conn
	once sync.Once
	done chan struct{}
}

func newCloseNotifyConn(conn net.Conn) *closeNotifyConn {
	return &closeNotifyConn{Conn: conn, done: make(chan struct{})}
}

func (c *closeNotifyConn) Close() error {
	c.once.Do(func() { close(c.done) })
	return c.Conn.Close()
}

// singleConnListener hands one connection to http.Server and then blocks until
// that connection is closed. Without the block, Serve would return immediately
// after accepting, and the enclosing CONNECT handler would close the live
// connection before the first request is served.
type singleConnListener struct {
	conn   net.Conn
	done   <-chan struct{}
	served bool
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	if !l.served {
		l.served = true
		return l.conn, nil
	}
	<-l.done
	return nil, io.EOF
}

func (l *singleConnListener) Close() error {
	return l.conn.Close()
}

func (l *singleConnListener) Addr() net.Addr {
	return l.conn.LocalAddr()
}

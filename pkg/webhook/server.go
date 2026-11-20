package webhook

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// Serve runs the HTTPS admission server until ctx is canceled or the server
// fails. The certificate loader reloads the needacert-mounted Secret on every
// handshake and falls back to an in-memory self-signed certificate while the
// Secret is unavailable.
func Serve(ctx context.Context, addr string, handler http.Handler, loader *CertLoader) error {
	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{
			MinVersion:     tls.VersionTLS12,
			GetCertificate: loader.GetCertificate,
		},
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServeTLS("", "")
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Warn("webhook server shutdown failed", "error", err)
		}
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("webhook server: %w", err)
	}
}

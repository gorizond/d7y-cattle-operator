// Command admission-webhook serves the opt-in Pod image-rewriting webhook.
// It runs in the downstream cluster, deployed by charts/admission-webhook and
// enabled only by p2p.dragonfly.io/admission.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/rancher/wrangler/v3/pkg/signals"

	"github.com/gorizond/d7y-cattle-operator/pkg/webhook"
	"github.com/gorizond/d7y-cattle-operator/pkg/webhook/admission"
)

func main() {
	if err := run(); err != nil {
		slog.Error("admission webhook failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	namespace := envOr("POD_NAMESPACE", "dragonfly-system")
	defaultNetworkOnly, err := strconv.ParseBool(envOr("ADMISSION_NETWORK_ONLY", "true"))
	if err != nil {
		return fmt.Errorf("ADMISSION_NETWORK_ONLY: %w", err)
	}
	var (
		proxyDomain    = flag.String("proxy-domain", envOr("PROXY_DOMAIN", ""), "public registry proxy host (DNS name, no scheme)")
		pullSecret     = flag.String("image-pull-secret", envOr("ADMISSION_IMAGE_PULL_SECRET", ""), "Secret injected as imagePullSecrets (empty: network-only)")
		serviceAccount = flag.String("service-account", envOr("ADMISSION_SERVICE_ACCOUNT", ""), "serviceAccountName injected for proxy pulls")
		networkOnly    = flag.Bool("network-only", defaultNetworkOnly, "disable credential injection; rely on network-only proxy access")
		port           = flag.Int("port", envIntOr("ADMISSION_PORT", 8443), "HTTPS listen port")
		certDir        = flag.String("cert-dir", envOr("ADMISSION_CERT_DIR", "/etc/admission-webhook/tls"), "directory containing tls.crt/tls.key")
		serviceName    = flag.String("service-name", envOr("ADMISSION_SERVICE_NAME", "admission-webhook"), "Service name used in the fallback certificate SANs")
	)
	flag.Parse()

	if strings.TrimSpace(*proxyDomain) == "" {
		return fmt.Errorf("--proxy-domain is required")
	}

	rewriter := admission.New(admission.Config{
		ProxyDomain:        *proxyDomain,
		ImagePullSecret:    *pullSecret,
		ServiceAccountName: *serviceAccount,
		NetworkOnly:        *networkOnly,
	})
	loader, err := webhook.NewCertLoader(*certDir, *serviceName, namespace)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.Handle("/mutate", rewriter)
	mux.HandleFunc("/healthz", ok)
	mux.HandleFunc("/readyz", ok)

	ctx := signals.SetupSignalContext()
	slog.Info("admission webhook listening", "port", *port, "proxyDomain", *proxyDomain)
	return webhook.Serve(ctx, fmt.Sprintf(":%d", *port), mux, loader)
}

func ok(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func envOr(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}
	return fallback
}

func envIntOr(key string, fallback int) int {
	value, err := strconv.Atoi(envOr(key, ""))
	if err != nil {
		return fallback
	}
	return value
}

// Command nydus-resolver is an HTTPS MITM forward proxy for nydus-snapshotter.
// It rewrites registry requests to the registry proxy path and trusts a local
// CA so the snapshotter can pull through HTTPS_PROXY without changing registry
// hostnames.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"syscall"

	"github.com/gorizond/d7y-cattle-operator/pkg/nydusresolver"
)

const (
	defaultListen    = "127.0.0.1:3999"
	defaultUpstream  = "https://proxy.koldun.negash.ru"
	defaultCACertOut = "/var/lib/nydus-resolver/ca.crt"
)

func main() {
	if err := run(); err != nil {
		slog.Error("nydus-resolver failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	listen := flag.String("listen", defaultListen, "listen address for CONNECT and /healthz")
	upstream := flag.String("upstream", defaultUpstream, "upstream registry proxy base URL (https)")
	caCert := flag.String("ca-cert", "", "existing CA certificate PEM (requires --ca-key)")
	caKey := flag.String("ca-key", "", "existing CA private key PEM (requires --ca-cert)")
	caCertOut := flag.String("ca-cert-out", defaultCACertOut, "CA certificate path; generated with a sibling key when no CA flags are set")
	registryMap := flag.String("registry-map", "", "extra registry mappings as host=path,... entries; unknown hosts are rejected")
	flag.Parse()

	upstreamURL, err := url.Parse(*upstream)
	if err != nil || upstreamURL.Host == "" || upstreamURL.Scheme != "https" {
		return fmt.Errorf("--upstream must be an absolute https URL")
	}
	if (*caCert == "") != (*caKey == "") {
		return fmt.Errorf("--ca-cert and --ca-key must be set together")
	}

	overrides, err := nydusresolver.ParseRegistryMap(*registryMap)
	if err != nil {
		return err
	}
	registry := nydusresolver.MergeRegistryMaps(nydusresolver.DefaultRegistryMap(), overrides)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	server, err := nydusresolver.New(nydusresolver.Options{
		ListenAddr:  *listen,
		UpstreamURL: upstreamURL.String(),
		RegistryMap: registry,
		CACertFile:  *caCert,
		CAKeyFile:   *caKey,
		CACertOut:   *caCertOut,
	})
	if err != nil {
		return err
	}

	slog.Info("nydus-resolver listening",
		"address", *listen,
		"upstream", upstreamURL.Host,
		"registries", len(registry),
	)
	return server.Serve(ctx)
}

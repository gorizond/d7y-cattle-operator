// Command nydus-resolver is an HTTPS MITM forward proxy for nydus-snapshotter.
// It is installed on the host by p2p-install.sh and reads its listen address,
// upstream registry proxy, and CA paths from config.yaml.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"syscall"

	"github.com/gorizond/d7y-cattle-operator/pkg/nydusresolver"
)

func main() {
	if err := run(); err != nil {
		slog.Error("nydus-resolver failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "", "path to the resolver config YAML (required)")
	flag.Parse()
	if *configPath == "" {
		return errors.New("--config is required")
	}

	cfg, err := nydusresolver.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	upstream, err := url.Parse(cfg.Upstream)
	if err != nil {
		return fmt.Errorf("parse upstream: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	server, err := nydusresolver.New(nydusresolver.Options{
		ListenAddr:   cfg.Listen,
		UpstreamURL:  cfg.Upstream,
		CAKeyFile:    cfg.CAKey,
		CABundleFile: cfg.CABundle,
		SystemCAFile: cfg.SystemCAFile,
	})
	if err != nil {
		return err
	}

	slog.Info("nydus-resolver listening",
		"address", cfg.Listen,
		"upstream", upstream.Host,
	)
	return server.Serve(ctx)
}

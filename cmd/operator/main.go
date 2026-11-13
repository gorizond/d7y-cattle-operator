package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/rancher/wrangler/v3/pkg/generated/controllers/core"
	"github.com/rancher/wrangler/v3/pkg/kubeconfig"
	"github.com/rancher/wrangler/v3/pkg/signals"
	"k8s.io/client-go/rest"

	"github.com/gorizond/d7y-cattle-operator/pkg/config"
	"github.com/gorizond/d7y-cattle-operator/pkg/pki"
)

func main() {
	if err := run(); err != nil {
		slog.Error("operator failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	restConfig, err := kubeRESTConfig(cfg.Kubeconfig)
	if err != nil {
		return fmt.Errorf("kubernetes client config: %w", err)
	}
	coreFactory, err := core.NewFactoryFromConfig(restConfig)
	if err != nil {
		return fmt.Errorf("wrangler core factory: %w", err)
	}
	secretController := coreFactory.Core().V1().Secret()

	ctx := signals.SetupSignalContext()
	if err := coreFactory.Start(ctx, 1); err != nil {
		return fmt.Errorf("start wrangler controllers: %w", err)
	}

	bundle, err := pki.Generate(pki.Options{
		ManagerGRPCDomain: cfg.ManagerGRPCDomain,
		ManagerAPIURL:     cfg.ManagerAPIURL,
	})
	if err != nil {
		return fmt.Errorf("generate mTLS material: %w", err)
	}

	created, err := pki.EnsureSecret(secretController, cfg.Namespace, cfg.MTLSSecretName, bundle)
	if err != nil {
		return fmt.Errorf("ensure mTLS secret: %w", err)
	}
	slog.Info("mTLS secret ready",
		"namespace", cfg.Namespace,
		"name", cfg.MTLSSecretName,
		"created", created,
	)
	slog.Info("operator scaffold running; controllers are not implemented yet")

	<-ctx.Done()
	slog.Info("shutting down")
	return nil
}

func kubeRESTConfig(kubeconfigPath string) (*rest.Config, error) {
	if kubeconfigPath == "" {
		return rest.InClusterConfig()
	}
	return kubeconfig.GetNonInteractiveClientConfig(kubeconfigPath).ClientConfig()
}

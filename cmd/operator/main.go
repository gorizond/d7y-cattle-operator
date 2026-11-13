package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/rancher/wrangler/v3/pkg/signals"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

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
	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("kubernetes client: %w", err)
	}

	bundle, err := pki.Generate(pki.Options{
		ManagerGRPCDomain: cfg.ManagerGRPCDomain,
		ManagerAPIURL:     cfg.ManagerAPIURL,
	})
	if err != nil {
		return fmt.Errorf("generate mTLS material: %w", err)
	}

	ctx := signals.SetupSignalContext()
	created, err := pki.EnsureSecret(ctx, client, cfg.Namespace, cfg.MTLSSecretName, bundle)
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

func kubeRESTConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig == "" {
		return rest.InClusterConfig()
	}
	return clientcmd.BuildConfigFromFlags("", kubeconfig)
}

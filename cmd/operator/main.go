package main

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"

	lassoclient "github.com/rancher/lasso/pkg/client"
	provisioning "github.com/rancher/rancher/pkg/generated/controllers/provisioning.cattle.io"
	"github.com/rancher/wrangler/v3/pkg/generated/controllers/core"
	"github.com/rancher/wrangler/v3/pkg/kubeconfig"
	"github.com/rancher/wrangler/v3/pkg/signals"
	"github.com/rancher/wrangler/v3/pkg/start"

	"github.com/gorizond/d7y-cattle-operator/pkg/cluster"
	"github.com/gorizond/d7y-cattle-operator/pkg/config"
	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
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

	restConfig, err := kubeconfig.GetNonInteractiveClientConfig(cfg.Kubeconfig).ClientConfig()
	if err != nil {
		return fmt.Errorf("kubernetes client config: %w", err)
	}

	coreFactory, err := core.NewFactoryFromConfig(restConfig)
	if err != nil {
		return fmt.Errorf("wrangler core factory: %w", err)
	}
	provisioningFactory, err := provisioning.NewFactoryFromConfig(restConfig)
	if err != nil {
		return fmt.Errorf("wrangler provisioning factory: %w", err)
	}

	clientFactory, err := lassoclient.NewSharedClientFactoryForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("resource client factory: %w", err)
	}
	helmOps, err := clientFactory.ForResource(helmop.HelmOpGVR, true)
	if err != nil {
		return fmt.Errorf("HelmOp client: %w", err)
	}

	apiHost, err := hostFromURL(cfg.ManagerAPIURL)
	if err != nil {
		return fmt.Errorf("manager API URL: %w", err)
	}

	// Discovery is allowed to every authenticated user, so checking the VPA
	// API needs no autoscaling RBAC.
	vpaEnabled := false
	if _, err := clientFactory.GVKForResource(helmop.VPAGVR); err == nil {
		vpaEnabled = true
	}

	secretController := coreFactory.Core().V1().Secret()
	eventController := coreFactory.Core().V1().Event()
	clusterController := provisioningFactory.Provisioning().V1().Cluster()

	ctx := signals.SetupSignalContext()
	cluster.Register(ctx, clusterController, secretController, eventController, helmOps, cluster.Options{
		OperatorNamespace: cfg.Namespace,
		MTLSSecretName:    cfg.MTLSSecretName,
		ManagerGRPCDomain: cfg.ManagerGRPCDomain,
		ManagerAPIHost:    apiHost,
		DragonflyChart: helmop.Chart{
			Repo:    cfg.DragonflyChartRepo,
			Name:    cfg.DragonflyChartName,
			Version: cfg.DragonflyChartVersion,
		},
		ExtrasChart: helmop.Chart{
			Repo:    cfg.ExtrasChartRepo,
			Name:    cfg.ExtrasChartName,
			Version: cfg.ExtrasChartVersion,
		},
		VPAEnabled: vpaEnabled,
	})

	if err := start.All(ctx, 1, coreFactory, provisioningFactory); err != nil {
		return fmt.Errorf("start controllers: %w", err)
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
	slog.Info("cluster controller running")

	<-ctx.Done()
	slog.Info("shutting down")
	return nil
}

func hostFromURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if parsed.Hostname() == "" {
		return "", fmt.Errorf("no host in %q", raw)
	}
	return parsed.Hostname(), nil
}

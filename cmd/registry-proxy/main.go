// Command registry-proxy serves the smart OCI registry proxy.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gorizond/d7y-cattle-operator/pkg/registryproxy"
)

const (
	configPath       = "/etc/registry-proxy/config.yaml"
	adminPasswordKey = "HARBOR_ADMIN_PASSWORD"
	adminSecretDir   = "/etc/registry-proxy/secrets/admin"
	webhookSecretDir = "/etc/registry-proxy/secrets/webhook"
)

func main() {
	if err := run(); err != nil {
		slog.Error("registry-proxy failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	configFile := flag.String("config", configPath, "proxy config file")
	listen := flag.String("listen", "", "listen address override")
	flag.Parse()

	cfg, err := registryproxy.Load(*configFile)
	if err != nil {
		return err
	}
	if *listen != "" {
		cfg.ListenAddr = *listen
	}
	adminPassword, err := readSecret(adminSecretDir, adminPasswordKey)
	if err != nil {
		return err
	}
	adminUsername := readOptional(adminSecretDir, "username")
	if adminUsername == "" {
		adminUsername = "admin"
	}
	webhookSecret := readOptional(webhookSecretDir, "secret")

	base := cfg.RegistryURL()
	harbor := &registryproxy.HarborClient{BaseURL: base, Username: adminUsername, Password: adminPassword}
	namespace := os.Getenv("POD_NAMESPACE")
	if namespace == "" {
		namespace = "dragonfly-system"
	}
	secretStore, err := registryproxy.NewKubeSecrets(namespace)
	if err != nil {
		return err
	}
	provisioner := &registryproxy.Provisioner{
		Harbor:         harbor,
		MirrorProject:  cfg.Mirror.Project,
		WebhookAddress: strings.TrimRight(cfg.Proxy.PublicURL, "/") + "/api/convert",
		WebhookSecret:  webhookSecret,
	}
	startupCtx, cancelStartup := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancelStartup()
	sourceProjects := make([]string, 0, len(cfg.Registry.Allowlist))
	for _, registry := range cfg.Registry.Allowlist {
		sourceProjects = append(sourceProjects, cfg.ProjectName(registry))
		if err := provisioner.Ensure(startupCtx, registry); err != nil {
			return fmt.Errorf("provision Harbor for %s: %w", registry, err)
		}
	}
	robot, err := registryproxy.EnsureRobotSecret(startupCtx, harbor, cfg.Harbor.RobotSecret, cfg.Mirror.Project, sourceProjects, secretStore)
	if err != nil {
		return err
	}
	source := &registryproxy.HTTPRegistry{BaseURL: base, Username: robot.Name, Password: robot.Secret}
	mirror := source

	copier := &registryproxy.Copier{
		Source:        source,
		Mirror:        mirror,
		MirrorProject: cfg.Mirror.Project,
	}
	timeout, _ := time.ParseDuration(cfg.Conversion.Timeout)
	converter := &registryproxy.Converter{
		HarborURL:     base,
		MirrorProject: cfg.Mirror.Project,
		NydusifyPath:  cfg.Conversion.Nydusify,
		NydusImage:    cfg.Conversion.NydusImage,
		Platform:      cfg.Conversion.Platform,
		WorkDir:       cfg.Conversion.WorkDir,
		Timeout:       timeout,
		SourceUser:    robot.Name,
		SourcePass:    robot.Secret,
		Registry:      mirror,
	}
	manager := registryproxy.NewManager(func(ctx context.Context, job registryproxy.Job) error {
		sourceDigest, err := copier.Copy(ctx, job.Registry, job.Repository, job.TagOrReference())
		if err != nil {
			return err
		}
		_, err = converter.Convert(ctx, job.Registry, job.Repository, job.TagOrReference(), sourceDigest)
		return err
	})

	backend := &registryproxy.HarborBackend{
		Source:        source,
		Mirror:        mirror,
		MirrorProject: cfg.Mirror.Project,
		Provisioner:   provisioner,
	}
	webhook := &registryproxy.Webhook{Secret: webhookSecret, Manager: manager, Config: cfg}
	server := &registryproxy.Server{Config: cfg, Backend: backend, Convert: webhook, Manager: manager}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	manager.Start(ctx)

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	slog.Info("registry-proxy listening", "address", cfg.ListenAddr)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func readSecret(dir, key string) (string, error) {
	raw, err := os.ReadFile(dir + "/" + key)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return "", errors.New("secret " + dir + "/" + key + " is empty")
	}
	return value, nil
}

func readOptional(dir, key string) string {
	raw, err := os.ReadFile(dir + "/" + key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

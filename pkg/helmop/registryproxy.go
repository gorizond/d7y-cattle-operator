package helmop

import (
	"fmt"
	"net/url"
	"strings"

	fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"
)

// RegistryProxy returns the per-cluster registry proxy HelmOp. It is retained
// for back-compat and tests; the controller reconciles the shared proxy.
func RegistryProxy(c Cluster) *fleetv1alpha1.HelmOp {
	harborURL := c.HarborURL
	if harborURL == "" && c.HarborDomain != "" {
		harborURL = "https://" + c.HarborDomain
	}
	values := registryProxyValues(c, registryProxyOptions{
		tlsSecret:         c.ProxyTLSSecret,
		harborAdminSecret: c.HarborAdminSecret,
		robotSecret:       c.ProxyRobotSecretName,
		webhookSecret:     c.ProxyWebhookSecretName,
		harborURL:         harborURL,
		webhookAddress:    registryProxyWebhookAddress(c),
	})
	// Harbor is an independent, replaceable component: the proxy HelmOp has
	// no dependsOn on it. Use harbor-url/proxy.domain for external endpoints.
	op := newHelmOp(RegistryProxyName(c.Name), c, c.RegistryProxyChart, "registry-proxy", values, nil)
	op.Spec.DownstreamResources = registryProxyDownstreamResources(
		c.HarborAdminSecret, c.ProxyWebhookSecretName, c.ProxyRobotSecretName)
	return op
}

// SharedRegistryProxy returns the shared registry-proxy HelmOp. The fullname is
// a Fleet template so the chart still renders <cluster>-registry-proxy on every
// target; the robot and webhook Secrets are fleet-wide fixed names.
func SharedRegistryProxy(c Cluster) *fleetv1alpha1.HelmOp {
	c = sharedCluster(c)
	harborURL := strings.TrimSpace(c.ProxyHarborURL)
	if harborURL == "" {
		harborURL = c.HarborURL
	}
	values := registryProxyValues(c, registryProxyOptions{
		fullnameOverride:  SharedClusterNameTemplate,
		tlsSecret:         SharedProxyTLSSecretTemplate,
		harborAdminSecret: SharedHarborAdminSecretName(),
		robotSecret:       SharedRegistryProxyRobotSecretName(),
		webhookSecret:     SharedRegistryProxyWebhookSecretName(),
		harborURL:         harborURL,
		webhookAddress:    SharedProxyWebhookAddressTemplate,
	})
	op := newSharedHelmOp(SharedRegistryProxyName(), TargetNamespace, c, c.RegistryProxyChart,
		"registry-proxy", values, nil, ProxySelector())
	op.Spec.DownstreamResources = registryProxyDownstreamResources(
		SharedHarborAdminSecretName(), SharedRegistryProxyWebhookSecretName(), SharedRegistryProxyRobotSecretName())
	return op
}

func registryProxyDownstreamResources(harborAdmin, webhook, robot string) []fleetv1alpha1.DownstreamResource {
	return []fleetv1alpha1.DownstreamResource{
		{Kind: "Secret", Name: harborAdmin},
		{Kind: "Secret", Name: webhook},
		{Kind: "Secret", Name: robot},
	}
}

// registryProxyWebhookAddress is the Harbor PUSH_ARTIFACT target used when the
// same Cluster owns Harbor: the proxy Service is reachable over the cluster
// network. An external Harbor keeps the chart default.
func registryProxyWebhookAddress(c Cluster) string {
	if !c.HarborManaged {
		return ""
	}
	return fmt.Sprintf("http://%s.%s.svc:%d/api/convert",
		RegistryProxyName(c.Name), TargetNamespace, RegistryProxyPort)
}

type registryProxyOptions struct {
	fullnameOverride  string
	tlsSecret         string
	harborAdminSecret string
	robotSecret       string
	webhookSecret     string
	harborURL         string
	webhookAddress    string
}

func registryProxyValues(c Cluster, opts registryProxyOptions) map[string]any {
	ingressHost := ""
	if parsed, err := url.Parse(c.ProxyPublicURL); err == nil {
		ingressHost = parsed.Hostname()
	}
	ingressPath := c.ProxyBasePath
	if ingressPath == "" {
		ingressPath = "/"
	}
	values := map[string]any{
		"command": []any{"/registry-proxy"},
		"ingress": map[string]any{
			"enabled":   ingressHost != "",
			"className": "traefik",
			"host":      ingressHost,
			"path":      ingressPath,
			"tlsSecret": opts.tlsSecret,
		},
		"harbor": map[string]any{
			"url":         opts.harborURL,
			"adminSecret": opts.harborAdminSecret,
			"robotSecret": opts.robotSecret,
		},
		"mirror": map[string]any{"project": RegistryProxyMirrorProject},
		"registry": map[string]any{
			"allowlist": c.ProxyRegistries,
		},
		"proxy": map[string]any{
			"publicURL": c.ProxyPublicURL,
			"basePath":  c.ProxyBasePath,
		},
		"webhook": map[string]any{
			"secretName": opts.webhookSecret,
			"address":    opts.webhookAddress,
		},
		"conversion": map[string]any{
			"platform":   "linux/amd64",
			"nydusify":   "/usr/local/bin/nydusify",
			"nydusImage": "/usr/local/bin/nydus-image",
			"workDir":    "/tmp/nydusify",
			"timeout":    "10m",
			"jobTimeout": "30m",
		},
		"service": map[string]any{"port": RegistryProxyPort},
	}
	if opts.fullnameOverride != "" {
		values["fullnameOverride"] = opts.fullnameOverride
	}
	if c.ProxyRobotSecretRevision != "" {
		values["podAnnotations"] = map[string]any{
			"p2p.dragonfly.io/robot-secret-revision": c.ProxyRobotSecretRevision,
		}
	}
	// Run the release's image instead of the chart's default tag: the
	// operator image contains /registry-proxy, so the proxy stays in lockstep
	// with the operator across upgrades.
	if c.OperatorImageRepository != "" && c.OperatorImageTag != "" {
		values["image"] = map[string]any{
			"repository": c.OperatorImageRepository,
			"tag":        c.OperatorImageTag,
		}
	}
	if strings.TrimSpace(c.ProxyPublicURL) == "" {
		values["ingress"].(map[string]any)["enabled"] = false
	}
	return values
}

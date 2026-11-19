package helmop

import (
	"fmt"
	"net/url"
	"strings"

	fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"
)

// RegistryProxy returns the stateless smart registry proxy HelmOp. The proxy
// has no PVC; it uses Harbor as the source of truth and keeps in-flight
// conversion state in memory.
func RegistryProxy(c Cluster) *fleetv1alpha1.HelmOp {
	harborURL := c.HarborURL
	if harborURL == "" && c.HarborDomain != "" {
		harborURL = "https://" + c.HarborDomain
	}
	ingressHost := ""
	if parsed, err := url.Parse(c.ProxyPublicURL); err == nil {
		ingressHost = parsed.Hostname()
	}
	ingressPath := c.ProxyBasePath
	if ingressPath == "" {
		ingressPath = "/"
	}
	hostAliases := make([]any, 0, len(c.ProxyHostAliases))
	for _, alias := range c.ProxyHostAliases {
		hostAliases = append(hostAliases, map[string]any{"ip": alias.IP, "hostnames": alias.Hostnames})
	}
	// Same-cluster Harbor reaches the proxy over the Service; an external
	// Harbor keeps the chart default (proxy.publicURL + /api/convert).
	webhookAddress := ""
	if c.HarborManaged {
		webhookAddress = fmt.Sprintf("http://%s.%s.svc:%d/api/convert",
			RegistryProxyName(c.Name), TargetNamespace, RegistryProxyPort)
	}
	values := map[string]any{
		"command": []any{"/registry-proxy"},
		"ingress": map[string]any{
			"enabled":   ingressHost != "",
			"className": "traefik",
			"host":      ingressHost,
			"path":      ingressPath,
			"tlsSecret": c.ProxyTLSSecret,
		},
		"harbor": map[string]any{
			"url":         harborURL,
			"adminSecret": c.HarborAdminSecret,
			"robotSecret": c.ProxyRobotSecretName,
		},
		"mirror": map[string]any{"project": RegistryProxyMirrorProject},
		"registry": map[string]any{
			"allowlist": c.ProxyRegistries,
		},
		"proxy": map[string]any{
			"publicURL":   c.ProxyPublicURL,
			"basePath":    c.ProxyBasePath,
			"hostAliases": hostAliases,
		},
		"webhook": map[string]any{
			"secretName": c.ProxyWebhookSecretName,
			"address":    webhookAddress,
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
	// Harbor is an independent, replaceable component: the proxy HelmOp has
	// no dependsOn on it. Use harbor-url/proxy.domain for external endpoints.
	op := newHelmOp(RegistryProxyName(c.Name), c, c.RegistryProxyChart, "registry-proxy", values, nil)
	// The robot Secret is Harbor auth data provisioned by the operator; Fleet
	// copies all three Secrets from the HelmOp namespace to the target
	// namespace. The proxy itself is read-only.
	op.Spec.DownstreamResources = []fleetv1alpha1.DownstreamResource{
		{Kind: "Secret", Name: c.HarborAdminSecret},
		{Kind: "Secret", Name: c.ProxyWebhookSecretName},
		{Kind: "Secret", Name: c.ProxyRobotSecretName},
	}
	return op
}

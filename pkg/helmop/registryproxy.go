package helmop

import (
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
	values := map[string]any{
		"command": []any{"/registry-proxy"},
		"ingress": map[string]any{
			"enabled":   ingressHost != "",
			"className": "traefik",
			"host":      ingressHost,
			"tlsSecret": "",
		},
		"harbor": map[string]any{
			"url":         harborURL,
			"adminSecret": c.HarborAdminSecret,
			"robotSecret": c.ProxyRobotSecretName,
		},
		"mirror": map[string]any{"project": "mirror"},
		"registry": map[string]any{
			"allowlist": c.ProxyRegistries,
		},
		"proxy": map[string]any{"publicURL": c.ProxyPublicURL},
		"webhook": map[string]any{
			"secretName": c.ProxyWebhookSecretName,
		},
		"conversion": map[string]any{
			"platform":   "linux/amd64",
			"nydusify":   "/usr/local/bin/nydusify",
			"nydusImage": "/usr/local/bin/nydus-image",
			"workDir":    "/tmp/nydusify",
			"timeout":    "10m",
		},
		"service": map[string]any{"port": RegistryProxyPort},
	}
	var dependsOn []string
	if c.HarborManaged {
		dependsOn = append(dependsOn, HarborName(c.Name))
	}
	if strings.TrimSpace(c.ProxyPublicURL) == "" {
		values["ingress"].(map[string]any)["enabled"] = false
	}
	op := newHelmOp(RegistryProxyName(c.Name), c, c.RegistryProxyChart, "registry-proxy", values, dependsOn)
	op.Spec.DownstreamResources = []fleetv1alpha1.DownstreamResource{
		{Kind: "Secret", Name: c.HarborAdminSecret},
		{Kind: "Secret", Name: c.ProxyWebhookSecretName},
	}
	return op
}

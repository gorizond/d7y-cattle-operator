package helmop

import (
	"fmt"
	"net/url"
	"strconv"
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
		hostAliases:       registryProxyHostAliases(c.ProxyHostAliases),
	})
	// Harbor is an independent, replaceable component: the proxy HelmOp has
	// no dependsOn on it. Use harbor-url/proxy.domain for external endpoints.
	op := newHelmOp(RegistryProxyName(c.Name), c, c.RegistryProxyChart, "registry-proxy", values, nil)
	op.Spec.DownstreamResources = registryProxyDownstreamResources(
		c.HarborAdminSecret, c.ProxyWebhookSecretName, c.ProxyRobotSecretName, "", "")
	return op
}

// SharedRegistryProxy returns the shared registry-proxy HelmOp. The fullname is
// a Fleet template so the chart still renders <cluster>-registry-proxy on every
// target; the robot and webhook Secrets are fleet-wide fixed names. Per-Cluster
// host aliases come from the p2p.dragonfly.io/proxy-host-aliases annotation,
// falling back to the operator-wide static list.
func SharedRegistryProxy(c Cluster) *fleetv1alpha1.HelmOp {
	c = sharedCluster(c)
	harborURL := strings.TrimSpace(c.ProxyHarborURL)
	if harborURL == "" {
		harborURL = c.HarborURL
	}
	values := registryProxyValues(c, registryProxyOptions{
		fullnameOverride:          SharedClusterNameTemplate,
		tlsSecret:                 SharedProxyTLSSecretTemplate,
		harborAdminSecret:         SharedHarborAdminSecretName(),
		robotSecret:               SharedRegistryProxyRobotSecretName(),
		webhookSecret:             SharedRegistryProxyWebhookSecretName(),
		harborURL:                 harborURL,
		webhookAddress:            SharedProxyWebhookAddressTemplate,
		hostAliases:               sharedProxyHostAliasesTemplate(c.ProxyHostAliases),
		basicAuthEnabled:          c.ProxyBasicAuthEnabled,
		basicAuthUsername:         c.ProxyBasicAuthUsername,
		upstreamCredentialsSecret: c.ProxyUpstreamCredentialsSecret,
	})
	basicAuthSecret := ""
	if c.ProxyBasicAuthEnabled {
		basicAuthSecret = SharedRegistryProxyBasicAuthSecretName()
	}
	op := newSharedHelmOp(SharedRegistryProxyName(), TargetNamespace, c, c.RegistryProxyChart,
		"registry-proxy", values, nil, ProxySelector())
	op.Spec.DownstreamResources = registryProxyDownstreamResources(
		SharedHarborAdminSecretName(), SharedRegistryProxyWebhookSecretName(), SharedRegistryProxyRobotSecretName(),
		basicAuthSecret, c.ProxyUpstreamCredentialsSecret)
	return op
}

func registryProxyDownstreamResources(harborAdmin, webhook, robot, basicAuth, upstream string) []fleetv1alpha1.DownstreamResource {
	resources := []fleetv1alpha1.DownstreamResource{
		{Kind: "Secret", Name: harborAdmin},
		{Kind: "Secret", Name: webhook},
		{Kind: "Secret", Name: robot},
	}
	if basicAuth != "" {
		resources = append(resources, fleetv1alpha1.DownstreamResource{Kind: "Secret", Name: basicAuth})
	}
	if upstream != "" {
		resources = append(resources, fleetv1alpha1.DownstreamResource{Kind: "Secret", Name: upstream})
	}
	return resources
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

// registryProxyHostAliases converts the static operator list into the chart's
// hostAliases list (used by the legacy per-cluster builder).
func registryProxyHostAliases(aliases []HostAlias) []any {
	result := make([]any, 0, len(aliases))
	for _, alias := range aliases {
		result = append(result, map[string]any{"ip": alias.IP, "hostnames": alias.Hostnames})
	}
	return result
}

// sharedProxyHostAliasesTemplate renders the per-Cluster
// p2p.dragonfly.io/proxy-host-aliases annotation into a real YAML list of
// {ip, hostnames:[host]} objects. Fleet marshals helm.values to YAML, renders
// ${ } over the whole document and parses it back; "$" is not a YAML indicator,
// so this string stays unquoted and the rendered [...] becomes a list.
//
// The annotation is a comma-separated list of ip=hostname pairs, e.g.
// "203.0.113.10=harbor.example.com". Items are trimmed and quoted so the
// flow sequence stays valid. Keep the map syntax JSON-style (no space after
// ':') so the YAML marshaller emits this template as an unquoted plain scalar;
// a ": " sequence would force quoting and render a string instead of a list.
// An absent or blank annotation falls back to the static operator-wide list
// (PROXY_HOST_ALIASES), or [] when that is empty.
//
// The guard coerces the lookup with default "": Fleet presents annotations as
// map[string]interface{} (toDict), so index on an absent key returns nil and
// trim nil fails with "invalid value; expected string" before the if can
// evaluate. default "" makes trim see a string; an empty result takes the
// static fallback branch.
func sharedProxyHostAliasesTemplate(static []HostAlias) string {
	return fmt.Sprintf(
		`${ if (trim (index .ClusterAnnotations "p2p.dragonfly.io/proxy-host-aliases" | default "")) }`+
			`[${ range $i, $pair := splitList "," (index .ClusterAnnotations "p2p.dragonfly.io/proxy-host-aliases") }${ if $i },${ end }`+
			`{"ip":${ trim (index (splitn "=" 2 $pair) "_0") | quote },"hostnames":[${ trim (index (splitn "=" 2 $pair) "_1") | quote }]}`+
			`${ end }]${ else }%s${ end }`,
		hostAliasesYAML(static),
	)
}

// hostAliasesYAML renders the static operator list as a valid YAML flow
// sequence. The config validates the IPs and hostnames, so strconv.Quote is
// enough to keep the values safe.
func hostAliasesYAML(aliases []HostAlias) string {
	if len(aliases) == 0 {
		return "[]"
	}
	items := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		hostnames := make([]string, 0, len(alias.Hostnames))
		for _, hostname := range alias.Hostnames {
			hostnames = append(hostnames, strconv.Quote(hostname))
		}
		items = append(items, fmt.Sprintf(`{"ip":%s,"hostnames":[%s]}`,
			strconv.Quote(alias.IP), strings.Join(hostnames, ",")))
	}
	return "[" + strings.Join(items, ",") + "]"
}

type registryProxyOptions struct {
	fullnameOverride          string
	tlsSecret                 string
	harborAdminSecret         string
	robotSecret               string
	webhookSecret             string
	harborURL                 string
	webhookAddress            string
	hostAliases               any
	basicAuthEnabled          bool
	basicAuthUsername         string
	upstreamCredentialsSecret string
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
		"auth": map[string]any{
			"mode": "network-only",
		},
		"mirror": map[string]any{"project": RegistryProxyMirrorProject},
		"registry": map[string]any{
			"allowlist": c.ProxyRegistries,
		},
		"proxy": map[string]any{
			"publicURL":   c.ProxyPublicURL,
			"basePath":    c.ProxyBasePath,
			"hostAliases": opts.hostAliases,
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
	if opts.basicAuthEnabled {
		auth := values["auth"].(map[string]any)
		auth["mode"] = "basic"
		auth["username"] = opts.basicAuthUsername
		auth["passwordSecret"] = SharedRegistryProxyBasicAuthSecretName()
	}
	if opts.upstreamCredentialsSecret != "" {
		values["upstream"] = map[string]any{
			"credentialsSecret": opts.upstreamCredentialsSecret,
			"credentialsFile":   "/etc/registry-proxy/secrets/upstream/credentials.yaml",
		}
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

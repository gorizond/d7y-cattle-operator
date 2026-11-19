package helmop

import "testing"

func TestHarborHelmOp(t *testing.T) {
	c := testCluster()
	c.HarborDomain = "harbor.example.com"
	c.HarborAdminSecret = "c1-harbor-admin"
	c.HarborDBPassword = "db-password"
	c.HarborSecretKey = "c1-harbor-secret"
	op := Harbor(c)

	if op.Name != "c1-harbor" || op.Spec.TargetNamespace != HarborTargetNamespace {
		t.Errorf("name/target namespace = %s/%s", op.Name, op.Spec.TargetNamespace)
	}
	if op.Spec.Helm.Chart != "harbor" || op.Spec.Helm.Version != "1.19.4" {
		t.Errorf("chart = %s %s", op.Spec.Helm.Chart, op.Spec.Helm.Version)
	}
	if len(op.Spec.DependsOn) != 1 || op.Spec.DependsOn[0].Name != "c1-keydb" {
		t.Errorf("dependsOn = %+v, want c1-keydb", op.Spec.DependsOn)
	}
	if len(op.Spec.DownstreamResources) != 2 ||
		op.Spec.DownstreamResources[0].Name != "c1-harbor-admin" ||
		op.Spec.DownstreamResources[1].Name != "c1-harbor-secret" {
		t.Errorf("downstreamResources = %+v", op.Spec.DownstreamResources)
	}

	values := op.Spec.Helm.Values.Data
	if got := valueAt(t, values, "redis", "type"); got != "external" {
		t.Errorf("redis.type = %v, want external", got)
	}
	if got := valueAt(t, values, "redis", "external", "addr"); got != "c1-keydb.dragonfly-system.svc:6379" {
		t.Errorf("redis.external.addr = %v", got)
	}
	if got := valueAt(t, values, "database", "type"); got != "internal" {
		t.Errorf("database.type = %v", got)
	}
	if got := valueAt(t, values, "trivy", "enabled"); got != false {
		t.Errorf("trivy.enabled = %v", got)
	}
	if got := valueAt(t, values, "existingSecretAdminPassword"); got != "c1-harbor-admin" {
		t.Errorf("existingSecretAdminPassword = %v", got)
	}
	if got := valueAt(t, values, "database", "internal", "password"); got != "db-password" {
		t.Errorf("database.internal.password = %v", got)
	}
	if got := valueAt(t, values, "existingSecretSecretKey"); got != "c1-harbor-secret" {
		t.Errorf("existingSecretSecretKey = %v", got)
	}
	if got := valueAt(t, values, "externalURL"); got != "https://harbor.example.com" {
		t.Errorf("externalURL = %v", got)
	}
	if got := valueAt(t, values, "persistence", "resourcePolicy"); got != "keep" {
		t.Errorf("persistence.resourcePolicy = %v", got)
	}
	pvcs := nested(t, values, "persistence", "persistentVolumeClaim")
	for _, name := range []string{"registry", "database"} {
		if _, ok := pvcs[name]; !ok {
			t.Errorf("persistence.persistentVolumeClaim.%s is missing", name)
		}
	}
	if jobservice, ok := pvcs["jobservice"].(map[string]any); !ok || jobservice["jobLog"] == nil {
		t.Errorf("persistence.persistentVolumeClaim.jobservice.jobLog is missing")
	}
	if _, ok := pvcs["redis"]; ok {
		t.Error("Harbor values contain a Redis PVC")
	}
	if containsKey(values, "redis.persistentVolumeClaim") {
		t.Error("Harbor values contain redis persistence")
	}
}

func TestRegistryProxyHelmOp(t *testing.T) {
	c := testCluster()
	c.HarborURL = ""
	c.HarborDomain = "harbor.example.com"
	c.HarborManaged = true
	c.ProxyEnabled = true
	c.ProxyPublicURL = "https://proxy.example.com/proxy"
	c.ProxyBasePath = "/proxy"
	c.ProxyTLSSecret = "poligon-harbor-tls"
	c.HarborAdminSecret = "c1-harbor-admin"
	c.ProxyWebhookSecretName = "c1-registry-proxy-webhook"
	c.ProxyRobotSecretName = "c1-registry-proxy-robot"
	c.ProxyRobotSecretRevision = "rev-1"
	c.ProxyRegistries = []string{"docker.io"}
	c.ProxyHostAliases = []HostAlias{{IP: "10.0.0.10", Hostnames: []string{"harbor.example.com"}}}
	op := RegistryProxy(c)

	if op.Name != "c1-registry-proxy" || op.Spec.TargetNamespace != TargetNamespace {
		t.Errorf("name/target namespace = %s/%s", op.Name, op.Spec.TargetNamespace)
	}
	if op.Spec.Helm.Chart != "registry-proxy" || op.Spec.Helm.Version != "0.1.11" {
		t.Errorf("chart = %s %s", op.Spec.Helm.Chart, op.Spec.Helm.Version)
	}
	if len(op.Spec.DependsOn) != 0 {
		t.Errorf("dependsOn = %+v, want none (independent toggles)", op.Spec.DependsOn)
	}
	if len(op.Spec.DownstreamResources) != 3 ||
		op.Spec.DownstreamResources[0].Kind != "Secret" || op.Spec.DownstreamResources[0].Name != "c1-harbor-admin" ||
		op.Spec.DownstreamResources[1].Kind != "Secret" || op.Spec.DownstreamResources[1].Name != "c1-registry-proxy-webhook" ||
		op.Spec.DownstreamResources[2].Kind != "Secret" || op.Spec.DownstreamResources[2].Name != "c1-registry-proxy-robot" {
		t.Errorf("downstreamResources = %+v", op.Spec.DownstreamResources)
	}
	if containsKey(op.Spec.Helm.Values.Data, "persistence") || containsKey(op.Spec.Helm.Values.Data, "persistentVolumeClaim") {
		t.Error("registry proxy values request persistence")
	}
	values := op.Spec.Helm.Values.Data
	if got := valueAt(t, values, "harbor", "url"); got != "https://harbor.example.com" {
		t.Errorf("harbor.url = %v", got)
	}
	if got := valueAt(t, values, "harbor", "robotSecret"); got != "c1-registry-proxy-robot" {
		t.Errorf("harbor.robotSecret = %v", got)
	}
	if got := valueAt(t, values, "podAnnotations", "p2p.dragonfly.io/robot-secret-revision"); got != "rev-1" {
		t.Errorf("podAnnotations robot revision = %v", got)
	}
	if got := valueAt(t, values, "mirror", "project"); got != "mirror" {
		t.Errorf("mirror.project = %v", got)
	}
	allowlist, ok := valueAt(t, values, "registry", "allowlist").([]string)
	if !ok || len(allowlist) != 1 || allowlist[0] != "docker.io" {
		t.Errorf("registry.allowlist = %v, want c.ProxyRegistries", valueAt(t, values, "registry", "allowlist"))
	}
	if got := valueAt(t, values, "proxy", "publicURL"); got != "https://proxy.example.com/proxy" {
		t.Errorf("proxy.publicURL = %v", got)
	}
	if got := valueAt(t, values, "ingress", "path"); got != "/proxy" {
		t.Errorf("ingress.path = %v", got)
	}
	if got := valueAt(t, values, "ingress", "tlsSecret"); got != "poligon-harbor-tls" {
		t.Errorf("ingress.tlsSecret = %v", got)
	}
	if got := valueAt(t, values, "proxy", "basePath"); got != "/proxy" {
		t.Errorf("proxy.basePath = %v", got)
	}
	if got := valueAt(t, values, "webhook", "address"); got != "http://c1-registry-proxy.dragonfly-system.svc:8080/api/convert" {
		t.Errorf("webhook.address = %v", got)
	}
	aliases, ok := valueAt(t, values, "proxy", "hostAliases").([]any)
	if !ok || len(aliases) != 1 {
		t.Fatalf("proxy.hostAliases = %v", valueAt(t, values, "proxy", "hostAliases"))
	}
	alias, _ := aliases[0].(map[string]any)
	if alias["ip"] != "10.0.0.10" {
		t.Errorf("proxy.hostAliases[0].ip = %v", alias["ip"])
	}
	if got := valueAt(t, values, "conversion", "nydusImage"); got != "/usr/local/bin/nydus-image" {
		t.Errorf("conversion.nydusImage = %v", got)
	}
}

func TestRegistryProxyExternalHarborHasNoHarborDependency(t *testing.T) {
	c := testCluster()
	c.HarborManaged = false
	c.HarborURL = "https://external.example.com"
	c.HarborDomain = ""
	c.ProxyEnabled = true
	c.ProxyPublicURL = "https://proxy.example.com"
	op := RegistryProxy(c)
	if len(op.Spec.DependsOn) != 0 {
		t.Errorf("dependsOn = %+v, want none for external Harbor", op.Spec.DependsOn)
	}
	if got := valueAt(t, op.Spec.Helm.Values.Data, "harbor", "url"); got != "https://external.example.com" {
		t.Errorf("harbor.url = %v", got)
	}
	if got := valueAt(t, op.Spec.Helm.Values.Data, "webhook", "address"); got != "" {
		t.Errorf("webhook.address = %v, want the chart default for an external Harbor", got)
	}
}

func TestRegistryProxyEmptyBasePathUsesRoot(t *testing.T) {
	c := testCluster()
	c.HarborManaged = true
	c.ProxyPublicURL = "https://proxy.example.com"
	c.ProxyBasePath = ""
	c.ProxyTLSSecret = "poligon-proxy-tls"
	op := RegistryProxy(c)
	values := op.Spec.Helm.Values.Data
	if got := valueAt(t, values, "ingress", "path"); got != "/" {
		t.Errorf("ingress.path = %v, want /", got)
	}
	if got := valueAt(t, values, "proxy", "basePath"); got != "" {
		t.Errorf("proxy.basePath = %v, want empty", got)
	}
	if got := valueAt(t, values, "ingress", "tlsSecret"); got != "poligon-proxy-tls" {
		t.Errorf("ingress.tlsSecret = %v", got)
	}
}

func TestRegistryProxyImageUsesOperatorImage(t *testing.T) {
	c := testCluster()
	c.OperatorImageRepository = "ghcr.io/gorizond/d7y-cattle-operator"
	c.OperatorImageTag = "v0.1.45"
	values := RegistryProxy(c).Spec.Helm.Values.Data
	if got := valueAt(t, values, "image", "repository"); got != "ghcr.io/gorizond/d7y-cattle-operator" {
		t.Errorf("image.repository = %v", got)
	}
	if got := valueAt(t, values, "image", "tag"); got != "v0.1.45" {
		t.Errorf("image.tag = %v", got)
	}
}

func TestRegistryProxyImageKeepsChartDefaultWithoutOperatorImage(t *testing.T) {
	c := testCluster()
	if _, ok := RegistryProxy(c).Spec.Helm.Values.Data["image"]; ok {
		t.Error("registry proxy values contain an image override without an operator image")
	}
}

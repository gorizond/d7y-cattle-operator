package helmop

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"text/template"

	fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"
	kyaml "sigs.k8s.io/yaml"
)

func sharedTestCluster() Cluster {
	c := testCluster()
	c.P2PDomain = "example.com"
	c.AdmissionChart = Chart{Repo: "https://charts.example.com", Name: "admission-webhook", Version: "0.1.0"}
	c.ProxyDomain = "proxy.example.com"
	return c
}

func TestSharedHelmOpNamesAndReleases(t *testing.T) {
	c := sharedTestCluster()
	ops := map[string]struct {
		op   *fleetv1alpha1.HelmOp
		name string
	}{
		"keydb":     {SharedKeyDB(c), "d7y-keydb"},
		"manager":   {SharedManager(c), "d7y-manager"},
		"control":   {SharedControl(c), "d7y-control"},
		"data":      {SharedData(c), "d7y-data"},
		"extras":    {SharedExtras(c, ExtrasOptions{}), "d7y-extras"},
		"mirror":    {SharedMirror(c), "d7y-containerd-mirror"},
		"admission": {SharedAdmission(c), "d7y-admission"},
		"proxy":     {SharedRegistryProxy(c), "d7y-registry-proxy"},
		"harbor":    {SharedHarbor(c), "d7y-harbor"},
	}
	for component, entry := range ops {
		if entry.op.Name != entry.name {
			t.Errorf("%s HelmOp name = %q, want %q", component, entry.op.Name, entry.name)
		}
		if entry.op.Spec.Helm == nil || entry.op.Spec.Helm.ReleaseName != entry.name {
			t.Errorf("%s releaseName = %+v, want %q", component, entry.op.Spec.Helm, entry.name)
		}
		if entry.op.Spec.Helm == nil || !entry.op.Spec.Helm.TakeOwnership {
			t.Errorf("%s takeOwnership = %+v, want true", component, entry.op.Spec.Helm)
		}
		if len(entry.op.Spec.Targets) != 1 || entry.op.Spec.Targets[0].ClusterName != "" {
			t.Errorf("%s targets = %+v, want one selector target", component, entry.op.Spec.Targets)
		}
		if entry.op.Spec.Helm != nil && len(entry.op.Spec.Helm.PreserveOnReplace) != 0 {
			t.Errorf("%s preserveOnReplace = %+v, want none", component, entry.op.Spec.Helm.PreserveOnReplace)
		}
	}
}

func TestSharedHelmOpSelectors(t *testing.T) {
	c := sharedTestCluster()
	tests := map[string]struct {
		op   *fleetv1alpha1.HelmOp
		want map[string]string
	}{
		"keydb":     {SharedKeyDB(c), map[string]string{LabelGate: "true"}},
		"manager":   {SharedManager(c), map[string]string{LabelGate: "true", LabelRoleManager: "true"}},
		"control":   {SharedControl(c), map[string]string{LabelGate: "true", LabelRoleSeed: "true"}},
		"data":      {SharedData(c), map[string]string{LabelGate: "true", LabelRoleSeed: "true"}},
		"extras":    {SharedExtras(c, ExtrasOptions{}), map[string]string{LabelGate: "true", LabelRoleSeed: "true"}},
		"mirror":    {SharedMirror(c), map[string]string{LabelGate: "true", LabelRoleSeed: "true"}},
		"admission": {SharedAdmission(c), map[string]string{LabelGate: "true", LabelRoleAdmission: "true"}},
		"proxy":     {SharedRegistryProxy(c), map[string]string{LabelGate: "true", LabelRoleProxy: "true"}},
		"harbor":    {SharedHarbor(c), map[string]string{LabelGate: "true", LabelRoleHarbor: "true"}},
	}
	for component, test := range tests {
		selector := test.op.Spec.Targets[0].ClusterSelector
		if selector == nil {
			t.Errorf("%s target has no clusterSelector", component)
			continue
		}
		if len(selector.MatchLabels) != len(test.want) {
			t.Errorf("%s selector = %v, want %v", component, selector.MatchLabels, test.want)
			continue
		}
		for key, value := range test.want {
			if selector.MatchLabels[key] != value {
				t.Errorf("%s selector[%q] = %q, want %q", component, key, selector.MatchLabels[key], value)
			}
		}
	}
}

func TestSharedValuesUseFleetTemplates(t *testing.T) {
	c := sharedTestCluster()

	keydb := SharedKeyDB(c).Spec.Helm.Values.Data
	if got := keydb["fullnameOverride"]; got != "${ .ClusterName }-keydb" {
		t.Errorf("keydb fullnameOverride = %v", got)
	}

	manager := SharedManager(c).Spec.Helm.Values.Data
	if got := manager["fullnameOverride"]; got != SharedClusterNameTemplate {
		t.Errorf("manager fullnameOverride = %v, want the cluster-name template", got)
	}
	managerAddrs, _ := manager["externalRedis"].(map[string]any)["addrs"].([]any)
	wantKeyDBAddr := SharedClusterNameTemplate + "-keydb." + TargetNamespace + ".svc.cluster.local:6379"
	if len(managerAddrs) != 1 || managerAddrs[0] != wantKeyDBAddr {
		t.Errorf("manager externalRedis.addrs = %v, want [%s]", managerAddrs, wantKeyDBAddr)
	}

	control := SharedControl(c).Spec.Helm.Values.Data
	if got := control["fullnameOverride"]; got != "${ .ClusterName }-control" {
		t.Errorf("control fullnameOverride = %v", got)
	}
	addrs, _ := control["externalRedis"].(map[string]any)["addrs"].([]any)
	if len(addrs) != 1 || addrs[0] != "${ .ClusterName }-keydb:6379" {
		t.Errorf("control externalRedis.addrs = %v", addrs)
	}
	if got := valueAt(t, control, "scheduler", "config", "manager", "schedulerClusterID"); got != SharedSchedulerClusterIDTemplate {
		t.Errorf("control schedulerClusterID = %v", got)
	}
	if got := valueAt(t, control, "scheduler", "config", "server", "advertiseIP"); got != SharedSchedulerPublicIPTemplate {
		t.Errorf("control advertiseIP = %v", got)
	}

	data := SharedData(c).Spec.Helm.Values.Data
	if got := data["fullnameOverride"]; got != "${ .ClusterName }-data" {
		t.Errorf("data fullnameOverride = %v", got)
	}
	wantAddr := SharedSchedulerAddrTemplate("example.com")
	if got := valueAt(t, data, "seedClient", "dynconfig", "scheduler", "addr"); got != wantAddr {
		t.Errorf("seedClient dynconfig addr = %v, want %v", got, wantAddr)
	}
	if got := valueAt(t, data, "client", "dynconfig", "scheduler", "addr"); got != wantAddr {
		t.Errorf("client dynconfig addr = %v, want %v", got, wantAddr)
	}
	if got := valueAt(t, data, "client", "config", "host", "schedulerClusterID"); got != SharedSchedulerClusterIDTemplate {
		t.Errorf("data schedulerClusterID = %v", got)
	}

	extras := SharedExtras(c, ExtrasOptions{}).Spec.Helm.Values.Data
	if got := valueAt(t, extras, "ingress", "grpc", "service"); got != "${ .ClusterName }-manager" {
		t.Errorf("extras ingress.grpc.service = %v", got)
	}
	if got := valueAt(t, extras, "scheduler", "service"); got != "${ .ClusterName }-control-scheduler" {
		t.Errorf("extras scheduler.service = %v", got)
	}
	if got := valueAt(t, extras, "scheduler", "publicIP"); got != SharedSchedulerPublicIPTemplate {
		t.Errorf("extras scheduler.publicIP = %v", got)
	}
	if got := valueAt(t, extras, "vpa", "targets", "data", "name"); got != "${ .ClusterName }-data-seed-client" {
		t.Errorf("extras data VPA target = %v", got)
	}

	admission := SharedAdmission(c)
	if admission.Spec.TargetNamespace != "" || admission.Spec.DefaultNamespace != TargetNamespace {
		t.Errorf("admission namespaces = %q/%q", admission.Spec.TargetNamespace, admission.Spec.DefaultNamespace)
	}
	admissionValues := admission.Spec.Helm.Values.Data
	excluded, ok := valueAt(t, admissionValues, "namespaceSelector", "excludedNamespaces").(string)
	if !ok || excluded != sharedAdmissionExcludedNamespacesTemplate {
		t.Errorf("admission excludedNamespaces = %v, want the mutate-exclure template", excluded)
	}
	for _, want := range []string{
		`index .ClusterAnnotations "p2p.dragonfly.io/mutate-exclure"`,
		`splitList ","`,
		`trim $v | quote`,
		`]${ else }[]`,
	} {
		if !strings.Contains(sharedAdmissionExcludedNamespacesTemplate, want) {
			t.Errorf("admission template lacks %q", want)
		}
	}

	// The legacy per-cluster builder still passes the parsed list.
	perCluster := c
	perCluster.AdmissionExcludeNamespaces = []string{"ns-a", "ns-b"}
	perClusterValues := Admission(perCluster).Spec.Helm.Values.Data
	parsed, ok := valueAt(t, perClusterValues, "namespaceSelector", "excludedNamespaces").([]string)
	if !ok || len(parsed) != 2 || parsed[0] != "ns-a" || parsed[1] != "ns-b" {
		t.Errorf("per-cluster excludedNamespaces = %v, want [ns-a ns-b]", parsed)
	}

	// No other shared component may consume the admission annotation.
	for name, op := range map[string]*fleetv1alpha1.HelmOp{
		"keydb":   SharedKeyDB(c),
		"manager": SharedManager(c),
		"control": SharedControl(c),
		"data":    SharedData(c),
		"extras":  SharedExtras(c, ExtrasOptions{}),
		"mirror":  SharedMirror(c),
		"proxy":   SharedRegistryProxy(c),
		"harbor":  SharedHarbor(c),
	} {
		if valuesContain(op.Spec.Helm.Values.Data, "mutate-exclure") {
			t.Errorf("%s values unexpectedly consume the mutate-exclure annotation", name)
		}
	}
}

func valuesContain(value any, substring string) bool {
	switch typed := value.(type) {
	case string:
		return strings.Contains(typed, substring)
	case map[string]any:
		for _, child := range typed {
			if valuesContain(child, substring) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if valuesContain(child, substring) {
				return true
			}
		}
	case []string:
		for _, child := range typed {
			if strings.Contains(child, substring) {
				return true
			}
		}
	}
	return false
}

func TestSchedulerHostTemplate(t *testing.T) {
	want := `d7y-scheduler.${ index .ClusterLabels "p2p.dragonfly.io/cluster-id" }.example.com`
	if got := SchedulerHostTemplate("example.com"); got != want {
		t.Errorf("SchedulerHostTemplate() = %q, want %q", got, want)
	}
	if got := SchedulerHostTemplate(""); got != "" {
		t.Errorf("SchedulerHostTemplate(\"\") = %q, want empty", got)
	}
}

// TestSharedDynconfigUsesSchedulerHost guards the Traefik TCP-TLS HostSNI
// contract: the seed client's dynconfig must dial the scheduler hostname, not
// the public IP, otherwise the TLS session carries no SNI and is not routed.
func TestSharedDynconfigUsesSchedulerHost(t *testing.T) {
	c := sharedTestCluster()
	values := SharedData(c).Spec.Helm.Values.Data
	for _, path := range [][]string{
		{"seedClient", "dynconfig", "scheduler", "addr"},
		{"client", "dynconfig", "scheduler", "addr"},
	} {
		addr, _ := valueAt(t, values, path...).(string)
		if !strings.Contains(addr, `d7y-scheduler.${ index .ClusterLabels`) {
			t.Errorf("%v = %q, want the scheduler host template", path, addr)
		}
		if strings.Contains(addr, "scheduler-public-ip") {
			t.Errorf("%v = %q, must not use the public-IP template for dynconfig", path, addr)
		}
	}
	// The IP remains required for the scheduler's advertised address.
	control := SharedControl(c).Spec.Helm.Values.Data
	if got := valueAt(t, control, "scheduler", "config", "server", "advertiseIP"); got != SharedSchedulerPublicIPTemplate {
		t.Errorf("control advertiseIP = %v, want the public-IP template", got)
	}
}

func TestSharedHelmOpsHaveNoPerClusterLabels(t *testing.T) {
	c := sharedTestCluster()
	for name, op := range map[string]*fleetv1alpha1.HelmOp{
		"keydb":     SharedKeyDB(c),
		"manager":   SharedManager(c),
		"control":   SharedControl(c),
		"data":      SharedData(c),
		"extras":    SharedExtras(c, ExtrasOptions{}),
		"mirror":    SharedMirror(c),
		"admission": SharedAdmission(c),
		"proxy":     SharedRegistryProxy(c),
		"harbor":    SharedHarbor(c),
	} {
		if got := op.Labels["p2p.dragonfly.io/cluster-name"]; got != "" {
			t.Errorf("%s carries a per-cluster label %q", name, got)
		}
		if got := op.Labels["app.kubernetes.io/managed-by"]; got != "d7y-cattle-operator" {
			t.Errorf("%s managed-by = %q", name, got)
		}
	}
}

func TestSharedNamesAreTemplates(t *testing.T) {
	names := SharedNames()
	for suffix, name := range map[string]string{
		"keydb":             names.KeyDB,
		"manager":           names.Manager,
		"control":           names.Control,
		"data":              names.Data,
		"control-scheduler": names.ControlScheduler,
		"data-seed-client":  names.DataSeedClient,
	} {
		want := SharedClusterNameTemplate + "-" + suffix
		if name != want {
			t.Errorf("%s name = %q, want %q", suffix, name, want)
		}
	}
}

func TestPerClusterHelmOpsKeepStrictOwnership(t *testing.T) {
	c := sharedTestCluster()
	ops := map[string]*fleetv1alpha1.HelmOp{
		"keydb":   KeyDB(c),
		"manager": Manager(c),
		"control": Control(c),
		"data":    Data(c),
		"extras":  Extras(c, ExtrasOptions{IngressEnabled: true, Manager: true}),
		"proxy":   RegistryProxy(c),
		"harbor":  Harbor(c),
	}
	for name, op := range ops {
		if op.Spec.Helm == nil {
			t.Fatalf("%s spec.helm is nil", name)
		}
		if op.Spec.Helm.TakeOwnership {
			t.Errorf("%s takeOwnership = true, want strict ownership for a per-cluster release", name)
		}
	}
}

func TestSharedManagerKeepsGlobalSecrets(t *testing.T) {
	op := SharedManager(sharedTestCluster())
	if len(op.Spec.DownstreamResources) != 2 ||
		op.Spec.DownstreamResources[0].Kind != "Secret" ||
		op.Spec.DownstreamResources[0].Name != "dragonfly-p2p-mtls" ||
		op.Spec.DownstreamResources[1].Kind != "Secret" ||
		op.Spec.DownstreamResources[1].Name != "dragonfly-manager-api" {
		t.Errorf("downstreamResources = %+v, want the fleet-global mTLS and manager credentials Secrets", op.Spec.DownstreamResources)
	}
}

func TestSharedRegistryProxyValues(t *testing.T) {
	c := sharedTestCluster()
	c.ProxyHarborURL = "https://harbor.example.com"
	c.ProxyRobotSecretRevision = "rev-1"
	op := SharedRegistryProxy(c)

	values := op.Spec.Helm.Values.Data
	if got := values["fullnameOverride"]; got != SharedClusterNameTemplate {
		t.Errorf("fullnameOverride = %v, want %s", got, SharedClusterNameTemplate)
	}
	if got := valueAt(t, values, "ingress", "tlsSecret"); got != SharedProxyTLSSecretTemplate {
		t.Errorf("ingress.tlsSecret = %v, want %s", got, SharedProxyTLSSecretTemplate)
	}
	for path, want := range map[string]any{
		"harbor.url":         "https://harbor.example.com",
		"harbor.adminSecret": SharedHarborAdminSecretName(),
		"harbor.robotSecret": SharedRegistryProxyRobotSecretName(),
		"webhook.secretName": SharedRegistryProxyWebhookSecretName(),
		"webhook.address":    SharedProxyWebhookAddressTemplate,
	} {
		keys := strings.Split(path, ".")
		if got := valueAt(t, values, keys...); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
	if len(op.Spec.DownstreamResources) != 3 ||
		op.Spec.DownstreamResources[0].Name != SharedHarborAdminSecretName() ||
		op.Spec.DownstreamResources[1].Name != SharedRegistryProxyWebhookSecretName() ||
		op.Spec.DownstreamResources[2].Name != SharedRegistryProxyRobotSecretName() {
		t.Errorf("downstreamResources = %+v", op.Spec.DownstreamResources)
	}
}

func TestSharedHarborValues(t *testing.T) {
	c := sharedTestCluster()
	c.HarborDBPassword = "db-password"
	op := SharedHarbor(c)

	if op.Spec.Helm == nil {
		t.Fatal("shared Harbor spec.helm is nil")
	}
	if op.Spec.Helm.Force {
		t.Errorf("shared Harbor force = true, want unset after the one-time adoption")
	}
	if len(op.Spec.Helm.PreserveOnReplace) != 0 {
		t.Errorf("shared Harbor preserveOnReplace = %+v, want none (the supported Fleet CRD prunes it)", op.Spec.Helm.PreserveOnReplace)
	}
	values := op.Spec.Helm.Values.Data
	if got := values["fullnameOverride"]; got != SharedHarborFullnameTemplate {
		t.Errorf("fullnameOverride = %v, want %s", got, SharedHarborFullnameTemplate)
	}
	if got := valueAt(t, values, "externalURL"); got != "https://"+SharedHarborDomainTemplate {
		t.Errorf("externalURL = %v", got)
	}
	if got := valueAt(t, values, "expose", "ingress", "hosts", "core"); got != SharedHarborDomainTemplate {
		t.Errorf("ingress host = %v", got)
	}
	if got := valueAt(t, values, "expose", "tls", "secret", "secretName"); got != SharedHarborTLSSecretTemplate {
		t.Errorf("ingress TLS Secret = %v", got)
	}
	for path, want := range map[string]any{
		"existingSecretAdminPassword": SharedHarborAdminSecretName(),
		"existingSecretSecretKey":     SharedHarborSecretKeyName(),
		"database.internal.password":  "db-password",
		"persistence.resourcePolicy":  "keep",
		"redis.external.addr":         SharedClusterNameTemplate + "-keydb.dragonfly-system.svc:6379",
	} {
		keys := strings.Split(path, ".")
		if got := valueAt(t, values, keys...); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
	if len(op.Spec.DependsOn) != 1 || op.Spec.DependsOn[0].Name != SharedKeyDBName() {
		t.Errorf("dependsOn = %+v, want shared KeyDB", op.Spec.DependsOn)
	}
	if len(op.Spec.DownstreamResources) != 2 ||
		op.Spec.DownstreamResources[0].Name != SharedHarborAdminSecretName() ||
		op.Spec.DownstreamResources[1].Name != SharedHarborSecretKeyName() {
		t.Errorf("downstreamResources = %+v", op.Spec.DownstreamResources)
	}
}

func TestSharedRegistryProxyHostAliasesTemplate(t *testing.T) {
	c := sharedTestCluster()
	noStaticTemplate, ok := valueAt(t, SharedRegistryProxy(c).Spec.Helm.Values.Data, "proxy", "hostAliases").(string)
	if !ok {
		t.Fatalf("shared proxy hostAliases = %T, want the Fleet template string",
			valueAt(t, SharedRegistryProxy(c).Spec.Helm.Values.Data, "proxy", "hostAliases"))
	}
	for _, want := range []string{
		AnnotationProxyHostAliases,
		`splitList ","`,
		`splitn "=" 2 $pair`,
		`trim (index (splitn "=" 2 $pair) "_0") | quote`,
		`| default ""`,
	} {
		if !strings.Contains(noStaticTemplate, want) {
			t.Errorf("shared proxy hostAliases template lacks %q: %s", want, noStaticTemplate)
		}
	}
	if !strings.Contains(noStaticTemplate, `]${ else }[`) {
		t.Errorf("shared proxy hostAliases template lacks the empty/static fallback: %s", noStaticTemplate)
	}

	c.ProxyHostAliases = []HostAlias{{IP: "10.0.0.10", Hostnames: []string{"harbor.example.com"}}}
	staticTemplate, ok := valueAt(t, SharedRegistryProxy(c).Spec.Helm.Values.Data, "proxy", "hostAliases").(string)
	if !ok {
		t.Fatal("shared proxy hostAliases with a static fallback is not a template string")
	}
	assertStatic := func(rendered []map[string]any) {
		t.Helper()
		if len(rendered) != 1 || rendered[0]["ip"] != "10.0.0.10" {
			t.Fatalf("static fallback rendered %v, want the 10.0.0.10 alias", rendered)
		}
		hosts, _ := rendered[0]["hostnames"].([]any)
		if len(hosts) != 1 || hosts[0] != "harbor.example.com" {
			t.Errorf("static fallback hostnames = %v", hosts)
		}
	}

	// 1. No annotation at all. Fleet exposes annotations as
	// map[string]interface{}, so index returns nil for an absent key; the
	// default "" coercion must keep the template renderable and the static
	// operator list must be used.
	assertStatic(renderFleetValue(t, staticTemplate, nil))

	// 2. An empty annotation value also falls back to the static list.
	assertStatic(renderFleetValue(t, staticTemplate, map[string]any{AnnotationProxyHostAliases: ""}))

	// 3. A real annotation renders a parsed list of {ip, hostnames:[host]}.
	rendered := renderFleetValue(t, noStaticTemplate, map[string]any{
		AnnotationProxyHostAliases: "203.0.113.10=harbor.example.com",
	})
	hosts, _ := rendered[0]["hostnames"].([]any)
	if len(rendered) != 1 || rendered[0]["ip"] != "203.0.113.10" || len(hosts) != 1 || hosts[0] != "harbor.example.com" {
		t.Errorf("annotation rendered %v, want the harbor.example.com alias", rendered)
	}

	// 4. No static list and no annotation renders a real empty list.
	if rendered := renderFleetValue(t, noStaticTemplate, nil); len(rendered) != 0 {
		t.Errorf("empty annotation with no static list rendered %v, want []", rendered)
	}
}

// renderFleetValue mirrors Fleet's values templating: the template is rendered
// with ${ } delimiters and the result is parsed as YAML.
func renderFleetValue(t *testing.T, text string, annotations map[string]any) []map[string]any {
	t.Helper()
	// Mirror Fleet: marshal the values map to YAML, render the whole document,
	// then parse it back.
	raw, err := kyaml.Marshal(map[string]any{"proxy": map[string]any{"hostAliases": text}})
	if err != nil {
		t.Fatalf("marshal values: %v", err)
	}
	tmpl, err := template.New("values").Funcs(fleetTemplateFuncs()).Option("missingkey=error").Delims("${", "}").Parse(string(raw))
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}
	var b bytes.Buffer
	if err := tmpl.Execute(&b, map[string]any{
		"ClusterAnnotations": annotations,
		"ClusterLabels":      map[string]string{},
	}); err != nil {
		t.Fatalf("render template: %v", err)
	}
	var doc map[string]any
	if err := kyaml.Unmarshal(b.Bytes(), &doc); err != nil {
		t.Fatalf("parse rendered values %q: %v", b.String(), err)
	}
	proxy, _ := doc["proxy"].(map[string]any)
	rawAliases, _ := proxy["hostAliases"].([]any)
	value := make([]map[string]any, 0, len(rawAliases))
	for _, item := range rawAliases {
		if alias, ok := item.(map[string]any); ok {
			value = append(value, alias)
		}
	}
	return value
}

func TestSharedHarborTLSSecretTemplate(t *testing.T) {
	base := map[string]any{
		"ClusterName":        "poligon",
		"ClusterAnnotations": map[string]string{},
		"ClusterLabels":      map[string]string{},
	}
	if got := renderFleetString(t, SharedHarborTLSSecretTemplate, base); got != "poligon-harbor-tls" {
		t.Errorf("default Harbor TLS Secret = %q, want poligon-harbor-tls", got)
	}
	base["ClusterAnnotations"] = map[string]string{
		"p2p.dragonfly.io/harbor-tls-secret": "custom-harbor-tls",
	}
	if got := renderFleetString(t, SharedHarborTLSSecretTemplate, base); got != "custom-harbor-tls" {
		t.Errorf("annotated Harbor TLS Secret = %q, want custom-harbor-tls", got)
	}
}

func renderFleetString(t *testing.T, text string, data map[string]any) string {
	t.Helper()
	tmpl, err := template.New("values").Funcs(fleetTemplateFuncs()).Option("missingkey=error").Delims("${", "}").Parse(text)
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}
	var b bytes.Buffer
	if err := tmpl.Execute(&b, data); err != nil {
		t.Fatalf("render template: %v", err)
	}
	return b.String()
}

// fleetTemplateFuncs covers the sprig functions used by the operator's values
// templates.
func fleetTemplateFuncs() template.FuncMap {
	return template.FuncMap{
		"default": func(def any, given ...any) any {
			if len(given) > 0 && given[0] != nil && given[0] != "" {
				return given[0]
			}
			return def
		},
		"splitList": func(sep, s string) []string { return strings.Split(s, sep) },
		"splitn": func(sep string, n int, s string) map[string]string {
			parts := strings.SplitN(s, sep, n)
			result := make(map[string]string, len(parts))
			for i, part := range parts {
				result[fmt.Sprintf("_%d", i)] = part
			}
			return result
		},
		"trim":  strings.TrimSpace,
		"quote": func(s string) string { return strconv.Quote(s) },
	}
}

package helmop

import (
	"bytes"
	"strings"
	"testing"
	"text/template"

	fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"
	kyaml "sigs.k8s.io/yaml"
)

func sharedTestCluster() Cluster {
	c := testCluster()
	c.P2PDomain = "example.com"
	return c
}

func TestSharedHelmOpNamesAndReleases(t *testing.T) {
	c := sharedTestCluster()
	ops := map[string]struct {
		op   *fleetv1alpha1.HelmOp
		name string
	}{
		"keydb":   {SharedKeyDB(c), "d7y-keydb"},
		"manager": {SharedManager(c), "d7y-manager"},
		"control": {SharedControl(c), "d7y-control"},
		"data":    {SharedData(c), "d7y-data"},
		"extras":  {SharedExtras(c, ExtrasOptions{}), "d7y-extras"},
		"mirror":  {SharedMirror(c), "d7y-containerd-mirror"},
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
		"keydb":   {SharedKeyDB(c), map[string]string{LabelGate: "true"}},
		"manager": {SharedManager(c), map[string]string{LabelGate: "true", LabelRoleManager: "true"}},
		"control": {SharedControl(c), map[string]string{LabelGate: "true", LabelRoleSeed: "true"}},
		"data":    {SharedData(c), map[string]string{LabelGate: "true", LabelRoleSeed: "true"}},
		"extras":  {SharedExtras(c, ExtrasOptions{}), map[string]string{LabelGate: "true", LabelRoleSeed: "true"}},
		"mirror":  {SharedMirror(c), map[string]string{LabelGate: "true", LabelRoleSeed: "true"}},
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
	templateValues := SharedData(c).Spec.Helm.TemplateValues
	for _, key := range []string{"seedClient", "client"} {
		if got := templateValues[key]; !strings.Contains(got, "addrs:") || !strings.Contains(got, ":8002") {
			t.Errorf("%s dynconfig template = %q, want scheduler addrs on :8002", key, got)
		}
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
	if got := valueAt(t, extras, "scheduler", "enabled"); got != false {
		t.Errorf("extras scheduler.enabled = %v, want false (no public scheduler front)", got)
	}
	if got := valueAt(t, extras, "vpa", "targets", "data", "name"); got != "${ .ClusterName }-data-seed-client" {
		t.Errorf("extras data VPA target = %v", got)
	}
	if got := valueAt(t, extras, "ingress", "enabled"); got != SharedManagerIngressEnabledTemplate {
		t.Errorf("extras ingress.enabled = %v, want the manager-label template", got)
	}

	// No other shared component may consume the admission annotation.
	for name, op := range map[string]*fleetv1alpha1.HelmOp{
		"keydb":   SharedKeyDB(c),
		"manager": SharedManager(c),
		"control": SharedControl(c),
		"data":    SharedData(c),
		"extras":  SharedExtras(c, ExtrasOptions{}),
		"mirror":  SharedMirror(c),
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

func TestSharedDynconfigUsesSchedulerIPs(t *testing.T) {
	c := sharedTestCluster()
	templateValues := SharedData(c).Spec.Helm.TemplateValues
	for _, key := range []string{"seedClient", "client"} {
		value := templateValues[key]
		if !strings.Contains(value, "addrs:") || !strings.Contains(value, ":8002") || !strings.Contains(value, "scheduler-ips") {
			t.Errorf("%s dynconfig template = %q, want scheduler-ips addrs on :8002", key, value)
		}
	}
	control := SharedControl(c).Spec.Helm.Values.Data
	if got := valueAt(t, control, "scheduler", "config", "server", "advertiseIP"); got != SharedSchedulerPublicIPTemplate {
		t.Errorf("control advertiseIP = %v, want the scheduler-ips template", got)
	}
}

func TestSharedHelmOpsHaveNoPerClusterLabels(t *testing.T) {
	c := sharedTestCluster()
	for name, op := range map[string]*fleetv1alpha1.HelmOp{
		"keydb":   SharedKeyDB(c),
		"manager": SharedManager(c),
		"control": SharedControl(c),
		"data":    SharedData(c),
		"extras":  SharedExtras(c, ExtrasOptions{}),
		"mirror":  SharedMirror(c),
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
		op.Spec.DownstreamResources[0].Name != ManagerMTLSSecretName ||
		op.Spec.DownstreamResources[1].Kind != "Secret" ||
		op.Spec.DownstreamResources[1].Name != "dragonfly-manager-api" {
		t.Errorf("downstreamResources = %+v, want the fleet-global mTLS and manager credentials Secrets", op.Spec.DownstreamResources)
	}
}

// TestClientSecretHasSingleOwner locks the Fleet copy contract: exactly one
// HelmOp may declare the client identity Secret. A second owner overwrites
// fleet.cattle.io/bundledeployment on the destination and forces an upgrade.
func TestClientSecretHasSingleOwner(t *testing.T) {
	c := sharedTestCluster()
	ops := map[string]*fleetv1alpha1.HelmOp{
		"keydb":     SharedKeyDB(c),
		"manager":   SharedManager(c),
		"control":   SharedControl(c),
		"data":      SharedData(c),
		"extras":    SharedExtras(c, ExtrasOptions{}),
		"mirror":    SharedMirror(c),
		"host-sync": HostSync(c),
	}
	var owners []string
	for name, op := range ops {
		for _, resource := range op.Spec.DownstreamResources {
			if resource.Kind == "Secret" && resource.Name == c.MTLSSecretName {
				owners = append(owners, name)
			}
		}
	}
	if len(owners) != 1 || owners[0] != "control" {
		t.Fatalf("client Secret owners = %v, want [control]", owners)
	}
}

// TestHelmOpDependencyGraphIsAcyclic builds the shared and per-cluster HelmOps
// and verifies that spec.dependsOn edges form no cycle. BundleRef selectors are
// external cert Bundles and are ignored here.
func TestHelmOpDependencyGraphIsAcyclic(t *testing.T) {
	c := sharedTestCluster()
	graph := map[string][]string{}
	for _, op := range []*fleetv1alpha1.HelmOp{
		SharedKeyDB(c), SharedManager(c), SharedControl(c), SharedData(c),
		SharedExtras(c, ExtrasOptions{}), SharedMirror(c), HostSync(c),
		KeyDB(c), Manager(c), Control(c), Data(c),
		Extras(c, ExtrasOptions{IngressEnabled: true}), Mirror(c),
	} {
		graph[op.Name] = nil
		for _, ref := range op.Spec.DependsOn {
			if ref.Name != "" {
				graph[op.Name] = append(graph[op.Name], ref.Name)
			}
		}
	}
	const (
		visiting = iota + 1
		done
	)
	state := map[string]int{}
	var visit func(string) bool
	visit = func(node string) bool {
		switch state[node] {
		case visiting:
			return false
		case done:
			return true
		}
		state[node] = visiting
		for _, next := range graph[node] {
			if _, known := graph[next]; known && !visit(next) {
				return false
			}
		}
		state[node] = done
		return true
	}
	for name := range graph {
		if !visit(name) {
			t.Fatalf("HelmOp dependency graph has a cycle at %s", name)
		}
	}
}

func TestSharedExtrasManagerIngressGate(t *testing.T) {
	if got := valueAt(t, SharedExtras(sharedTestCluster(), ExtrasOptions{}).Spec.Helm.Values.Data, "ingress", "enabled"); got != SharedManagerIngressEnabledTemplate {
		t.Errorf("SharedExtras ingress.enabled = %v, want the manager-label template", got)
	}

	seedOnly := map[string]string{LabelGate: "true", LabelRoleSeed: "true"}
	if got := renderFleetBool(t, SharedManagerIngressEnabledTemplate, seedOnly); got {
		t.Errorf("seed-only labels rendered ingress.enabled = true, want false")
	}

	manager := map[string]string{LabelGate: "true", LabelRoleManager: "true"}
	if got := renderFleetBool(t, SharedManagerIngressEnabledTemplate, manager); !got {
		t.Errorf("manager labels rendered ingress.enabled = false, want true")
	}
}

// renderFleetBool renders a values template through the same YAML-marshal ->
// ${ } text-template -> YAML-unmarshal flow Fleet uses and asserts the result is
// a real bool. A quoted "false" would be a string and truthy in Helm.
func renderFleetBool(t *testing.T, text string, labels map[string]string) bool {
	t.Helper()
	raw, err := kyaml.Marshal(map[string]any{"ingress": map[string]any{"enabled": text}})
	if err != nil {
		t.Fatalf("marshal values: %v", err)
	}
	tmpl, err := template.New("values").Funcs(fleetTemplateFuncs()).Option("missingkey=error").Delims("${", "}").Parse(string(raw))
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}
	var b bytes.Buffer
	if err := tmpl.Execute(&b, map[string]any{
		"ClusterLabels":      labels,
		"ClusterAnnotations": map[string]string{},
	}); err != nil {
		t.Fatalf("render template: %v", err)
	}
	var doc map[string]any
	if err := kyaml.Unmarshal(b.Bytes(), &doc); err != nil {
		t.Fatalf("parse rendered values %q: %v", b.String(), err)
	}
	ingress, _ := doc["ingress"].(map[string]any)
	enabled, ok := ingress["enabled"].(bool)
	if !ok {
		t.Fatalf("ingress.enabled = %T (%v), want a YAML bool (a quoted string is truthy in Helm)", ingress["enabled"], ingress["enabled"])
	}
	return enabled
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
	}
}

func TestSharedSchedulerPublicIPTemplate(t *testing.T) {
	templateSource := "{{ " + strings.TrimSuffix(strings.TrimPrefix(SharedSchedulerPublicIPTemplate, "${ "), " }") + " }}"
	tmpl := template.Must(template.New("public-ip").Funcs(template.FuncMap{
		"splitList": func(sep, value string) []string { return strings.Split(value, sep) },
		"first": func(values []string) string {
			if len(values) == 0 {
				return ""
			}
			return values[0]
		},
		"default": func(fallback, value string) string {
			if value == "" {
				return fallback
			}
			return value
		},
	}).Parse(templateSource))
	for _, tc := range []struct {
		name        string
		annotations map[string]string
		want        string
	}{
		{"missing", map[string]string{}, ""},
		{"empty", map[string]string{"p2p.dragonfly.io/scheduler-ips": ""}, ""},
		{"multiple", map[string]string{"p2p.dragonfly.io/scheduler-ips": "203.0.113.10,203.0.113.11"}, "203.0.113.10"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := tmpl.Execute(&out, map[string]any{"ClusterAnnotations": tc.annotations}); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if got := out.String(); got != tc.want {
				t.Errorf("rendered public IP = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSharedControlPerSeedSchedulerTemplateAndCert(t *testing.T) {
	op := SharedControl(sharedTestCluster())
	if len(op.Spec.DependsOn) != 2 {
		t.Fatalf("dependsOn = %+v, want keydb + scheduler cert selector", op.Spec.DependsOn)
	}
	ref := op.Spec.DependsOn[1]
	if ref.Selector == nil || ref.Selector.MatchLabels[SchedulerCertLabel] != "true" {
		t.Errorf("scheduler dependency = %+v, want scheduler cert label selector", ref)
	}
	if op.Labels["p2p-dragonfly-io"] != "control" {
		t.Errorf("control component label = %q, want control (never p2p-mtls)", op.Labels["p2p-dragonfly-io"])
	}
	if op.Labels["p2p-dragonfly-io"] == "p2p-mtls" {
		t.Error("control HelmOp carries the cert Bundle selector label and can self-match")
	}
	template := op.Spec.Helm.TemplateValues["scheduler"]
	for _, want := range []string{AnnotationMTLSCertRevision, "index .ClusterAnnotations", "scheduler-mtls", "client-mtls", ClientCertDir} {
		if !strings.Contains(template, want) {
			t.Errorf("scheduler template missing %q: %q", want, template)
		}
	}
	values := op.Spec.Helm.Values.Data
	volumes := valueAt(t, values, "scheduler", "extraVolumes").([]any)
	if len(volumes) != 3 {
		t.Errorf("scheduler extraVolumes = %v, want scheduler + client + logs", volumes)
	}
	if got := valueAt(t, values, "scheduler", "config", "manager", "tls", "caCert"); got != ClientCertDir+"/ca.crt" {
		t.Errorf("manager client TLS = %v", got)
	}
	if got := valueAt(t, values, "scheduler", "config", "server", "tls", "caCert"); got != CertDir+"/ca.crt" {
		t.Errorf("scheduler server TLS = %v", got)
	}
	if len(op.Spec.DownstreamResources) != 1 || op.Spec.DownstreamResources[0].Name != "dragonfly-p2p-mtls" {
		t.Errorf("control downstreamResources = %+v, want client Secret only", op.Spec.DownstreamResources)
	}
}

func TestSharedDataUsesClientRevision(t *testing.T) {
	op := SharedData(sharedTestCluster())
	if got := valueAt(t, op.Spec.Helm.Values.Data, "client", "podAnnotations", certRevisionAnnotation); got != "rev1" {
		t.Errorf("client revision = %v", got)
	}
	if got := valueAt(t, op.Spec.Helm.Values.Data, "seedClient", "podAnnotations", certRevisionAnnotation); got != "rev1" {
		t.Errorf("seedClient revision = %v", got)
	}
	if len(op.Spec.DownstreamResources) != 0 {
		t.Errorf("data downstreamResources = %+v, want none: control is the sole client Secret owner", op.Spec.DownstreamResources)
	}
}

func TestSharedSchedulerCertTemplateRendersRevision(t *testing.T) {
	template := SharedControl(sharedTestCluster()).Spec.Helm.TemplateValues["scheduler"]
	rendered, err := renderFleetTemplate(t, template, map[string]string{AnnotationMTLSCertRevision: "sched-42"})
	if err != nil {
		t.Fatalf("render scheduler template: %v", err)
	}
	var scheduler map[string]any
	if err := kyaml.Unmarshal(rendered, &scheduler); err != nil {
		t.Fatalf("rendered scheduler values are not YAML: %v\n%s", err, rendered)
	}
	if got := valueAt(t, scheduler, "podAnnotations", AnnotationMTLSCertRevision); got != "sched-42" {
		t.Errorf("rendered scheduler revision = %v, want sched-42", got)
	}
	if got := valueAt(t, scheduler, "podAnnotations", certRevisionAnnotation); got != "rev1" {
		t.Errorf("rendered client revision = %v, want rev1", got)
	}
}

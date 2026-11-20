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
		op.Spec.DownstreamResources[0].Name != "dragonfly-p2p-mtls" ||
		op.Spec.DownstreamResources[1].Kind != "Secret" ||
		op.Spec.DownstreamResources[1].Name != "dragonfly-manager-api" {
		t.Errorf("downstreamResources = %+v, want the fleet-global mTLS and manager credentials Secrets", op.Spec.DownstreamResources)
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

package helmop

import "testing"

func TestAdmissionHelmOp(t *testing.T) {
	c := testCluster()
	c.AdmissionChart = Chart{Repo: "https://charts.example.com", Name: "admission-webhook", Version: "0.1.0"}
	c.ProxyDomain = "proxy.example.com"
	c.AdmissionEnabled = true
	c.AdmissionImagePullSecret = "proxy-pull"
	c.AdmissionServiceAccount = "proxy-sa"

	op := Admission(c)

	if op.Name != "c1-admission-webhook" {
		t.Errorf("name = %s", op.Name)
	}
	if op.Spec.TargetNamespace != "" {
		t.Errorf("target namespace = %q, want empty (cluster-scoped webhook)", op.Spec.TargetNamespace)
	}
	if op.Spec.DefaultNamespace != TargetNamespace {
		t.Errorf("default namespace = %q, want %q", op.Spec.DefaultNamespace, TargetNamespace)
	}
	if op.Spec.Helm.Chart != "admission-webhook" || op.Spec.Helm.Version != "0.1.0" {
		t.Errorf("chart = %s %s", op.Spec.Helm.Chart, op.Spec.Helm.Version)
	}
	if op.Labels["p2p-dragonfly-io"] != "admission-webhook" {
		t.Errorf("component label = %q", op.Labels["p2p-dragonfly-io"])
	}
	if len(op.Spec.DependsOn) != 0 {
		t.Errorf("dependsOn = %+v, want none (independent toggle)", op.Spec.DependsOn)
	}
	if len(op.Spec.DownstreamResources) != 0 {
		t.Errorf("downstreamResources = %+v, want none (no secret values)", op.Spec.DownstreamResources)
	}

	values := op.Spec.Helm.Values.Data
	if got := valueAt(t, values, "proxy", "domain"); got != "proxy.example.com" {
		t.Errorf("proxy.domain = %v", got)
	}
	if got := valueAt(t, values, "inject", "imagePullSecret"); got != "proxy-pull" {
		t.Errorf("inject.imagePullSecret = %v", got)
	}
	if got := valueAt(t, values, "inject", "serviceAccountName"); got != "proxy-sa" {
		t.Errorf("inject.serviceAccountName = %v", got)
	}
	if got := valueAt(t, values, "networkOnly"); got != false {
		t.Errorf("networkOnly = %v", got)
	}
}

func TestAdmissionHelmOpIndependentOfHarborAndProxy(t *testing.T) {
	c := testCluster()
	c.AdmissionChart = Chart{Repo: "https://charts.example.com", Name: "admission-webhook", Version: "0.1.0"}
	c.ProxyDomain = "proxy.example.com"
	c.HarborManaged = false
	c.ProxyEnabled = false
	c.AdmissionEnabled = true

	op := Admission(c)
	if len(op.Spec.DependsOn) != 0 {
		t.Errorf("dependsOn = %+v, want none", op.Spec.DependsOn)
	}
	if containsKey(op.Spec.Helm.Values.Data, "harbor") {
		t.Error("admission values contain Harbor state")
	}
}

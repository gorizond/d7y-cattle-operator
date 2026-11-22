package helmop

import "testing"

func TestSchedulerCertHelmOp(t *testing.T) {
	c := testCluster()
	c.SchedulerCertChart = Chart{Repo: "https://charts.example.com", Name: "p2p-scheduler-cert", Version: "0.1.0"}
	op := SchedulerCert(c, "crt", "key", "ca")
	if op.Name != "c1-p2p-scheduler-cert" {
		t.Errorf("name = %q", op.Name)
	}
	if op.Spec.TargetNamespace != TargetNamespace || len(op.Spec.Targets) != 1 || op.Spec.Targets[0].ClusterName != "c1" {
		t.Errorf("target = %+v", op.Spec)
	}
	if op.Spec.Helm.Chart != "p2p-scheduler-cert" || op.Spec.Helm.Version != "0.1.0" {
		t.Errorf("chart = %s@%s", op.Spec.Helm.Chart, op.Spec.Helm.Version)
	}
	if !op.Spec.Helm.TakeOwnership {
		t.Error("scheduler certificate HelmOp must take ownership of the Secret")
	}
	values := op.Spec.Helm.Values.Data
	if got := valueAt(t, values, "tls", "crt"); got != "crt" {
		t.Errorf("tls.crt = %v", got)
	}
	if got := valueAt(t, values, "tls", "key"); got != "key" {
		t.Errorf("tls.key = %v", got)
	}
	if got := valueAt(t, values, "tls", "ca"); got != "ca" {
		t.Errorf("tls.ca = %v", got)
	}
	if len(op.Spec.DependsOn) != 0 || len(op.Spec.DownstreamResources) != 0 {
		t.Errorf("unexpected dependencies/downstream resources: %+v / %+v", op.Spec.DependsOn, op.Spec.DownstreamResources)
	}
	if got := op.Spec.Labels["p2p-dragonfly-io"]; got != "p2p-mtls" {
		t.Errorf("bundle watch label = %q", got)
	}
	if got := op.Spec.Labels[SchedulerCertLabel]; got != "true" {
		t.Errorf("scheduler cert selector label = %q", got)
	}
	if got := op.Spec.Labels["p2p.dragonfly.io/cluster-name"]; got != "c1" {
		t.Errorf("bundle cluster-name label = %q", got)
	}
	if op.Labels["p2p-dragonfly-io"] == "p2p-mtls" {
		t.Error("HelmOp metadata must not carry the bundle selector label")
	}
}

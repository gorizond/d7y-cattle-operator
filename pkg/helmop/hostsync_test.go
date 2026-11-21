package helmop

import "testing"

func TestHostSyncHelmOp(t *testing.T) {
	c := testCluster()
	c.SchedulerIPs = "203.0.113.10, 203.0.113.11,203.0.113.10"
	op := HostSync(c)
	if op.Name != "c1-p2p-host-sync" || op.Spec.TargetNamespace != TargetNamespace {
		t.Errorf("name/namespace = %s/%s", op.Name, op.Spec.TargetNamespace)
	}
	values := op.Spec.Helm.Values.Data
	ips, _ := values["schedulerIPs"].([]string)
	if len(ips) != 2 || ips[0] != "203.0.113.10:8002" || ips[1] != "203.0.113.11:8002" {
		t.Errorf("schedulerIPs = %v", values["schedulerIPs"])
	}
	if got := valueAt(t, values, "mtlsSecret"); got != "dragonfly-p2p-mtls" {
		t.Errorf("mtlsSecret = %v", got)
	}
	if len(op.Spec.DependsOn) != 1 || op.Spec.DependsOn[0].Name != SharedControlName() {
		t.Errorf("dependsOn = %+v, want the shared control Bundle", op.Spec.DependsOn)
	}
	if len(op.Spec.DownstreamResources) != 0 {
		t.Errorf("downstreamResources = %+v, want none: control is the sole client Secret owner", op.Spec.DownstreamResources)
	}
	if op.Spec.Helm.Chart != "p2p-host-sync" || op.Spec.Helm.Version != "0.1.0" {
		t.Errorf("chart = %s/%s", op.Spec.Helm.Chart, op.Spec.Helm.Version)
	}
}

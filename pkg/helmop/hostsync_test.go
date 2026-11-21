package helmop

import "testing"

func TestHostSyncHelmOp(t *testing.T) {
	c := testCluster()
	c.SchedulerIPs = "203.0.113.10, 203.0.113.11,203.0.113.10"
	c.StaticURL = "https://static.example.test"
	c.StaticClientVersion = "v1.5.14"
	c.StaticNydusVersion = "v2.4.5"
	c.StaticSnapshotterVersion = "v0.16.0"
	c.StaticClientSHA256AMD64 = "client-amd64"
	c.StaticNydusSHA256AMD64 = "nydus-amd64"
	c.StaticSnapshotterSHA256AMD64 = "snapshotter-amd64"
	c.StaticSnapshotterBinarySHA256AMD64 = "snapshotter-bin-amd64"
	c.StaticSnapshotterBinarySHA256ARM64 = "snapshotter-bin-arm64"
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
	if got := valueAt(t, values, "staticURL"); got != "https://static.example.test" {
		t.Errorf("staticURL = %v", got)
	}
	if got := valueAt(t, values, "versions", "client"); got != "v1.5.14" {
		t.Errorf("client version = %v", got)
	}
	if got := valueAt(t, values, "versions", "nydus"); got != "v2.4.5" {
		t.Errorf("nydus version = %v", got)
	}
	if got := valueAt(t, values, "versions", "snapshotter"); got != "v0.16.0" {
		t.Errorf("snapshotter version = %v", got)
	}
	if got := valueAt(t, values, "checksums", "snapshotterBinary", "amd64"); got != "snapshotter-bin-amd64" {
		t.Errorf("snapshotter binary amd64 checksum = %v", got)
	}
	if got := valueAt(t, values, "checksums", "snapshotterBinary", "arm64"); got != "snapshotter-bin-arm64" {
		t.Errorf("snapshotter binary arm64 checksum = %v", got)
	}
	if op.Spec.Helm.Chart != "p2p-host-sync" || op.Spec.Helm.Version != "0.1.0" {
		t.Errorf("chart = %s/%s", op.Spec.Helm.Chart, op.Spec.Helm.Version)
	}
}

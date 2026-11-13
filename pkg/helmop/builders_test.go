package helmop

import "testing"

func TestKeyDBHelmOp(t *testing.T) {
	op := KeyDB(testCluster())

	if op.Name != "c1-keydb" || op.Namespace != "fleet-default" {
		t.Errorf("name/namespace = %s/%s", op.Name, op.Namespace)
	}
	if op.Labels["p2p-dragonfly-io"] != "keydb" {
		t.Errorf("component label = %q", op.Labels["p2p-dragonfly-io"])
	}
	if len(op.Spec.DependsOn) != 0 {
		t.Errorf("dependsOn = %+v, want none", op.Spec.DependsOn)
	}
	if len(op.Spec.DownstreamResources) != 0 {
		t.Errorf("downstreamResources = %+v, want none", op.Spec.DownstreamResources)
	}
	if op.Spec.Helm.Chart != KeyDBChartName || op.Spec.Helm.Version != KeyDBChartVersion {
		t.Errorf("chart = %s %s", op.Spec.Helm.Chart, op.Spec.Helm.Version)
	}

	values := op.Spec.Helm.Values.Data
	if got := valueAt(t, values, "architecture"); got != "replication" {
		t.Errorf("architecture = %v", got)
	}
	if got := valueAt(t, values, "replica", "replicaCount"); got != 2 {
		t.Errorf("replica.replicaCount = %v", got)
	}
	if got := valueAt(t, values, "replica", "persistence", "enabled"); got != false {
		t.Errorf("replica.persistence.enabled = %v", got)
	}
	if got := valueAt(t, values, "master", "persistence", "enabled"); got != false {
		t.Errorf("master.persistence.enabled = %v", got)
	}
}

func TestNydusHelmOp(t *testing.T) {
	op := Nydus(testCluster())

	if op.Name != "c1-nydus-snapshotter" {
		t.Errorf("name = %q", op.Name)
	}
	if len(op.Spec.DependsOn) != 1 || op.Spec.DependsOn[0].Name != "c1-keydb" {
		t.Errorf("dependsOn = %+v", op.Spec.DependsOn)
	}
	if op.Spec.Helm.Chart != NydusChartName || op.Spec.Helm.Version != NydusChartVersion {
		t.Errorf("chart = %s %s", op.Spec.Helm.Chart, op.Spec.Helm.Version)
	}

	values := op.Spec.Helm.Values.Data
	if got := valueAt(t, values, "priorityClassName"); got != "system-node-critical" {
		t.Errorf("priorityClassName = %v", got)
	}
	if got := valueAt(t, values, "runtimeClass"); got != "nydus" {
		t.Errorf("runtimeClass = %v", got)
	}
	if got := valueAt(t, values, "hostSocket"); got != "/run/k3s/containerd/containerd.sock" {
		t.Errorf("hostSocket = %v", got)
	}
	if got := valueAt(t, values, "initContainer", "privileged"); got != true {
		t.Errorf("initContainer.privileged = %v", got)
	}
	if got := valueAt(t, values, "containerRuntime", "containerd", "configFile"); got != "/var/lib/rancher/rke2/agent/etc/containerd/config.toml.tmpl" {
		t.Errorf("containerd configFile = %v", got)
	}
}

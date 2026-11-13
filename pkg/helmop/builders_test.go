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

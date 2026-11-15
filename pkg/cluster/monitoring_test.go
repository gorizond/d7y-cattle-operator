package cluster

import "testing"

func TestHelmopClusterMonitoring(t *testing.T) {
	controller, _, _ := newTestController(t)
	cluster := testSeedCluster("42")
	cluster.Labels["monitoring"] = "enabled"
	cluster.Annotations[AnnotationMonitoring] = "monitoring=enabled"

	if hc := controller.helmopCluster(cluster); !hc.Monitoring {
		t.Error("helmopCluster().Monitoring = false, want true")
	}

	delete(cluster.Labels, "monitoring")
	if hc := controller.helmopCluster(cluster); hc.Monitoring {
		t.Error("helmopCluster().Monitoring = true without a matching label")
	}
}

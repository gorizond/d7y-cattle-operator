package cluster

import (
	"testing"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestHelmopClusterP2PDataTLS(t *testing.T) {
	controller := &Controller{}
	cluster := &provv1.Cluster{ObjectMeta: metav1.ObjectMeta{
		Annotations: map[string]string{},
	}}

	if controller.helmopCluster(cluster).P2PDataTLS {
		t.Error("helmopCluster().P2PDataTLS = true by default, want false")
	}

	cluster.Annotations[AnnotationP2PDataTLS] = "enabled"
	if !controller.helmopCluster(cluster).P2PDataTLS {
		t.Error("helmopCluster().P2PDataTLS = false with enabled annotation, want true")
	}

	cluster.Annotations[AnnotationP2PDataTLS] = "false"
	if controller.helmopCluster(cluster).P2PDataTLS {
		t.Error("helmopCluster().P2PDataTLS = true with false annotation, want false")
	}
}

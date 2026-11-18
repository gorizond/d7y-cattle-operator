package helmop

import (
	"testing"

	fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"
)

func TestMirrorHelmOpHasNoProxyValues(t *testing.T) {
	c := testCluster()
	c.ProxyEnabled = true
	c.ProxyPublicURL = "https://proxy.example.com/proxy"
	c.ProxyRegistries = []string{"docker.io", "registry.gitlab.com", "ghcr.io"}
	op := Mirror(c)

	if got := dependsOnNames(op); len(got) != 1 || got[0] != "c1-data" {
		t.Errorf("dependsOn = %v, want data only", got)
	}
	values := op.Spec.Helm.Values.Data
	for _, key := range []string{"proxy", "registries", "docker.io", "index.docker.io"} {
		if containsKey(values, key) {
			t.Errorf("mirror values contain %q; chart hosts files are static", key)
		}
	}
}

func dependsOnNames(op *fleetv1alpha1.HelmOp) []string {
	names := make([]string, 0, len(op.Spec.DependsOn))
	for _, item := range op.Spec.DependsOn {
		names = append(names, item.Name)
	}
	return names
}

func TestMirrorHelmOp(t *testing.T) {
	op := Mirror(testCluster())

	if op.Name != "c1-containerd-mirror" || op.Namespace != "fleet-default" {
		t.Errorf("name/namespace = %s/%s", op.Name, op.Namespace)
	}
	if op.Spec.TargetNamespace != MirrorTargetNamespace {
		t.Errorf("target namespace = %q, want %q", op.Spec.TargetNamespace, MirrorTargetNamespace)
	}
	if op.Labels["p2p-dragonfly-io"] != "containerd-mirror" {
		t.Errorf("component label = %q", op.Labels["p2p-dragonfly-io"])
	}
	if len(op.Spec.DependsOn) != 1 || op.Spec.DependsOn[0].Name != "c1-data" {
		t.Errorf("dependsOn = %+v, want c1-data", op.Spec.DependsOn)
	}
	if len(op.Spec.DownstreamResources) != 0 {
		t.Errorf("downstreamResources = %+v, want none", op.Spec.DownstreamResources)
	}
	helm := op.Spec.Helm
	if helm.Chart != "containerd-mirror" || helm.Repo != "https://charts.example.com" || helm.Version != "0.1.0" {
		t.Errorf("chart/repo/version = %s/%s/%s", helm.Chart, helm.Repo, helm.Version)
	}

	values := helm.Values.Data
	if got := valueAt(t, values, "namespace"); got != MirrorTargetNamespace {
		t.Errorf("namespace = %v, want %s", got, MirrorTargetNamespace)
	}
	if got := valueAt(t, values, "port"); got != MirrorPort {
		t.Errorf("port = %v, want %d", got, MirrorPort)
	}
	if got := valueAt(t, values, "hostPath"); got != MirrorCertsDir {
		t.Errorf("hostPath = %v, want %s", got, MirrorCertsDir)
	}
	if containsKey(values, "nginx") {
		t.Error("mirror values contain nginx")
	}
	if containsKey(values, "persistentVolumeClaim") || len(truePersistence(values)) > 0 {
		t.Error("mirror values request persistence")
	}
}

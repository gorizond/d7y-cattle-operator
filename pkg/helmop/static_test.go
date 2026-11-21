package helmop

import (
	"reflect"
	"testing"
)

func staticTestCluster() Cluster {
	c := sharedTestCluster()
	c.StaticChart = Chart{Repo: "https://charts.example.com", Name: "p2p-static", Version: "0.1.11"}
	c.StaticReleaseName = "p2p-static"
	c.StaticClientVersion = "v1.5.14"
	c.StaticNydusVersion = "v2.4.5"
	c.StaticSnapshotterVersion = "v0.16.0"
	c.StaticClientRepo = "dragonflyoss/client"
	c.StaticNydusRepo = "dragonflyoss/nydus"
	c.StaticSnapshotterRepo = "containerd/nydus-snapshotter"
	c.StaticClientSHA256AMD64 = "ba080e62f1626ab2fc2a5a0629a5cd912cc15099508820403108abbe2f75b1f7"
	c.StaticClientSHA256ARM64 = "370ff792a1c6957a7244b770d6cd12792668e326b2282b3017fea2cbfa3510af"
	c.StaticNydusSHA256AMD64 = "1ad7b793a1996577e462b0bf5c1a22e4450b9c04f7e9e99d4b274ccc92f19072"
	c.StaticNydusSHA256ARM64 = "85925af702d79fc3a8b9d501a5267ddc9f81449ff9fe94841845eb98eda84593"
	c.StaticSnapshotterSHA256AMD64 = "24e4ceaae9c87e396e71e89bd3eb074f2cc6a5a14b1a2081c8d890481857a0e4"
	c.StaticSnapshotterSHA256ARM64 = "136e9c649a63c16b3d3322a15fcdf918e7dda9a4cbb043e2923451e87a8ba08f"
	c.StaticManagerHost = "manager-api.example.com"
	c.StaticTLSSecret = "manager-tls"
	c.StaticIngressClassName = "traefik"
	return c
}

func TestSharedStaticRendersConfigVersions(t *testing.T) {
	c := staticTestCluster()
	op := SharedStatic(c)
	if op.Name != "p2p-static" || op.Spec.Helm.ReleaseName != "p2p-static" {
		t.Errorf("name/release = %s/%s", op.Name, op.Spec.Helm.ReleaseName)
	}
	if !op.Spec.Helm.TakeOwnership {
		t.Error("takeOwnership = false, want true for adoption")
	}
	if len(op.Spec.Targets) != 1 || op.Spec.Targets[0].ClusterSelector == nil {
		t.Fatalf("targets = %+v", op.Spec.Targets)
	}
	if op.Spec.Targets[0].ClusterSelector.MatchLabels[LabelRoleManager] != "true" {
		t.Errorf("selector = %v, want manager", op.Spec.Targets[0].ClusterSelector.MatchLabels)
	}

	values := op.Spec.Helm.Values.Data
	if got := valueAt(t, values, "manager", "host"); got != "manager-api.example.com" {
		t.Errorf("manager.host = %v", got)
	}
	if got := valueAt(t, values, "manager", "tlsSecret"); got != "manager-tls" {
		t.Errorf("manager.tlsSecret = %v", got)
	}
	if got := valueAt(t, values, "static", "populate", "client", "compatVersion"); got != "v1.5.14" {
		t.Errorf("client compatVersion = %v", got)
	}
	if got := valueAt(t, values, "static", "populate", "nydus", "version"); got != "v2.4.5" {
		t.Errorf("nydus version = %v", got)
	}
	if got := valueAt(t, values, "static", "populate", "snapshotter", "version"); got != "v0.16.0" {
		t.Errorf("snapshotter version = %v", got)
	}
	client := valueAt(t, values, "static", "populate", "client", "versions").([]any)[0].(map[string]any)
	arches := client["arches"].([]any)
	if len(arches) != 2 {
		t.Fatalf("client arches = %v", arches)
	}
	amd64 := arches[0].(map[string]any)
	if amd64["arch"] != "amd64" || amd64["sha256"] != c.StaticClientSHA256AMD64 {
		t.Errorf("amd64 client = %+v", amd64)
	}
	arm64 := arches[1].(map[string]any)
	if arm64["arch"] != "arm64" || arm64["sha256"] != c.StaticClientSHA256ARM64 {
		t.Errorf("arm64 client = %+v", arm64)
	}
}

func TestSharedStaticIsStable(t *testing.T) {
	c := staticTestCluster()
	first := SharedStatic(c)
	second := SharedStatic(c)
	if !reflect.DeepEqual(first.Spec.Helm.Values.Data, second.Spec.Helm.Values.Data) {
		t.Error("SharedStatic values changed between identical calls")
	}
	if first.Spec.Targets[0].ClusterSelector.String() != second.Spec.Targets[0].ClusterSelector.String() {
		t.Error("SharedStatic selector changed between identical calls")
	}
}

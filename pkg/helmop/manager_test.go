package helmop

import "testing"

func testCluster() Cluster {
	return Cluster{
		Name:           "c1",
		Namespace:      "fleet-default",
		MTLSSecretName: "dragonfly-p2p-mtls",
		Chart:          Chart{Repo: "https://charts.example.com", Name: "dragonfly", Version: "1.10.1"},
		Labels:         map[string]string{"app.kubernetes.io/managed-by": "d7y-cattle-operator"},
	}
}

func valueAt(t *testing.T, value any, keys ...string) any {
	t.Helper()
	current := value
	for _, key := range keys {
		m, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("value at %v is %T, want map[string]any", keys, current)
		}
		current, ok = m[key]
		if !ok {
			t.Fatalf("missing key %q in %v", key, keys)
		}
	}
	return current
}

func nested(t *testing.T, value any, keys ...string) map[string]any {
	t.Helper()
	result, ok := valueAt(t, value, keys...).(map[string]any)
	if !ok {
		t.Fatalf("value at %v is not a map", keys)
	}
	return result
}

func TestManagerHelmOp(t *testing.T) {
	op := Manager(testCluster())

	if op.APIVersion != APIVersion || op.Kind != Kind {
		t.Errorf("apiVersion/kind = %s/%s", op.APIVersion, op.Kind)
	}
	if op.Name != "c1-manager" || op.Namespace != "fleet-default" {
		t.Errorf("name/namespace = %s/%s", op.Name, op.Namespace)
	}
	if op.Labels["p2p-dragonfly-io"] != "manager" {
		t.Errorf("component label = %q", op.Labels["p2p-dragonfly-io"])
	}
	if op.Spec.TargetNamespace != TargetNamespace {
		t.Errorf("target namespace = %q", op.Spec.TargetNamespace)
	}
	if len(op.Spec.Targets) != 1 || op.Spec.Targets[0].ClusterName != "c1" {
		t.Errorf("targets = %+v", op.Spec.Targets)
	}
	if len(op.Spec.DependsOn) != 1 || op.Spec.DependsOn[0].Name != "c1-keydb" {
		t.Errorf("dependsOn = %+v", op.Spec.DependsOn)
	}
	if len(op.Spec.DownstreamResources) != 1 ||
		op.Spec.DownstreamResources[0].Kind != "Secret" ||
		op.Spec.DownstreamResources[0].Name != "dragonfly-p2p-mtls" {
		t.Errorf("downstreamResources = %+v", op.Spec.DownstreamResources)
	}

	helm := op.Spec.Helm
	if helm == nil {
		t.Fatal("spec.helm is nil")
	}
	if helm.Repo != "https://charts.example.com" || helm.Chart != "dragonfly" || helm.Version != "1.10.1" {
		t.Errorf("chart = %s/%s/%s", helm.Repo, helm.Chart, helm.Version)
	}
	if helm.ReleaseName != "c1-manager" {
		t.Errorf("releaseName = %q", helm.ReleaseName)
	}

	values := helm.Values.Data
	if got := valueAt(t, values, "fullnameOverride"); got != "c1" {
		t.Errorf("fullnameOverride = %v", got)
	}
	if got := valueAt(t, values, "manager", "enable"); got != true {
		t.Errorf("manager.enable = %v", got)
	}
	if got := valueAt(t, values, "mysql", "enable"); got != true {
		t.Errorf("mysql.enable = %v", got)
	}
	if got := valueAt(t, values, "mysql", "persistence", "enabled"); got != false {
		t.Errorf("mysql.persistence.enabled = %v", got)
	}
	if got := valueAt(t, values, "redis", "enable"); got != false {
		t.Errorf("redis.enable = %v", got)
	}
	if got := valueAt(t, values, "externalRedis", "host"); got != "c1-keydb" {
		t.Errorf("externalRedis.host = %v", got)
	}
	for _, component := range []string{"scheduler", "seedClient", "client"} {
		if got := valueAt(t, values, component, "enable"); got != false {
			t.Errorf("%s.enable = %v", component, got)
		}
	}

	grpcTLS := nested(t, values, "manager", "config", "server", "grpc", "tls")
	if grpcTLS["caCert"] != CertDir+"/ca.crt" || grpcTLS["cert"] != CertDir+"/tls.crt" || grpcTLS["key"] != CertDir+"/tls.key" {
		t.Errorf("gRPC TLS = %v", grpcTLS)
	}
	restTLS := nested(t, values, "manager", "config", "server", "rest", "tls")
	if restTLS["cert"] != CertDir+"/tls.crt" || restTLS["key"] != CertDir+"/tls.key" {
		t.Errorf("REST TLS = %v", restTLS)
	}

	volumes := valueAt(t, values, "manager", "extraVolumes").([]any)
	foundMTLS := false
	for _, volume := range volumes {
		volumeMap, ok := volume.(map[string]any)
		if !ok || volumeMap["name"] != "mtls" {
			continue
		}
		secret := nested(t, volumeMap, "secret")
		if secret["secretName"] != "dragonfly-p2p-mtls" {
			t.Errorf("mTLS volume secret = %v", secret)
		}
		foundMTLS = true
	}
	if !foundMTLS {
		t.Error("manager values have no mTLS volume")
	}
}

package helmop

import "testing"

func testCluster() Cluster {
	return Cluster{
		Name:               "c1",
		Namespace:          "fleet-default",
		MTLSSecretName:     "dragonfly-p2p-mtls",
		Chart:              Chart{Repo: "https://charts.example.com", Name: "dragonfly", Version: "1.10.1"},
		ExtrasChart:        Chart{Repo: "https://charts.example.com", Name: "dragonfly-extras", Version: "0.1.2"},
		ManagerGRPCDomain:  "grpc.example.com",
		ManagerAPIHost:     "api.example.com",
		SchedulerClusterID: 42,
		SchedulerReplicas:  ReplicasTemplate("p2p.dragonfly.io/scheduler-replicas"),
		SeedClientReplicas: ReplicasTemplate("p2p.dragonfly.io/seedclient-replicas"),
		Labels:             map[string]string{"app.kubernetes.io/managed-by": "d7y-cattle-operator"},
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

func managerVolume(t *testing.T, values any, name string) map[string]any {
	t.Helper()
	for _, volume := range valueAt(t, values, "manager", "extraVolumes").([]any) {
		volumeMap, ok := volume.(map[string]any)
		if ok && volumeMap["name"] == name {
			return volumeMap
		}
	}
	return nil
}

func managerVolumeMount(t *testing.T, values any, name string) map[string]any {
	t.Helper()
	for _, mount := range valueAt(t, values, "manager", "extraVolumeMounts").([]any) {
		mountMap, ok := mount.(map[string]any)
		if ok && mountMap["name"] == name {
			return mountMap
		}
	}
	return nil
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
	if got := valueAt(t, values, "mysql", "primary", "persistence", "enabled"); got != false {
		t.Errorf("mysql.primary.persistence.enabled = %v", got)
	}
	if mysql := nested(t, values, "mysql"); mysql["persistence"] != nil {
		t.Errorf("mysql.persistence = %v, want unset (Bitnami MySQL expects mysql.primary.persistence)", mysql["persistence"])
	}
	if got := valueAt(t, values, "redis", "enable"); got != false {
		t.Errorf("redis.enable = %v", got)
	}
	addrs, ok := valueAt(t, values, "externalRedis", "addrs").([]any)
	if !ok || len(addrs) != 1 || addrs[0] != "c1-keydb.dragonfly-system.svc.cluster.local:6379" {
		t.Errorf("externalRedis.addrs = %v, want [c1-keydb.dragonfly-system.svc.cluster.local:6379]", addrs)
	}
	if externalRedis := nested(t, values, "externalRedis"); externalRedis["host"] != nil {
		t.Errorf("externalRedis.host = %v, want unset (Dragonfly chart requires externalRedis.addrs)", externalRedis["host"])
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
	if managerVolume(t, values, "public-tls") != nil {
		t.Error("manager values have a public-tls volume without ManagerPublicTLSSecret")
	}
	if managerVolumeMount(t, values, "public-tls") != nil {
		t.Error("manager values have a public-tls mount without ManagerPublicTLSSecret")
	}
}

func TestManagerPublicTLSSecret(t *testing.T) {
	cluster := testCluster()
	cluster.ManagerPublicTLSSecret = "poligon-manager-public-tls"
	values := Manager(cluster).Spec.Helm.Values.Data

	grpcTLS := nested(t, values, "manager", "config", "server", "grpc", "tls")
	if grpcTLS["caCert"] != CertDir+"/ca.crt" {
		t.Errorf("gRPC caCert = %v, want the internal client CA", grpcTLS["caCert"])
	}
	if grpcTLS["cert"] != PublicTLSDir+"/tls.crt" || grpcTLS["key"] != PublicTLSDir+"/tls.key" {
		t.Errorf("gRPC TLS = %v, want public certificate paths", grpcTLS)
	}

	restTLS := nested(t, values, "manager", "config", "server", "rest", "tls")
	if restTLS["cert"] != CertDir+"/tls.crt" || restTLS["key"] != CertDir+"/tls.key" {
		t.Errorf("REST TLS = %v, want internal certificate paths", restTLS)
	}

	volume := managerVolume(t, values, "public-tls")
	if volume == nil {
		t.Fatal("manager values have no public-tls volume")
	}
	secret := nested(t, volume, "secret")
	if secret["secretName"] != "poligon-manager-public-tls" {
		t.Errorf("public-tls volume secret = %v", secret)
	}
	mount := managerVolumeMount(t, values, "public-tls")
	if mount == nil {
		t.Fatal("manager values have no public-tls mount")
	}
	if mount["mountPath"] != PublicTLSDir || mount["readOnly"] != true {
		t.Errorf("public-tls mount = %v, want %s readOnly", mount, PublicTLSDir)
	}
	if managerVolume(t, values, "mtls") == nil {
		t.Error("manager values have no mTLS volume")
	}
}

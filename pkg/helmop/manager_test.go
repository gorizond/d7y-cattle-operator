package helmop

import (
	"testing"

	fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"
)

func testCluster() Cluster {
	return Cluster{
		Name:                        "c1",
		Namespace:                   "fleet-default",
		MTLSSecretName:              "dragonfly-p2p-mtls",
		Chart:                       Chart{Repo: "https://charts.example.com", Name: "dragonfly", Version: "1.10.1-patch.2"},
		ExtrasChart:                 Chart{Repo: "https://charts.example.com", Name: "dragonfly-extras", Version: "0.1.10"},
		MirrorChart:                 Chart{Repo: "https://charts.example.com", Name: "containerd-mirror", Version: "0.1.0"},
		HostSyncChart:               Chart{Repo: "https://charts.example.com", Name: "p2p-host-sync", Version: "0.1.0"},
		ManagerGRPCDomain:           "grpc.example.com",
		ManagerGRPCPort:             ManagerGRPCEdgePort,
		ManagerAPIHost:              "api.example.com",
		ManagerAPICredentialsSecret: "dragonfly-manager-api",
		SchedulerClusterID:          42,
		SchedulerReplicas:           ReplicasTemplate("p2p.dragonfly.io/scheduler-replicas"),
		SeedClientReplicas:          ReplicasTemplate("p2p.dragonfly.io/seedclient-replicas"),
		MTLSCertRevision:            "rev1",
		ManagerCertRevision:         "mgr1",
		SchedulerCertRevision:       "sched1",
		Labels:                      map[string]string{"app.kubernetes.io/managed-by": "d7y-cattle-operator"},
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
	if len(op.Spec.DownstreamResources) != 2 ||
		op.Spec.DownstreamResources[0].Kind != "Secret" ||
		op.Spec.DownstreamResources[0].Name != ManagerMTLSSecretName ||
		op.Spec.DownstreamResources[1].Kind != "Secret" ||
		op.Spec.DownstreamResources[1].Name != "dragonfly-manager-api" {
		t.Errorf("downstreamResources = %+v", op.Spec.DownstreamResources)
	}

	helm := op.Spec.Helm
	if helm == nil {
		t.Fatal("spec.helm is nil")
	}
	if helm.Repo != "https://charts.example.com" || helm.Chart != "dragonfly" || helm.Version != "1.10.1-patch.2" {
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
	envVars, ok := valueAt(t, values, "manager", "extraEnvVars").([]any)
	if !ok || len(envVars) != 2 {
		t.Fatalf("manager.extraEnvVars = %v, want two entries", valueAt(t, values, "manager", "extraEnvVars"))
	}
	wantEnvKeys := map[string]string{
		"DRAGONFLY_INITIAL_ROOT_PASSWORD": "password",
		"MANAGER_AUTH_JWT_KEY":            "jwt-key",
	}
	for _, raw := range envVars {
		env, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("manager.extraEnvVars entry = %T, want map[string]any", raw)
		}
		name, _ := env["name"].(string)
		wantKey, ok := wantEnvKeys[name]
		if !ok {
			t.Errorf("unexpected manager env var %q", name)
			continue
		}
		ref := nested(t, env, "valueFrom", "secretKeyRef")
		if ref["name"] != "dragonfly-manager-api" || ref["key"] != wantKey {
			t.Errorf("%s secretKeyRef = %v", name, ref)
		}
		delete(wantEnvKeys, name)
	}
	if len(wantEnvKeys) != 0 {
		t.Errorf("missing manager env vars: %v", wantEnvKeys)
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
		if secret["secretName"] != ManagerMTLSSecretName {
			t.Errorf("mTLS volume secret = %v", secret)
		}
		foundMTLS = true
	}
	if !foundMTLS {
		t.Error("manager values have no mTLS volume")
	}
	if got := valueAt(t, values, "manager", "podAnnotations", certRevisionAnnotation); got != "mgr1" {
		t.Errorf("manager podAnnotations revision = %v", got)
	}
}

func TestManagerHelmOpWithoutCredentialsSecret(t *testing.T) {
	cluster := testCluster()
	cluster.ManagerAPICredentialsSecret = ""
	op := Manager(cluster)

	if len(op.Spec.DownstreamResources) != 1 ||
		op.Spec.DownstreamResources[0].Name != ManagerMTLSSecretName {
		t.Errorf("downstreamResources = %+v, want only the manager Secret", op.Spec.DownstreamResources)
	}
	managerValues := nested(t, op.Spec.Helm.Values.Data, "manager")
	if _, ok := managerValues["extraEnvVars"]; ok {
		t.Errorf("manager.extraEnvVars = %v, want unset when the Secret name is empty", managerValues["extraEnvVars"])
	}
}

func TestMonitoringValues(t *testing.T) {
	components := []string{"manager", "scheduler", "seedClient", "client"}
	enabled := testCluster()
	enabled.Monitoring = true
	seen := map[string]bool{}
	for name, op := range map[string]*fleetv1alpha1.HelmOp{
		"manager": Manager(enabled),
		"control": Control(enabled),
		"data":    Data(enabled),
	} {
		values := op.Spec.Helm.Values.Data
		for _, component := range components {
			componentValues, ok := values[component].(map[string]any)
			if !ok {
				t.Fatalf("%s %s values are not a map", name, component)
			}
			isEnabled, _ := componentValues["enable"].(bool)
			metrics, hasMetrics := componentValues["metrics"].(map[string]any)
			if !hasMetrics {
				t.Errorf("%s %s.metrics is missing", name, component)
				continue
			}
			if metrics["enable"] != isEnabled {
				t.Errorf("%s %s.metrics.enable = %v, want %v", name, component, metrics["enable"], isEnabled)
			}
			serviceMonitor := nested(t, metrics, "serviceMonitor")
			if serviceMonitor["enable"] != isEnabled {
				t.Errorf("%s %s.metrics.serviceMonitor.enable = %v, want %v", name, component, serviceMonitor["enable"], isEnabled)
			}
			if isEnabled {
				seen[component] = true
			}
		}
	}
	for _, component := range components {
		if !seen[component] {
			t.Errorf("enabled component %s did not receive monitoring metrics", component)
		}
	}

	disabled := testCluster()
	for name, op := range map[string]*fleetv1alpha1.HelmOp{
		"manager": Manager(disabled),
		"control": Control(disabled),
		"data":    Data(disabled),
	} {
		values := op.Spec.Helm.Values.Data
		for _, component := range components {
			componentValues, ok := values[component].(map[string]any)
			if !ok {
				t.Fatalf("%s %s values are not a map", name, component)
			}
			if _, ok := componentValues["metrics"]; ok {
				t.Errorf("%s %s.metrics should be absent when monitoring is disabled", name, component)
			}
		}
	}
}

package helmop

import (
	"bytes"
	"strings"
	"testing"
	"text/template"

	fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"
	"sigs.k8s.io/yaml"
)

func TestDataHelmOp(t *testing.T) {
	op := Data(testCluster())

	if op.Name != "c1-data" || op.Namespace != "fleet-default" {
		t.Errorf("name/namespace = %s/%s", op.Name, op.Namespace)
	}
	if len(op.Spec.DependsOn) != 3 ||
		op.Spec.DependsOn[0].Name != "c1-control" ||
		op.Spec.DependsOn[1].Name != "c1-keydb" ||
		op.Spec.DependsOn[2].Name != "c1-extras" {
		t.Errorf("dependsOn = %+v", op.Spec.DependsOn)
	}
	if len(op.Spec.DownstreamResources) != 0 {
		t.Errorf("downstreamResources = %+v, want none: control is the sole client Secret owner", op.Spec.DownstreamResources)
	}

	values := op.Spec.Helm.Values.Data
	if got := valueAt(t, values, "externalManager", "host"); got != "" {
		t.Errorf("externalManager.host = %v, want empty", got)
	}
	if got := valueAt(t, values, "manager", "enable"); got != false {
		t.Errorf("manager.enable = %v", got)
	}
	if got := valueAt(t, values, "scheduler", "enable"); got != false {
		t.Errorf("scheduler.enable = %v", got)
	}

	seedClient := nested(t, values, "seedClient")
	if seedClient["enable"] != true || seedClient["hostNetwork"] != true {
		t.Errorf("seedClient enable/hostNetwork = %v/%v", seedClient["enable"], seedClient["hostNetwork"])
	}
	if got := valueAt(t, seedClient, "persistence", "enable"); got != false {
		t.Errorf("seedClient.persistence.enable = %v, want false (no PVC)", got)
	}
	if got := valueAt(t, seedClient, "replicas"); got != testCluster().SeedClientReplicas {
		t.Errorf("seedClient.replicas = %v", got)
	}
	dynconfig := op.Spec.Helm.TemplateValues["seedClient"]
	for _, want := range []string{"addrs:", ":8002", "scheduler-ips"} {
		if !strings.Contains(dynconfig, want) {
			t.Errorf("seedClient dynconfig template missing %q: %q", want, dynconfig)
		}
	}
	if got := valueAt(t, seedClient, "podAnnotations", certRevisionAnnotation); got != "rev1" {
		t.Errorf("seedClient podAnnotations revision = %v", got)
	}
	if got := valueAt(t, seedClient, "config", "host", "schedulerClusterID"); got != 42 {
		t.Errorf("seedClient config.host.schedulerClusterID = %v", got)
	}
	if _, ok := seedClient["host"]; ok {
		t.Error("seedClient has a top-level host key; schedulerClusterID must be under config.host")
	}
	seedScheduler := nested(t, seedClient, "config", "scheduler")
	if seedScheduler["caCert"] != CertDir+"/ca.crt" ||
		seedScheduler["cert"] != CertDir+"/tls.crt" ||
		seedScheduler["key"] != CertDir+"/tls.key" {
		t.Errorf("seedClient config.scheduler TLS = %v", seedScheduler)
	}
	if _, ok := seedScheduler["tls"]; ok {
		t.Error("seedClient config.scheduler contains a nested tls key")
	}

	assertPorts(t, seedClient, 4100, 4101, 4102, 4103, 4104)
	seedStorage := nested(t, seedClient, "config", "storage", "server")
	if seedStorage["tcpPort"] != 4105 || seedStorage["quicPort"] != 4106 {
		t.Errorf("seedClient storage ports = %v", seedStorage)
	}
	for _, component := range []string{"proxy", "metrics", "health", "stats"} {
		if got := valueAt(t, seedClient, "config", component, "ip"); got != "127.0.0.1" {
			t.Errorf("seedClient %s ip = %v", component, got)
		}
	}

	client := nested(t, values, "client")
	if client["enable"] != true || client["hostNetwork"] != true {
		t.Errorf("client enable/hostNetwork = %v/%v", client["enable"], client["hostNetwork"])
	}
	if got := valueAt(t, client, "dfinit", "enable"); got != false {
		t.Errorf("client.dfinit.enable = %v", got)
	}
	if got := valueAt(t, client, "config", "host", "schedulerClusterID"); got != 42 {
		t.Errorf("client config.host.schedulerClusterID = %v", got)
	}
	if _, ok := client["host"]; ok {
		t.Error("client has a top-level host key; schedulerClusterID must be under config.host")
	}
	clientScheduler := nested(t, client, "config", "scheduler")
	if clientScheduler["caCert"] != CertDir+"/ca.crt" ||
		clientScheduler["cert"] != CertDir+"/tls.crt" ||
		clientScheduler["key"] != CertDir+"/tls.key" {
		t.Errorf("client config.scheduler TLS = %v", clientScheduler)
	}
	if _, ok := clientScheduler["tls"]; ok {
		t.Error("client config.scheduler contains a nested tls key")
	}
	if got := valueAt(t, client, "priorityClassName"); got != "system-node-critical" {
		t.Errorf("client.priorityClassName = %v", got)
	}
	if got := valueAt(t, client, "podAnnotations", certRevisionAnnotation); got != "rev1" {
		t.Errorf("client podAnnotations revision = %v", got)
	}
	assertPorts(t, client, 4000, 4001, 4002, 4003, 4004)
	clientStorage := nested(t, client, "config", "storage", "server")
	if clientStorage["tcpPort"] != 4005 || clientStorage["quicPort"] != 4006 {
		t.Errorf("client storage ports = %v", clientStorage)
	}
	for _, component := range []string{"proxy", "metrics", "health", "stats"} {
		if got := valueAt(t, client, "config", component, "ip"); got != "127.0.0.1" {
			t.Errorf("client %s ip = %v", component, got)
		}
	}
	clientVolumes, ok := valueAt(t, client, "extraVolumes").([]any)
	if !ok {
		t.Fatalf("client extraVolumes is not a list")
	}
	var storageVolume map[string]any
	for _, volume := range clientVolumes {
		volumeMap, ok := volume.(map[string]any)
		if ok && volumeMap["name"] == "storage" {
			storageVolume = volumeMap
			break
		}
	}
	if storageVolume == nil {
		t.Fatal("client storage volume is missing")
	}
	if _, ok := storageVolume["emptyDir"]; ok {
		t.Error("client storage volume is emptyDir, want hostPath")
	}
	hostPath, ok := storageVolume["hostPath"].(map[string]any)
	if !ok {
		t.Fatalf("client storage hostPath = %v, want map", storageVolume["hostPath"])
	}
	if hostPath["path"] != "/var/lib/dragonfly" || hostPath["type"] != "DirectoryOrCreate" {
		t.Errorf("client storage hostPath = %v, want /var/lib/dragonfly DirectoryOrCreate", hostPath)
	}

	assertNoTLS(t, nested(t, seedClient, "config", "upload", "server"))
	assertNoTLS(t, nested(t, seedClient, "config", "upload", "client"))
	assertNoTLS(t, nested(t, client, "config", "upload", "server"))
	assertNoTLS(t, nested(t, client, "config", "upload", "client"))

	for _, component := range []string{"seedClient", "client"} {
		config := nested(t, values, component, "config")
		if _, ok := config["manager"]; ok {
			t.Errorf("%s config contains manager.addr; data must use dynconfig", component)
		}
		if got := valueAt(t, config, "download", "server", "requestRateLimit"); got != 500 {
			t.Errorf("%s download.server.requestRateLimit = %v, want 500", component, got)
		}
		if got := valueAt(t, config, "proxy", "server", "requestRateLimit"); got != 500 {
			t.Errorf("%s proxy.server.requestRateLimit = %v, want 500", component, got)
		}
		rules, ok := valueAt(t, config, "proxy", "rules").([]map[string]any)
		if !ok {
			t.Fatalf("%s proxy.rules = %v, want a list (dfdaemon rejects a map)", component, valueAt(t, config, "proxy", "rules"))
		}
		if len(rules) != 2 {
			t.Errorf("%s proxy.rules = %v, want blobs and manifests rules", component, rules)
		}
		for _, regex := range []string{"blobs/sha256.*", "manifests/sha256.*"} {
			found := false
			for _, rule := range rules {
				if rule["regex"] != regex {
					continue
				}
				found = true
				if rule["schedulingPolicy"] != "always" {
					t.Errorf("%s proxy rule %q schedulingPolicy = %v, want always", component, regex, rule["schedulingPolicy"])
				}
			}
			if !found {
				t.Errorf("%s proxy.rules is missing regex %q", component, regex)
			}
		}
	}
}

func TestDataHelmOpP2PDataTLSEnabled(t *testing.T) {
	c := testCluster()
	c.P2PDataTLS = true
	values := Data(c).Spec.Helm.Values.Data
	for _, component := range []string{"seedClient", "client"} {
		server := nested(t, values, component, "config", "upload", "server")
		if server["caCert"] != CertDir+"/ca.crt" || server["cert"] != CertDir+"/tls.crt" || server["key"] != CertDir+"/tls.key" {
			t.Errorf("%s upload server TLS = %v", component, server)
		}
		client := nested(t, values, component, "config", "upload", "client")
		if client["caCert"] != CertDir+"/ca.crt" || client["cert"] != CertDir+"/tls.crt" || client["key"] != CertDir+"/tls.key" {
			t.Errorf("%s upload client TLS = %v", component, client)
		}
	}
}

func assertNoTLS(t *testing.T, value map[string]any) {
	t.Helper()
	for _, key := range []string{"caCert", "cert", "key", "tls"} {
		if _, ok := value[key]; ok {
			t.Errorf("unexpected TLS key %q in %v", key, value)
		}
	}
}

func assertPorts(t *testing.T, component map[string]any, upload, proxy, metrics, health, stats int) {
	t.Helper()
	ports := map[string]int{
		"upload": upload, "proxy": proxy, "metrics": metrics, "health": health, "stats": stats,
	}
	for name, want := range ports {
		if got := valueAt(t, component, "config", name, "server", "port"); got != want {
			t.Errorf("%s port = %v, want %d", name, got, want)
		}
	}
}

func TestControlHelmOp(t *testing.T) {
	op := Control(testCluster())

	if op.Name != "c1-control" {
		t.Errorf("name = %q", op.Name)
	}
	if len(op.Spec.DependsOn) != 2 || op.Spec.DependsOn[0].Name != "c1-keydb" || op.Spec.DependsOn[1].Name != SchedulerBundleName("c1") {
		t.Errorf("dependsOn = %+v, want keydb and scheduler Bundle", op.Spec.DependsOn)
	}
	if len(op.Spec.DownstreamResources) != 1 || op.Spec.DownstreamResources[0].Name != "dragonfly-p2p-mtls" {
		t.Errorf("control downstreamResources = %+v, want the client Secret", op.Spec.DownstreamResources)
	}

	values := op.Spec.Helm.Values.Data
	if got := valueAt(t, values, "scheduler", "enable"); got != true {
		t.Errorf("scheduler.enable = %v", got)
	}
	if got := valueAt(t, values, "scheduler", "replicas"); got != testCluster().SchedulerReplicas {
		t.Errorf("scheduler.replicas = %v", got)
	}
	if got := valueAt(t, values, "scheduler", "config", "manager", "schedulerClusterID"); got != 42 {
		t.Errorf("schedulerClusterID = %v", got)
	}
	if got := valueAt(t, values, "externalManager", "host"); got != "grpc.example.com" {
		t.Errorf("externalManager.host = %v", got)
	}
	if got := valueAt(t, values, "externalManager", "grpcPort"); got != ManagerGRPCEdgePort {
		t.Errorf("externalManager.grpcPort = %v, want edge port %d", got, ManagerGRPCEdgePort)
	}
	for _, component := range []string{"seedClient", "client", "manager"} {
		if got := valueAt(t, values, component, "enable"); got != false {
			t.Errorf("%s.enable = %v", component, got)
		}
	}
	managerTLS := nested(t, values, "scheduler", "config", "manager", "tls")
	if managerTLS["caCert"] != ClientCertDir+"/ca.crt" {
		t.Errorf("manager client TLS = %v", managerTLS)
	}
	serverTLS := nested(t, values, "scheduler", "config", "server", "tls")
	if serverTLS["caCert"] != CertDir+"/ca.crt" {
		t.Errorf("server TLS = %v", serverTLS)
	}
	seedPeer := nested(t, values, "scheduler", "config", "seedPeer")
	if _, ok := seedPeer["tls"]; ok {
		t.Errorf("seedPeer TLS is present by default: %v", seedPeer)
	}
	if got := valueAt(t, values, "scheduler", "config", "server", "advertiseIP"); got != SharedSchedulerPublicIPTemplate {
		t.Errorf("scheduler advertiseIP = %v, want the scheduler-ips template", got)
	}
	if got := valueAt(t, values, "scheduler", "config", "server", "advertisePort"); got != SchedulerPort {
		t.Errorf("scheduler advertisePort = %v, want scheduler gRPC port %d", got, SchedulerPort)
	}
	if got := valueAt(t, values, "scheduler", "podAnnotations", certRevisionAnnotation); got != "rev1" {
		t.Errorf("scheduler client revision = %v", got)
	}
	if got := valueAt(t, values, "scheduler", "podAnnotations", AnnotationMTLSCertRevision); got != "sched1" {
		t.Errorf("scheduler revision = %v", got)
	}
	volumes := valueAt(t, values, "scheduler", "extraVolumes").([]any)
	foundScheduler, foundClient := false, false
	for _, raw := range volumes {
		volume, _ := raw.(map[string]any)
		secret, _ := volume["secret"].(map[string]any)
		switch volume["name"] {
		case "scheduler-mtls":
			foundScheduler = secret["secretName"] == SchedulerMTLSSecretName
		case "client-mtls":
			foundClient = secret["secretName"] == "dragonfly-p2p-mtls"
		}
	}
	if !foundScheduler || !foundClient {
		t.Errorf("scheduler extraVolumes = %v, want scheduler and client Secrets", volumes)
	}
}

func TestControlHelmOpP2PDataTLSEnabled(t *testing.T) {
	c := testCluster()
	c.P2PDataTLS = true
	values := Control(c).Spec.Helm.Values.Data
	seedPeerTLS := nested(t, values, "scheduler", "config", "seedPeer", "tls")
	if seedPeerTLS["caCert"] != CertDir+"/ca.crt" || seedPeerTLS["cert"] != CertDir+"/tls.crt" || seedPeerTLS["key"] != CertDir+"/tls.key" {
		t.Errorf("seedPeer TLS = %v", seedPeerTLS)
	}
	if got := valueAt(t, values, "scheduler", "config", "server", "tls", "caCert"); got != CertDir+"/ca.crt" {
		t.Errorf("scheduler server TLS = %v", got)
	}
	if got := valueAt(t, values, "scheduler", "config", "manager", "tls", "caCert"); got != ClientCertDir+"/ca.crt" {
		t.Errorf("manager client TLS = %v", got)
	}
}

func TestDataResourceSizing(t *testing.T) {
	values := Data(testCluster()).Spec.Helm.Values.Data

	seedClient := nested(t, values, "seedClient")
	assertResources(t, seedClient, "1", "2Gi", "4", "8Gi")
	client := nested(t, values, "client")
	assertResources(t, client, "250m", "512Mi", "2", "4Gi")
}

func assertResources(t *testing.T, component map[string]any, requestsCPU, requestsMemory, limitsCPU, limitsMemory string) {
	t.Helper()
	requests := nested(t, component, "resources", "requests")
	if requests["cpu"] != requestsCPU || requests["memory"] != requestsMemory {
		t.Errorf("resources.requests = %v, want cpu=%s memory=%s", requests, requestsCPU, requestsMemory)
	}
	limits := nested(t, component, "resources", "limits")
	if limits["cpu"] != limitsCPU || limits["memory"] != limitsMemory {
		t.Errorf("resources.limits = %v, want cpu=%s memory=%s", limits, limitsCPU, limitsMemory)
	}
}

func TestNoInClusterManagerAddress(t *testing.T) {
	c := testCluster()
	for name, op := range map[string]*fleetv1alpha1.HelmOp{
		"control": Control(c),
		"data":    Data(c),
	} {
		if containsSubstring(op.Spec.Helm.Values.Data, ".dragonfly-system.svc") {
			t.Errorf("%s HelmOp contains an in-cluster manager DNS address", name)
		}
	}
	if got := valueAt(t, Control(c).Spec.Helm.Values.Data, "externalManager", "host"); got != "grpc.example.com" {
		t.Errorf("control externalManager.host = %v, want public manager host", got)
	}
	if got := valueAt(t, Data(c).Spec.Helm.Values.Data, "externalManager", "host"); got != "" {
		t.Errorf("data externalManager.host = %v, want empty local dynconfig path", got)
	}
}

func containsSubstring(value any, substr string) bool {
	switch typed := value.(type) {
	case string:
		return strings.Contains(typed, substr)
	case map[string]any:
		for _, child := range typed {
			if containsSubstring(child, substr) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsSubstring(child, substr) {
				return true
			}
		}
	}
	return false
}

func TestSchedulerAddrsTemplateRendersFleetAnnotations(t *testing.T) {
	tmpl := Data(testCluster()).Spec.Helm.TemplateValues["seedClient"]
	tests := []struct {
		name        string
		annotations map[string]string
		want        []string
	}{
		{
			name:        "ips",
			annotations: map[string]string{"p2p.dragonfly.io/scheduler-ips": "203.0.113.10,203.0.113.11"},
			want:        []string{"203.0.113.10:8002", "203.0.113.11:8002"},
		},
		{
			name:        "empty",
			annotations: map[string]string{},
			want:        nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rendered, err := renderFleetTemplate(t, tmpl, tc.annotations)
			if err != nil {
				t.Fatalf("render template: %v", err)
			}
			var values map[string]any
			if err := yaml.Unmarshal(rendered, &values); err != nil {
				t.Fatalf("rendered values are not YAML: %v\n%s", err, rendered)
			}
			addrs, _ := valueAt(t, values, "dynconfig", "scheduler", "addrs").([]any)
			if len(addrs) != len(tc.want) {
				t.Fatalf("addrs = %v, want %v", addrs, tc.want)
			}
			for i, want := range tc.want {
				if addrs[i] != want {
					t.Errorf("addrs[%d] = %v, want %s", i, addrs[i], want)
				}
			}
		})
	}
}

func renderFleetTemplate(t *testing.T, text string, annotations map[string]string) ([]byte, error) {
	t.Helper()
	tmpl, err := template.New("values").Delims("${", "}").Funcs(template.FuncMap{
		"default": func(def, value any) any {
			if value == nil || value == "" {
				return def
			}
			return value
		},
		"splitList": func(sep, value string) []string { return strings.Split(value, sep) },
		"first": func(values []string) string {
			if len(values) == 0 {
				return ""
			}
			return values[0]
		},
	}).Parse(text)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, map[string]any{"ClusterAnnotations": annotations}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

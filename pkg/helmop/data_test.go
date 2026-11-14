package helmop

import "testing"

func TestDataHelmOp(t *testing.T) {
	op := Data(testCluster())

	if op.Name != "c1-data" || op.Namespace != "fleet-default" {
		t.Errorf("name/namespace = %s/%s", op.Name, op.Namespace)
	}
	if len(op.Spec.DependsOn) != 2 ||
		op.Spec.DependsOn[0].Name != "c1-control" ||
		op.Spec.DependsOn[1].Name != "c1-keydb" {
		t.Errorf("dependsOn = %+v", op.Spec.DependsOn)
	}
	if len(op.Spec.DownstreamResources) != 1 || op.Spec.DownstreamResources[0].Name != "dragonfly-p2p-mtls" {
		t.Errorf("downstreamResources = %+v", op.Spec.DownstreamResources)
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
	if got := valueAt(t, seedClient, "dynconfig", "scheduler", "addr"); got != "c1-control-scheduler.dragonfly-system.svc:8002" {
		t.Errorf("seedClient dynconfig addr = %v", got)
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

	uploadServer := nested(t, seedClient, "config", "upload", "server")
	if uploadServer["caCert"] != CertDir+"/ca.crt" ||
		uploadServer["cert"] != CertDir+"/tls.crt" ||
		uploadServer["key"] != CertDir+"/tls.key" {
		t.Errorf("seedClient upload TLS = %v", uploadServer)
	}

	for _, component := range []string{"seedClient", "client"} {
		config := nested(t, values, component, "config")
		if _, ok := config["manager"]; ok {
			t.Errorf("%s config contains manager.addr; data must use dynconfig", component)
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
	if len(op.Spec.DependsOn) != 1 || op.Spec.DependsOn[0].Name != "c1-keydb" {
		t.Errorf("dependsOn = %+v", op.Spec.DependsOn)
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
	if managerTLS["caCert"] != CertDir+"/ca.crt" {
		t.Errorf("manager TLS = %v", managerTLS)
	}
	serverTLS := nested(t, values, "scheduler", "config", "server", "tls")
	if serverTLS["caCert"] != CertDir+"/ca.crt" {
		t.Errorf("server TLS = %v", serverTLS)
	}
	seedPeerTLS := nested(t, values, "scheduler", "config", "seedPeer", "tls")
	if seedPeerTLS["caCert"] != CertDir+"/ca.crt" {
		t.Errorf("seedPeer TLS = %v", seedPeerTLS)
	}
}

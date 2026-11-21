package bootstrap

import (
	"strings"
	"testing"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildInstallEnvCarriesDistroAndSchedulerIPs(t *testing.T) {
	secret := &corev1.Secret{Data: map[string][]byte{
		"ca.crt":                []byte("ca"),
		corev1.TLSCertKey:       []byte("cert"),
		corev1.TLSPrivateKeyKey: []byte("key"),
	}}
	cfg := InstallConfig{
		ManagerGRPCDomain: "manager.example.com",
		ManagerGRPCPort:   443,
		StaticURL:         "https://static.example.com",
		SchedulerDomain:   "example.com",
		ClusterIDFallback: "c-m-fallback",
	}
	for _, tc := range []struct {
		version string
		distro  string
	}{
		{"v1.35.9+k3s1", "k3s"},
		{"v1.35.9+rke2r1", "rke2"},
		{"", "k3s"},
	} {
		cluster := &provv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				"p2p.dragonfly.io/scheduler-cluster-id": "2",
				"p2p.dragonfly.io/scheduler-ips":        "203.0.113.10,203.0.113.11",
			}},
			Spec:   provv1.ClusterSpec{KubernetesVersion: tc.version},
			Status: provv1.ClusterStatus{ClusterName: "c-m-test"},
		}
		env, err := BuildInstallEnv(cluster, secret, "", cfg)
		if err != nil {
			t.Fatalf("BuildInstallEnv(%q) error = %v", tc.version, err)
		}
		for _, want := range []string{
			"P2P_DISTRO=" + tc.distro,
			"SCHEDULER_IPS=203.0.113.10,203.0.113.11",
			"SCHEDULER_HOST=d7y-scheduler.c-m-test.example.com",
		} {
			if !strings.Contains(env, want) {
				t.Errorf("BuildInstallEnv(%q) missing %q: %q", tc.version, want, env)
			}
		}
	}
}

func TestBuildInstallEnvCarriesEmptySchedulerIPs(t *testing.T) {
	secret := &corev1.Secret{Data: map[string][]byte{
		"ca.crt":                []byte("ca"),
		corev1.TLSCertKey:       []byte("cert"),
		corev1.TLSPrivateKeyKey: []byte("key"),
	}}
	cluster := &provv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
			"p2p.dragonfly.io/scheduler-cluster-id": "2",
		}},
		Status: provv1.ClusterStatus{ClusterName: "c-m-test"},
	}
	env, err := BuildInstallEnv(cluster, secret, "", InstallConfig{
		ManagerGRPCDomain: "manager.example.com",
		ManagerGRPCPort:   443,
		StaticURL:         "https://static.example.com",
		SchedulerDomain:   "example.com",
	})
	if err != nil {
		t.Fatalf("BuildInstallEnv() error = %v", err)
	}
	if !strings.Contains(env, "SCHEDULER_IPS=\n") {
		t.Errorf("empty scheduler IPs are not represented: %q", env)
	}
}

func TestBuildInstallEnvEmptySchedulerIDUsesPlaceholder(t *testing.T) {
	secret := &corev1.Secret{Data: map[string][]byte{
		"ca.crt":                []byte("ca"),
		corev1.TLSCertKey:       []byte("cert"),
		corev1.TLSPrivateKeyKey: []byte("key"),
	}}
	cluster := &provv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{}},
		Status:     provv1.ClusterStatus{ClusterName: "c-m-test"},
	}
	env, err := BuildInstallEnv(cluster, secret, "", InstallConfig{
		ManagerGRPCDomain: "manager.example.com",
		ManagerGRPCPort:   443,
		StaticURL:         "https://static.example.com",
		SchedulerDomain:   "example.com",
	})
	if err != nil {
		t.Fatalf("BuildInstallEnv() error = %v", err)
	}
	if !strings.Contains(env, "SCHEDULER_CLUSTER_ID=0\n") {
		t.Errorf("env = %q, want scheduler cluster ID 0 placeholder", env)
	}
}

func TestBuildInstallEnvUsesTrustBundle(t *testing.T) {
	secret := &corev1.Secret{Data: map[string][]byte{
		"ca.crt":                []byte("new-ca"),
		"ca-bundle.crt":         []byte("new-ca-legacy-ca"),
		corev1.TLSCertKey:       []byte("cert"),
		corev1.TLSPrivateKeyKey: []byte("key"),
	}}
	cluster := &provv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
			"p2p.dragonfly.io/scheduler-cluster-id": "2",
		}},
		Status: provv1.ClusterStatus{ClusterName: "c-m-test"},
	}
	env, err := BuildInstallEnv(cluster, secret, "", InstallConfig{
		ManagerGRPCDomain: "manager.example.com",
		ManagerGRPCPort:   443,
		StaticURL:         "https://static.example.com",
		SchedulerDomain:   "example.com",
	})
	if err != nil {
		t.Fatalf("BuildInstallEnv() error = %v", err)
	}
	if !strings.Contains(env, "CA_CRT_B64=") || !strings.Contains(env, "bmV3LWNhLWxlZ2FjeS1jYQ==") {
		t.Errorf("env does not carry the CA bundle: %q", env)
	}
}

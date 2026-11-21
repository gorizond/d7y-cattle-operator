package bootstrap

import (
	"fmt"
	"strings"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	corev1 "k8s.io/api/core/v1"
)

const (
	// Marker identifies an injected bootstrap block for idempotency.
	Marker                       = "# d7y-cattle-operator:p2p-install"
	schedulerClusterIDAnnotation = "p2p.dragonfly.io/scheduler-cluster-id"
	schedulerIPsAnnotation       = "p2p.dragonfly.io/scheduler-ips"
)

// InstallConfig carries the operator-wide values used to build the p2p install
// block for the machine-bootstrap Secret controller.
type InstallConfig struct {
	ManagerGRPCDomain string
	ManagerGRPCPort   int
	StaticURL         string
	SmartProxyURL     string
	NydusMirrorURL    string
	SchedulerDomain   string
	ClusterIDFallback string
}

// BuildInstallEnv builds the env-file assignments for the embedded p2p install
// block. schedulerID overrides the Cluster annotation when non-empty; otherwise
// the Cluster annotation is used.
func BuildInstallEnv(cluster *provv1.Cluster, secret *corev1.Secret, schedulerID string, cfg InstallConfig) (string, error) {
	if secret == nil {
		return "", fmt.Errorf("mTLS Secret is not configured")
	}
	if schedulerID == "" && cluster != nil {
		schedulerID = strings.TrimSpace(cluster.Annotations[schedulerClusterIDAnnotation])
	}
	if schedulerID == "" {
		return "", fmt.Errorf("scheduler cluster ID annotation is empty")
	}
	clusterID := strings.TrimSpace(cfg.ClusterIDFallback)
	if cluster != nil && strings.TrimSpace(cluster.Status.ClusterName) != "" {
		clusterID = strings.TrimSpace(cluster.Status.ClusterName)
	}
	host := schedulerHost(cfg.SchedulerDomain, clusterID)
	if host == "" {
		return "", fmt.Errorf("scheduler host is empty: P2P_DOMAIN and cluster ID are required")
	}
	if cfg.ManagerGRPCPort < 1 || cfg.ManagerGRPCPort > 65535 {
		return "", fmt.Errorf("manager gRPC port %d is outside 1-65535", cfg.ManagerGRPCPort)
	}
	env, err := Env(Payload{
		ManagerGRPC:        fmt.Sprintf("https://%s:%d", cfg.ManagerGRPCDomain, cfg.ManagerGRPCPort),
		StaticURL:          cfg.StaticURL,
		SmartProxyURL:      cfg.SmartProxyURL,
		NydusMirrorURL:     cfg.NydusMirrorURL,
		SchedulerHost:      host,
		SchedulerClusterID: schedulerID,
		CACert:             secret.Data["ca.crt"],
		ClientCert:         secret.Data[corev1.TLSCertKey],
		ClientKey:          secret.Data[corev1.TLSPrivateKeyKey],
	})
	if err != nil {
		return "", err
	}
	distro := "k3s"
	if cluster != nil && strings.Contains(strings.ToLower(cluster.Spec.KubernetesVersion), "+rke2") {
		distro = "rke2"
	}
	ips := ""
	if cluster != nil {
		ips = strings.TrimSpace(cluster.Annotations[schedulerIPsAnnotation])
	}
	return "P2P_DISTRO=" + distro + "\nSCHEDULER_IPS=" + ips + "\n" + env, nil
}

func schedulerHost(domain, clusterID string) string {
	domain = strings.TrimSpace(strings.Trim(domain, "."))
	clusterID = strings.TrimSpace(clusterID)
	if domain == "" || clusterID == "" {
		return ""
	}
	return fmt.Sprintf("d7y-scheduler.%s.%s", clusterID, domain)
}

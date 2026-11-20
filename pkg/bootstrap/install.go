package bootstrap

import (
	"fmt"
	"strings"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	corev1 "k8s.io/api/core/v1"
)

const schedulerClusterIDAnnotation = "p2p.dragonfly.io/scheduler-cluster-id"

// InstallConfig carries the operator-wide values used to build the p2p install
// line. The same values feed the admission webhook and the machine-bootstrap
// secret controller.
type InstallConfig struct {
	ManagerGRPCDomain string
	ManagerGRPCPort   int
	StaticURL         string
	SmartProxyURL     string
	NydusMirrorURL    string
	SchedulerDomain   string
	ClusterIDFallback string
}

// BuildInstallLine builds the encoded p2p install command for a provisioning
// Cluster. schedulerID overrides the Cluster annotation when non-empty (the
// webhook uses it for machine-config annotations); otherwise the Cluster
// annotation is used. A nil Cluster is allowed for webhook config-level
// applicability and uses the ID fallback.
func BuildInstallLine(cluster *provv1.Cluster, secret *corev1.Secret, schedulerID string, cfg InstallConfig) (string, error) {
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
	payload, err := Encode(Payload{
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
	return InjectionLine(cfg.StaticURL, payload), nil
}

func schedulerHost(domain, clusterID string) string {
	domain = strings.TrimSpace(strings.Trim(domain, "."))
	clusterID = strings.TrimSpace(clusterID)
	if domain == "" || clusterID == "" {
		return ""
	}
	return fmt.Sprintf("d7y-scheduler.%s.%s", clusterID, domain)
}

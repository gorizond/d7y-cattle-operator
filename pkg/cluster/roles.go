package cluster

import (
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	// LabelEnabled is the single gate for deploying the Dragonfly stack.
	LabelEnabled = "p2p-dragonfly-io"

	AnnotationManager                = "p2p.dragonfly.io/manager"
	AnnotationSeed                   = "p2p.dragonfly.io/seed"
	AnnotationSchedulerClusterID     = "p2p.dragonfly.io/scheduler-cluster-id"
	AnnotationSchedulerClusterIDAuto = "p2p.dragonfly.io/scheduler-cluster-id-auto"
	AnnotationSchedulerReplicas      = "p2p.dragonfly.io/scheduler-replicas"
	AnnotationSeedClientReplicas     = "p2p.dragonfly.io/seedclient-replicas"
	AnnotationMonitoring             = "p2p.dragonfly.io/monitoring"
	// AnnotationP2PDataTLS keeps TLS on the P2P data path (dfdaemon upload
	// server/client and scheduler seedPeer). It is disabled by default; only
	// "true" or "enabled" preserve the legacy data-path TLS values. Scheduler
	// server, manager gRPC/API, and monitoring TLS are always kept.
	AnnotationP2PDataTLS = "p2p.dragonfly.io/p2p-data-tls"

	// AnnotationHarbor deploys Harbor for the cluster. HarborDomain is
	// required when it is "true"; HarborURL switches to an existing Harbor.
	AnnotationHarbor       = "p2p.dragonfly.io/harbor"
	AnnotationHarborDomain = "p2p.dragonfly.io/harbor-domain"
	AnnotationHarborURL    = "p2p.dragonfly.io/harbor-url"
	// AnnotationProxy deploys the external registry proxy independently of
	// Harbor and the seed role.
	AnnotationProxy = "p2p.dragonfly.io/proxy"
	// AnnotationAdmission deploys the opt-in Pod image rewriting webhook. It is
	// independent of Harbor, the proxy, and the seed role.
	AnnotationAdmission = "p2p.dragonfly.io/admission"
	// AnnotationAdmissionExclude lists namespaces the admission webhook must
	// skip, in addition to the chart's system namespaces and release namespace.
	// The annotation spelling is part of the external contract.
	AnnotationAdmissionExclude = "p2p.dragonfly.io/mutate-exclure"
)

// Roles is the resolved role set for a Cluster.
type Roles struct {
	Enabled   bool
	Manager   bool
	Seed      bool
	Harbor    bool
	Proxy     bool
	Admission bool
	// HarborDomain and HarborURL are trimmed annotations. HarborDomain is the
	// ingress/externalURL host; HarborURL selects an existing/external Harbor.
	HarborDomain string
	HarborURL    string
	// AdmissionExclude is the resolved namespace skip list for the admission
	// webhook, parsed from p2p.dragonfly.io/mutate-exclure.
	AdmissionExclude []string
}

// ResolveRoles applies the label gate first, then the role annotations.
func ResolveRoles(cluster *provv1.Cluster) Roles {
	if cluster == nil {
		return Roles{}
	}
	enabled := cluster.Labels[LabelEnabled] == "true"
	if !enabled {
		return Roles{}
	}
	return Roles{
		Enabled:      enabled,
		Manager:      cluster.Annotations[AnnotationManager] == "true",
		Seed:         cluster.Annotations[AnnotationSeed] == "true",
		Harbor:       cluster.Annotations[AnnotationHarbor] == "true",
		Proxy:        cluster.Annotations[AnnotationProxy] == "true",
		Admission:    cluster.Annotations[AnnotationAdmission] == "true",
		HarborDomain: strings.TrimSpace(cluster.Annotations[AnnotationHarborDomain]),
		HarborURL:    strings.TrimSpace(cluster.Annotations[AnnotationHarborURL]),

		AdmissionExclude: parseAdmissionExclude(cluster.Annotations[AnnotationAdmissionExclude]),
	}
}

// parseAdmissionExclude splits the comma-separated namespace annotation,
// trims entries, drops blanks, and deduplicates while preserving order.
// Invalid DNS-1123 labels are logged and skipped; the list is never allowed to
// break reconciliation.
func parseAdmissionExclude(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	seen := make(map[string]struct{})
	var namespaces []string
	for _, entry := range strings.Split(raw, ",") {
		namespace := strings.TrimSpace(entry)
		if namespace == "" {
			continue
		}
		if errs := validation.IsDNS1123Label(namespace); len(errs) > 0 {
			slog.Warn("skipping invalid admission exclude namespace",
				"annotation", AnnotationAdmissionExclude,
				"namespace", namespace,
				"reason", strings.Join(errs, "; "))
			continue
		}
		if _, ok := seen[namespace]; ok {
			continue
		}
		seen[namespace] = struct{}{}
		namespaces = append(namespaces, namespace)
	}
	return namespaces
}

// validateHarborDomain verifies the Harbor ingress/externalURL host. Scheme,
// port, path, and wildcards are rejected.
func validateHarborDomain(value string) error {
	if value == "" {
		return fmt.Errorf("harbor domain is required")
	}
	if strings.Contains(value, "://") || strings.ContainsAny(value, "/:? @") {
		return fmt.Errorf("harbor domain %q must be a host name without scheme, port, or path", value)
	}
	if msgs := validation.IsDNS1123Subdomain(value); len(msgs) > 0 {
		return fmt.Errorf("harbor domain %q: %s", value, strings.Join(msgs, "; "))
	}
	return nil
}

// validateHarborURL verifies an existing/external Harbor URL.
func validateHarborURL(value string) error {
	if value == "" {
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("harbor URL %q must be an absolute https URL", value)
	}
	return nil
}

// monitoringEnabled reports whether Dragonfly metrics should be enabled for a
// Cluster. The annotation is a Kubernetes label selector evaluated against the
// provisioning Cluster labels; "true"/"enabled" are unconditional shortcuts.
// Empty, invalid, or non-matching selectors keep monitoring disabled.
func monitoringEnabled(cluster *provv1.Cluster) bool {
	if cluster == nil {
		return false
	}
	raw := strings.TrimSpace(cluster.Annotations[AnnotationMonitoring])
	if raw == "" {
		return false
	}
	if strings.EqualFold(raw, "true") || strings.EqualFold(raw, "enabled") {
		return true
	}
	selector, err := labels.Parse(raw)
	if err != nil {
		return false
	}
	return selector.Matches(labels.Set(cluster.Labels))
}

// p2pDataTLSEnabled reports whether the P2P data path keeps TLS. The default
// is plaintext; only the explicit "true" and "enabled" values opt back in.
func p2pDataTLSEnabled(cluster *provv1.Cluster) bool {
	if cluster == nil {
		return false
	}
	raw := strings.TrimSpace(cluster.Annotations[AnnotationP2PDataTLS])
	return strings.EqualFold(raw, "true") || strings.EqualFold(raw, "enabled")
}

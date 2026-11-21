package cluster

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"
	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/yaml"

	"github.com/gorizond/d7y-cattle-operator/pkg/apply"
	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
	"github.com/gorizond/d7y-cattle-operator/pkg/pki"
)

const (
	// AnnotationLegacyTrustDeadline marks the end of the one-time trust
	// overlap after the legacy CA key was lost.
	AnnotationLegacyTrustDeadline = "p2p.dragonfly.io/legacy-trust-deadline"
	// legacyTrustWindow keeps both CAs trusted long enough for every pod to
	// roll onto the new CA bundle.
	legacyTrustWindow = 24 * time.Hour
)

// ensurePKI creates/loads the stable CA and ensures the shared client identity,
// the manager identity, and (for seeds) the per-seed scheduler identity and
// Bundle. It returns the possibly-annotated Cluster and whether the PKI path is
// ready.
func (c *Controller) ensurePKI(cluster *provv1.Cluster, roles Roles, seedIPs []string, seedDiscoveryFailed bool) (*provv1.Cluster, bool, error) {
	if cluster == nil || (!roles.Manager && !roles.Seed) {
		return cluster, true, nil
	}
	operatorNS := c.opts.OperatorNamespace
	client, clientErr := c.secrets.Get(operatorNS, c.opts.MTLSSecretName, metav1.GetOptions{})
	if clientErr != nil && !apierrors.IsNotFound(clientErr) {
		return cluster, false, fmt.Errorf("get client mTLS Secret %s/%s: %w", operatorNS, c.opts.MTLSSecretName, clientErr)
	}
	manager, managerErr := c.secrets.Get(operatorNS, helmop.ManagerMTLSSecretName, metav1.GetOptions{})
	if managerErr != nil && !apierrors.IsNotFound(managerErr) {
		return cluster, false, fmt.Errorf("get manager mTLS Secret %s/%s: %w", operatorNS, helmop.ManagerMTLSSecretName, managerErr)
	}
	scheduler, schedulerErr := c.secrets.Get(operatorNS, helmop.SchedulerSourceSecretName(cluster.Name), metav1.GetOptions{})
	if schedulerErr != nil && !apierrors.IsNotFound(schedulerErr) {
		return cluster, false, fmt.Errorf("get scheduler mTLS Secret %s/%s: %w", operatorNS, helmop.SchedulerSourceSecretName(cluster.Name), schedulerErr)
	}

	// A missing CA may only be created from scratch, or during the one-time
	// upgrade from the legacy shared leaf. Existing split leaves without a CA
	// must never be silently re-rooted.
	allowCreate := managerErr != nil && schedulerErr != nil
	if allowCreate && c.clusterCache != nil {
		otherLeaf, lookupErr := c.otherSchedulerLeafExists(cluster.Name)
		if lookupErr != nil {
			return cluster, false, lookupErr
		}
		if otherLeaf {
			allowCreate = false
		}
	}
	var legacyCA []byte
	if clientErr == nil && len(client.Data[corev1.TLSCertKey]) > 0 && len(client.Data[corev1.TLSPrivateKeyKey]) > 0 {
		legacyCA = client.Data["ca.crt"]
	}
	ca, created, err := pki.EnsureCA(c.secrets, operatorNS, pki.CASecretName, allowCreate, legacyCA)
	if err != nil {
		c.event(cluster, corev1.EventTypeWarning, "PKICAUnavailable", err.Error())
		return cluster, false, err
	}

	clientLegacy := false
	if clientErr == nil && len(client.Data[corev1.TLSCertKey]) > 0 {
		clientLegacy = !signedByCA(client.Data[corev1.TLSCertKey], ca)
	}

	deadlineRaw := strings.TrimSpace(cluster.Annotations[AnnotationLegacyTrustDeadline])
	if deadlineRaw == "" && (created && len(legacyCA) > 0 || clientLegacy) {
		deadlineRaw = time.Now().Add(legacyTrustWindow).UTC().Format(time.RFC3339)
		var err error
		cluster, err = c.setClusterAnnotation(cluster, AnnotationLegacyTrustDeadline, deadlineRaw)
		if err != nil {
			return cluster, false, err
		}
	}
	phaseA := false
	if deadlineRaw != "" {
		if deadline, err := time.Parse(time.RFC3339, deadlineRaw); err == nil && time.Now().Before(deadline) {
			phaseA = true
		}
	}

	clientOpts := pki.IssueOptions{CommonName: "d7y-cattle-operator-client", ClientAuth: true}
	if _, err := pki.EnsureLeaf(c.secrets, operatorNS, c.opts.MTLSSecretName, ca, clientOpts, phaseA && clientLegacy); err != nil {
		c.event(cluster, corev1.EventTypeWarning, "MTLSSecretUnavailable", err.Error())
		return cluster, false, err
	}
	clientSecret, err := c.secrets.Get(operatorNS, c.opts.MTLSSecretName, metav1.GetOptions{})
	if err != nil {
		return cluster, false, fmt.Errorf("get ensured client Secret: %w", err)
	}

	managerOpts, err := c.managerIssueOptions()
	if err != nil {
		c.event(cluster, corev1.EventTypeWarning, "ManagerCertificateFailed", err.Error())
		return cluster, false, err
	}
	if phaseA && (hasLeafData(client) || hasLeafData(manager)) {
		if _, err := c.ensureLegacyLeaf(operatorNS, helmop.ManagerMTLSSecretName, client.Data, ca); err != nil {
			c.event(cluster, corev1.EventTypeWarning, "MTLSSecretUnavailable", err.Error())
			return cluster, false, err
		}
	} else {
		if _, err := pki.EnsureLeaf(c.secrets, operatorNS, helmop.ManagerMTLSSecretName, ca, managerOpts, false); err != nil {
			c.event(cluster, corev1.EventTypeWarning, "MTLSSecretUnavailable", err.Error())
			return cluster, false, err
		}
	}
	managerSecret, err := c.secrets.Get(operatorNS, helmop.ManagerMTLSSecretName, metav1.GetOptions{})
	if err != nil {
		return cluster, false, fmt.Errorf("get ensured manager Secret: %w", err)
	}
	if err := c.replicateSecret(clientSecret, cluster.Namespace); err != nil {
		return cluster, false, err
	}
	if err := c.replicateSecret(managerSecret, cluster.Namespace); err != nil {
		return cluster, false, err
	}

	if roles.Seed {
		schedulerOpts, err := c.schedulerIssueOptions(cluster, seedIPs, seedDiscoveryFailed, scheduler)
		if err != nil {
			c.event(cluster, corev1.EventTypeWarning, "SchedulerHostUnavailable", err.Error())
			return cluster, false, err
		}
		if phaseA && (hasLeafData(client) || hasLeafData(scheduler)) {
			scheduler, err = c.ensureLegacyLeaf(operatorNS, helmop.SchedulerSourceSecretName(cluster.Name), client.Data, ca)
			if err != nil {
				c.event(cluster, corev1.EventTypeWarning, "MTLSSecretUnavailable", err.Error())
				return cluster, false, err
			}
		} else {
			if _, err := pki.EnsureLeaf(c.secrets, operatorNS, helmop.SchedulerSourceSecretName(cluster.Name), ca, schedulerOpts, false); err != nil {
				c.event(cluster, corev1.EventTypeWarning, "MTLSSecretUnavailable", err.Error())
				return cluster, false, err
			}
			scheduler, err = c.secrets.Get(operatorNS, helmop.SchedulerSourceSecretName(cluster.Name), metav1.GetOptions{})
			if err != nil {
				return cluster, false, fmt.Errorf("get ensured scheduler Secret: %w", err)
			}
		}
		if err := c.ensureSchedulerBundle(cluster, scheduler); err != nil {
			c.event(cluster, corev1.EventTypeWarning, "SchedulerBundleFailed", err.Error())
			return cluster, false, err
		}
		cluster, err = c.setClusterAnnotation(cluster, helmop.AnnotationMTLSCertRevision, pki.SecretRevision(scheduler.Data))
		if err != nil {
			return cluster, false, err
		}
	} else if err := c.deleteSchedulerPKI(cluster); err != nil {
		return cluster, false, err
	}

	return cluster, true, nil
}

// otherSchedulerLeafExists is the conservative CA guard: a missing CA may not
// be replaced while any other seed's scheduler identity still exists.
func (c *Controller) otherSchedulerLeafExists(current string) (bool, error) {
	clusters, err := c.clusterCache.List(metav1.NamespaceAll, labels.Everything())
	if err != nil {
		return false, fmt.Errorf("list clusters for CA guard: %w", err)
	}
	for _, cluster := range clusters {
		if cluster == nil || cluster.Name == current {
			continue
		}
		if _, err := c.secrets.Get(c.opts.OperatorNamespace, helmop.SchedulerSourceSecretName(cluster.Name), metav1.GetOptions{}); err == nil {
			return true, nil
		} else if !apierrors.IsNotFound(err) {
			return false, fmt.Errorf("check scheduler leaf %s/%s: %w", c.opts.OperatorNamespace, helmop.SchedulerSourceSecretName(cluster.Name), err)
		}
	}
	return false, nil
}

func (c *Controller) managerIssueOptions() (pki.IssueOptions, error) {
	if strings.TrimSpace(c.opts.ManagerGRPCDomain) == "" {
		return pki.IssueOptions{}, fmt.Errorf("manager gRPC domain is empty")
	}
	dns := []string{c.opts.ManagerGRPCDomain, c.opts.ManagerAPIHost, "dragonfly-manager." + helmop.TargetNamespace + ".svc", "localhost"}
	ips := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	return pki.IssueOptions{
		CommonName:  c.opts.ManagerGRPCDomain,
		DNSNames:    pki.NormalizeDNSNames(dns),
		IPAddresses: pki.NormalizeIPs(ips),
		ServerAuth:  true,
		ClientAuth:  true,
	}, nil
}

func (c *Controller) schedulerIssueOptions(cluster *provv1.Cluster, seedIPs []string, discoveryFailed bool, existing *corev1.Secret) (pki.IssueOptions, error) {
	clusterID := c.clusterID(cluster)
	if clusterID == "" {
		return pki.IssueOptions{}, fmt.Errorf("cluster %s/%s has empty status.clusterName and no --p2p-cluster-id fallback", cluster.Namespace, cluster.Name)
	}
	host := schedulerHost(c.opts.P2PDomain, clusterID)
	if host == "" {
		return pki.IssueOptions{}, fmt.Errorf("scheduler host is empty: --p2p-domain is required")
	}
	ips := publicIPs(seedIPs)
	if discoveryFailed && len(ips) == 0 {
		ips = existingPublicIPs(existing)
	}
	ips = append(ips, net.IPv4(127, 0, 0, 1), net.IPv6loopback)
	return pki.IssueOptions{
		CommonName:  host,
		DNSNames:    pki.NormalizeDNSNames([]string{host, cluster.Name + "-control-scheduler." + helmop.TargetNamespace + ".svc", "localhost"}),
		IPAddresses: pki.NormalizeIPs(ips),
		ServerAuth:  true,
		ClientAuth:  true,
	}, nil
}

func publicIPs(values []string) []net.IP {
	result := make([]net.IP, 0, len(values))
	for _, raw := range values {
		ip := net.ParseIP(strings.TrimSpace(raw))
		if ip != nil && pki.IsPublicIP(ip) {
			result = append(result, ip)
		}
	}
	return pki.NormalizeIPs(result)
}

func hasLeafData(secret *corev1.Secret) bool {
	return secret != nil && len(secret.Data[corev1.TLSCertKey]) > 0 && len(secret.Data[corev1.TLSPrivateKeyKey]) > 0
}

func signedByCA(certPEM []byte, ca *pki.CA) bool {
	certs, err := pki.ParseCertificates(certPEM)
	return err == nil && len(certs) > 0 && ca != nil && certs[0].CheckSignatureFrom(ca.Cert) == nil
}

func (c *Controller) ensureLegacyLeaf(namespace, name string, source map[string][]byte, ca *pki.CA) (*corev1.Secret, error) {
	if current, err := c.secrets.Get(namespace, name, metav1.GetOptions{}); err == nil {
		if current.Data == nil {
			current.Data = map[string][]byte{}
		}
		changed := false
		if current.Annotations != nil {
			if _, ok := current.Annotations["kubectl.kubernetes.io/last-applied-configuration"]; ok {
				delete(current.Annotations, "kubectl.kubernetes.io/last-applied-configuration")
				changed = true
			}
		}
		if string(current.Data["ca.crt"]) != string(ca.BundlePEM) {
			current.Data["ca.crt"] = append([]byte(nil), ca.BundlePEM...)
			changed = true
		}
		if !changed {
			return current, nil
		}
		return c.secrets.Update(current)
	} else if !apierrors.IsNotFound(err) {
		return nil, err
	}
	return c.secrets.Create(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Type:       corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"ca.crt":                append([]byte(nil), ca.BundlePEM...),
			corev1.TLSCertKey:       append([]byte(nil), source[corev1.TLSCertKey]...),
			corev1.TLSPrivateKeyKey: append([]byte(nil), source[corev1.TLSPrivateKeyKey]...),
		},
	})
}

func (c *Controller) replicateSecret(source *corev1.Secret, namespace string) error {
	if source == nil {
		return nil
	}
	if _, err := apply.ReplicateSecret(source, namespace, c.secrets); err != nil {
		return fmt.Errorf("replicate Secret %s/%s: %w", namespace, source.Name, err)
	}
	return nil
}

func (c *Controller) setClusterAnnotation(cluster *provv1.Cluster, key, value string) (*provv1.Cluster, error) {
	if cluster == nil || cluster.Annotations[key] == value {
		return cluster, nil
	}
	updated := cluster.DeepCopy()
	if updated.Annotations == nil {
		updated.Annotations = map[string]string{}
	}
	updated.Annotations[key] = value
	return c.clusters.Update(updated)
}

func (c *Controller) ensureSchedulerBundle(cluster *provv1.Cluster, secret *corev1.Secret) error {
	if c.bundles == nil || secret == nil {
		return fmt.Errorf("scheduler Bundle store is not configured")
	}
	payloadSecret := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      helmop.SchedulerMTLSSecretName,
			Namespace: helmop.TargetNamespace,
		},
		Type: corev1.SecretTypeTLS,
		Data: secret.Data,
	}
	raw, err := yaml.Marshal(payloadSecret)
	if err != nil {
		return fmt.Errorf("marshal scheduler Secret: %w", err)
	}
	labels := apply.Labels(cluster.Namespace, cluster.Name)
	labels["p2p-dragonfly-io"] = "p2p-mtls"
	bundle := &fleetv1alpha1.Bundle{
		TypeMeta: metav1.TypeMeta{APIVersion: "fleet.cattle.io/v1alpha1", Kind: "Bundle"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      helmop.SchedulerBundleName(cluster.Name),
			Namespace: cluster.Namespace,
			Labels:    labels,
		},
		Spec: fleetv1alpha1.BundleSpec{
			TargetNamespace: helmop.TargetNamespace,
			Targets:         []fleetv1alpha1.BundleTarget{{ClusterName: cluster.Name}},
			Resources:       []fleetv1alpha1.BundleResource{{Name: "scheduler-secret.yaml", Content: string(raw)}},
		},
	}
	_, err = apply.Ensure(c.ctx, c.bundles, bundle)
	if err != nil {
		return fmt.Errorf("apply scheduler Bundle %s/%s: %w", cluster.Namespace, bundle.Name, err)
	}
	return nil
}

func (c *Controller) deleteSchedulerPKI(cluster *provv1.Cluster) error {
	if c == nil || cluster == nil {
		return nil
	}
	var errs []error
	if c.bundles != nil {
		if _, err := apply.Delete(c.ctx, c.bundles, cluster.Namespace, helmop.SchedulerBundleName(cluster.Name)); err != nil {
			errs = append(errs, fmt.Errorf("delete scheduler Bundle: %w", err))
		}
	}
	if c.secrets != nil {
		if err := c.secrets.Delete(c.opts.OperatorNamespace, helmop.SchedulerSourceSecretName(cluster.Name), &metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("delete scheduler Secret: %w", err))
		}
	}
	return errorsJoin(errs)
}

func errorsJoin(errs []error) error {
	return errors.Join(errs...)
}

// existingPublicIPs keeps previously issued public scheduler IPs when a
// transient downstream discovery failure would otherwise shrink the SAN set.
func existingPublicIPs(secret *corev1.Secret) []net.IP {
	if secret == nil {
		return nil
	}
	certs, err := pki.ParseCertificates(secret.Data[corev1.TLSCertKey])
	if err != nil || len(certs) == 0 {
		return nil
	}
	var result []net.IP
	for _, ip := range certs[0].IPAddresses {
		if pki.IsPublicIP(ip) {
			result = append(result, ip)
		}
	}
	return pki.NormalizeIPs(result)
}

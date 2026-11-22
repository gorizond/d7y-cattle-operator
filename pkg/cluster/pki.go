package cluster

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/gorizond/d7y-cattle-operator/pkg/apply"
	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
	"github.com/gorizond/d7y-cattle-operator/pkg/pki"
)

const legacyServerSecretName = "dragonfly-p2p-legacy"

type secretCache interface {
	List(namespace string, selector labels.Selector) ([]*corev1.Secret, error)
}

const (
	// legacyTrustWindow keeps both CAs trusted long enough for every pod to
	// roll onto the new CA bundle.
	legacyTrustWindow = 24 * time.Hour
)

func signedByCA(certPEM []byte, ca *pki.CA) bool {
	certs, err := pki.ParseCertificates(certPEM)
	return err == nil && len(certs) > 0 && ca != nil && certs[0].CheckSignatureFrom(ca.Cert) == nil
}

// ensurePKI creates/loads the stable CA and ensures the shared client identity,
// the manager identity, and (for seeds) the per-seed scheduler identity and
// certificate HelmOp. It returns the possibly-annotated Cluster and whether the
// PKI path is ready.
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

	allowCreate := managerErr != nil && schedulerErr != nil
	if allowCreate && c.secretCache != nil {
		labeled, err := c.hasLabeledLeaves()
		if err != nil {
			return cluster, false, err
		}
		if labeled {
			allowCreate = false
		}
	}
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
	clientLeafValid := clientErr == nil && pki.KeyMatches(client.Data[corev1.TLSCertKey], client.Data[corev1.TLSPrivateKeyKey])
	if clientLeafValid {
		legacyCA = client.Data["ca.crt"]
	}
	ca, created, err := pki.EnsureCA(c.secrets, operatorNS, pki.CASecretName, allowCreate, legacyCA)
	if err != nil {
		c.event(cluster, corev1.EventTypeWarning, "PKICAUnavailable", err.Error())
		return cluster, false, err
	}

	clientLegacy := clientLeafValid && !signedByCA(client.Data[corev1.TLSCertKey], ca)

	deadline, err := c.caDeadline(operatorNS)
	if err != nil {
		return cluster, false, err
	}
	if deadline == "" && (created && len(legacyCA) > 0 || clientLegacy || bundleHasLegacy(ca)) {
		deadline = time.Now().Add(legacyTrustWindow).UTC().Format(time.RFC3339)
		if err := c.setCAAnnotation(operatorNS, pki.CAAnnotationLegacyTrustDeadline, deadline); err != nil {
			return cluster, false, err
		}
	}
	phaseA := false
	if deadline != "" {
		parsed, parseErr := time.Parse(time.RFC3339, deadline)
		if parseErr != nil {
			phaseA = true
			c.event(cluster, corev1.EventTypeWarning, "LegacyTrustDeadlineInvalid",
				fmt.Sprintf("invalid %s on CA Secret: %v; keeping Phase A trust overlap", pki.CAAnnotationLegacyTrustDeadline, parseErr))
		} else {
			phaseA = time.Now().Before(parsed)
		}
	}

	// Preserve the legacy server leaf before the client identity can be
	// re-signed. Server identities never read tls.crt/tls.key from the mutable
	// client Secret again after this point.
	legacyServer, err := c.ensureLegacyServerSecret(operatorNS, client, clientLeafValid, manager, scheduler, ca, phaseA)
	if err != nil {
		return cluster, false, err
	}

	if roles.Seed {
		clientOpts := pki.IssueOptions{CommonName: "d7y-cattle-operator-client", ClientAuth: true}
		reuseClient := phaseA && clientLegacy && clientLeafValid && pki.CertTrustedByBundle(client.Data[corev1.TLSCertKey], ca.BundlePEM)
		if _, err := pki.EnsureLeaf(c.secrets, operatorNS, c.opts.MTLSSecretName, ca, clientOpts, reuseClient); err != nil {
			c.event(cluster, corev1.EventTypeWarning, "MTLSSecretUnavailable", err.Error())
			return cluster, false, err
		}
		clientSecret, err := c.secrets.Get(operatorNS, c.opts.MTLSSecretName, metav1.GetOptions{})
		if err != nil {
			return cluster, false, fmt.Errorf("get ensured client Secret: %w", err)
		}
		if err := c.replicateSecret(clientSecret, cluster.Namespace); err != nil {
			return cluster, false, err
		}
	}

	if roles.Manager {
		managerOpts, err := c.managerIssueOptions()
		if err != nil {
			c.event(cluster, corev1.EventTypeWarning, "ManagerCertificateFailed", err.Error())
			return cluster, false, err
		}
		managerSecret, err := c.ensureServerLeaf(operatorNS, helmop.ManagerMTLSSecretName, legacyServer, ca, managerOpts, phaseA)
		if err != nil {
			c.event(cluster, corev1.EventTypeWarning, "MTLSSecretUnavailable", err.Error())
			return cluster, false, err
		}
		if err := c.replicateSecret(managerSecret, cluster.Namespace); err != nil {
			return cluster, false, err
		}
	}

	if roles.Seed {
		schedulerOpts, err := c.schedulerIssueOptions(cluster, seedIPs, seedDiscoveryFailed, scheduler)
		if err != nil {
			c.event(cluster, corev1.EventTypeWarning, "SchedulerHostUnavailable", err.Error())
			return cluster, false, err
		}
		scheduler, err = c.ensureServerLeaf(operatorNS, helmop.SchedulerSourceSecretName(cluster.Name), legacyServer, ca, schedulerOpts, phaseA)
		if err != nil {
			c.event(cluster, corev1.EventTypeWarning, "MTLSSecretUnavailable", err.Error())
			return cluster, false, err
		}
		certReady, err := c.ensureSchedulerCert(cluster, scheduler)
		if err != nil {
			c.event(cluster, corev1.EventTypeWarning, "SchedulerCertFailed", err.Error())
			return cluster, false, err
		}
		if certReady {
			cluster, err = c.setClusterAnnotation(cluster, helmop.AnnotationMTLSCertRevision, pki.SecretRevision(scheduler.Data))
			if err != nil {
				return cluster, false, err
			}
			// The new HelmOp release is ready, so the legacy raw Bundle can
			// be removed without a window where the Secret is absent.
			if err := c.deleteLegacySchedulerBundle(cluster); err != nil {
				return cluster, false, err
			}
		}
	}

	if !phaseA {
		if err := c.cleanupRetiredLegacyServer(operatorNS, ca); err != nil {
			return cluster, false, err
		}
	}
	return cluster, true, nil
}

func (c *Controller) cleanupRetiredLegacyServer(namespace string, ca *pki.CA) error {
	if c.secretCache == nil {
		return nil
	}
	legacy, err := c.secrets.Get(namespace, legacyServerSecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get legacy server Secret: %w", err)
	}
	leaves, err := c.secretCache.List(namespace, labels.SelectorFromSet(map[string]string{pki.LeafGenerationLabel: pki.LeafGenerationV2}))
	if err != nil {
		return fmt.Errorf("list leaves for legacy cleanup: %w", err)
	}
	for _, leaf := range leaves {
		if leaf == nil || leaf.Name == legacy.Name {
			continue
		}
		if !pki.CertTrustedByBundle(leaf.Data[corev1.TLSCertKey], ca.BundlePEM) {
			return nil
		}
	}
	if err := c.secrets.Delete(namespace, legacyServerSecretName, &metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete retired legacy server Secret: %w", err)
	}
	return nil
}

func (c *Controller) caDeadline(namespace string) (string, error) {
	secret, err := c.secrets.Get(namespace, pki.CASecretName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get CA Secret for deadline: %w", err)
	}
	if secret.Annotations != nil {
		if _, ok := secret.Annotations["kubectl.kubernetes.io/last-applied-configuration"]; ok {
			delete(secret.Annotations, "kubectl.kubernetes.io/last-applied-configuration")
			if _, err := c.secrets.Update(secret); err != nil {
				return "", fmt.Errorf("scrub CA Secret annotation: %w", err)
			}
		}
	}
	return strings.TrimSpace(secret.Annotations[pki.CAAnnotationLegacyTrustDeadline]), nil
}

func (c *Controller) setCAAnnotation(namespace, key, value string) error {
	secret, err := c.secrets.Get(namespace, pki.CASecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get CA Secret: %w", err)
	}
	if secret.Annotations == nil {
		secret.Annotations = map[string]string{}
	}
	if secret.Annotations[key] == value {
		return nil
	}
	secret.Annotations[key] = value
	delete(secret.Annotations, "kubectl.kubernetes.io/last-applied-configuration")
	secret.Labels = mergeStringMap(secret.Labels, map[string]string{
		pki.ManagedByLabel: pki.ManagedByValue,
	})
	if _, err := c.secrets.Update(secret); err != nil {
		return fmt.Errorf("update CA Secret annotations: %w", err)
	}
	return nil
}

func (c *Controller) ensureLegacyServerSecret(namespace string, client *corev1.Secret, clientValid bool, manager, scheduler *corev1.Secret, ca *pki.CA, phaseA bool) (*corev1.Secret, error) {
	current, err := c.secrets.Get(namespace, legacyServerSecretName, metav1.GetOptions{})
	if err == nil {
		activeFingerprint := pki.ShortFingerprint(ca.CertPEM)
		if current.Labels[pki.CAFingerprintLabel] == activeFingerprint && isBundleTrustedServer(current, ca) {
			return current, nil
		}
		if isBundleTrustedServer(current, ca) {
			// The leaf is still trusted; just refresh its active-CA marker.
			current.Labels = mergeStringMap(current.Labels, map[string]string{
				pki.CAFingerprintLabel:  activeFingerprint,
				pki.LeafGenerationLabel: pki.LeafGenerationV2,
				pki.ManagedByLabel:      pki.ManagedByValue,
			})
			return c.secrets.Update(current)
		}
		// A stale legacy leaf must never be minted into a new identity.
		source := firstTrustedServer(ca, manager, scheduler)
		if source == nil && phaseA && clientValid && isBundleTrustedServer(client, ca) {
			source = client
		}
		if source == nil {
			if err := c.secrets.Delete(namespace, legacyServerSecretName, &metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("delete stale legacy server Secret: %w", err)
			}
			return nil, nil
		}
		return c.refreshLegacyServer(current, source, ca, activeFingerprint)
	}
	if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("get legacy server Secret: %w", err)
	}
	if !phaseA || !clientValid || client == nil || client.Data == nil || signedByCA(client.Data[corev1.TLSCertKey], ca) || !isBundleTrustedServer(client, ca) {
		return nil, nil
	}
	return c.secrets.Create(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      legacyServerSecretName,
			Namespace: namespace,
			Labels: map[string]string{
				pki.LeafGenerationLabel: pki.LeafGenerationV2,
				pki.ManagedByLabel:      pki.ManagedByValue,
				pki.CAFingerprintLabel:  pki.ShortFingerprint(ca.CertPEM),
			},
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"ca.crt":                append([]byte(nil), ca.BundlePEM...),
			corev1.TLSCertKey:       append([]byte(nil), client.Data[corev1.TLSCertKey]...),
			corev1.TLSPrivateKeyKey: append([]byte(nil), client.Data[corev1.TLSPrivateKeyKey]...),
		},
	})
}

func (c *Controller) refreshLegacyServer(current, source *corev1.Secret, ca *pki.CA, fingerprint string) (*corev1.Secret, error) {
	current.Type = corev1.SecretTypeTLS
	current.Data = map[string][]byte{
		"ca.crt":                append([]byte(nil), ca.BundlePEM...),
		corev1.TLSCertKey:       append([]byte(nil), source.Data[corev1.TLSCertKey]...),
		corev1.TLSPrivateKeyKey: append([]byte(nil), source.Data[corev1.TLSPrivateKeyKey]...),
	}
	current.Labels = mergeStringMap(current.Labels, map[string]string{
		pki.CAFingerprintLabel:  fingerprint,
		pki.LeafGenerationLabel: pki.LeafGenerationV2,
		pki.ManagedByLabel:      pki.ManagedByValue,
	})
	delete(current.Annotations, "kubectl.kubernetes.io/last-applied-configuration")
	return c.secrets.Update(current)
}

func firstTrustedServer(ca *pki.CA, secrets ...*corev1.Secret) *corev1.Secret {
	for _, secret := range secrets {
		if isBundleTrustedServer(secret, ca) {
			return secret
		}
	}
	return nil
}

func isBundleTrustedServer(secret *corev1.Secret, ca *pki.CA) bool {
	if secret == nil || !pki.KeyMatches(secret.Data[corev1.TLSCertKey], secret.Data[corev1.TLSPrivateKeyKey]) {
		return false
	}
	certs, err := pki.ParseCertificates(secret.Data[corev1.TLSCertKey])
	if err != nil || len(certs) == 0 || !pki.HasServerAuth(certs[0]) || !pki.HasClientAuth(certs[0]) {
		return false
	}
	if ca != nil {
		return pki.CertTrustedByBundle(secret.Data[corev1.TLSCertKey], ca.BundlePEM)
	}
	return true
}

func getOptionalSecret(store apply.SecretStore, namespace, name string) (*corev1.Secret, error) {
	secret, err := store.Get(namespace, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	return secret, err
}

func (c *Controller) ensureServerLeaf(namespace, name string, legacy *corev1.Secret, ca *pki.CA, opts pki.IssueOptions, phaseA bool) (*corev1.Secret, error) {
	legacyValid := phaseA && serverLeafCovers(legacy, ca, opts)
	current, err := c.secrets.Get(namespace, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if legacyValid {
			return c.createLeafFrom(namespace, name, legacy, ca)
		}
		if _, err := pki.EnsureLeaf(c.secrets, namespace, name, ca, opts, false); err != nil {
			return nil, err
		}
		return c.secrets.Get(namespace, name, metav1.GetOptions{})
	}
	if err != nil {
		return nil, err
	}
	if legacyValid && !serverLeafCovers(current, ca, opts) {
		return c.repairLeafFrom(current, legacy, ca)
	}
	if legacyValid {
		return ensureLeafCABundle(c.secrets, current, ca)
	}
	if _, err := pki.EnsureLeaf(c.secrets, namespace, name, ca, opts, false); err != nil {
		return nil, err
	}
	return c.secrets.Get(namespace, name, metav1.GetOptions{})
}

func serverLeafCovers(secret *corev1.Secret, ca *pki.CA, opts pki.IssueOptions) bool {
	if secret == nil || !pki.KeyMatches(secret.Data[corev1.TLSCertKey], secret.Data[corev1.TLSPrivateKeyKey]) {
		return false
	}
	certs, err := pki.ParseCertificates(secret.Data[corev1.TLSCertKey])
	return err == nil && len(certs) > 0 && pki.HasServerAuth(certs[0]) && pki.HasClientAuth(certs[0]) &&
		pki.SANsCover(certs[0], opts.DNSNames, opts.IPAddresses) &&
		pki.CertTrustedByBundle(secret.Data[corev1.TLSCertKey], ca.BundlePEM)
}

func (c *Controller) createLeafFrom(namespace, name string, source *corev1.Secret, ca *pki.CA) (*corev1.Secret, error) {
	return c.secrets.Create(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				pki.LeafGenerationLabel: pki.LeafGenerationV2,
				pki.ManagedByLabel:      pki.ManagedByValue,
			},
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"ca.crt":                append([]byte(nil), ca.BundlePEM...),
			corev1.TLSCertKey:       append([]byte(nil), source.Data[corev1.TLSCertKey]...),
			corev1.TLSPrivateKeyKey: append([]byte(nil), source.Data[corev1.TLSPrivateKeyKey]...),
		},
	})
}

func (c *Controller) repairLeafFrom(current, source *corev1.Secret, ca *pki.CA) (*corev1.Secret, error) {
	current.Type = corev1.SecretTypeTLS
	current.Data = map[string][]byte{
		"ca.crt":                append([]byte(nil), ca.BundlePEM...),
		corev1.TLSCertKey:       append([]byte(nil), source.Data[corev1.TLSCertKey]...),
		corev1.TLSPrivateKeyKey: append([]byte(nil), source.Data[corev1.TLSPrivateKeyKey]...),
	}
	ensureLeafLabels(current)
	delete(current.Annotations, "kubectl.kubernetes.io/last-applied-configuration")
	return c.secrets.Update(current)
}

func ensureLeafCABundle(store apply.SecretStore, secret *corev1.Secret, ca *pki.CA) (*corev1.Secret, error) {
	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	changed := false
	if string(secret.Data["ca.crt"]) != string(ca.BundlePEM) {
		secret.Data["ca.crt"] = append([]byte(nil), ca.BundlePEM...)
		changed = true
	}
	if ensureLeafLabels(secret) {
		changed = true
	}
	if _, ok := secret.Annotations["kubectl.kubernetes.io/last-applied-configuration"]; ok {
		delete(secret.Annotations, "kubectl.kubernetes.io/last-applied-configuration")
		changed = true
	}
	if !changed {
		return secret, nil
	}
	return store.Update(secret)
}

func ensureLeafLabels(secret *corev1.Secret) bool {
	if secret.Labels == nil {
		secret.Labels = map[string]string{}
	}
	changed := false
	for key, value := range map[string]string{
		pki.LeafGenerationLabel: pki.LeafGenerationV2,
		pki.ManagedByLabel:      pki.ManagedByValue,
	} {
		if secret.Labels[key] != value {
			secret.Labels[key] = value
			changed = true
		}
	}
	return changed
}

func bundleHasLegacy(ca *pki.CA) bool {
	if ca == nil || len(ca.BundlePEM) == 0 {
		return false
	}
	certs, err := pki.ParseCertificates(ca.BundlePEM)
	return err == nil && len(certs) > 1
}

func (c *Controller) hasLabeledLeaves() (bool, error) {
	if c.secretCache == nil {
		return false, nil
	}
	selector := labels.SelectorFromSet(map[string]string{pki.LeafGenerationLabel: pki.LeafGenerationV2})
	secrets, err := c.secretCache.List(c.opts.OperatorNamespace, selector)
	if err != nil {
		return false, fmt.Errorf("list labeled PKI leaves: %w", err)
	}
	return len(secrets) > 0, nil
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
	if discoveryFailed {
		ips = append(ips, existingPublicIPs(existing)...)
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

func (c *Controller) ensureSchedulerCert(cluster *provv1.Cluster, secret *corev1.Secret) (bool, error) {
	if c.helmops == nil || c.bundles == nil || secret == nil {
		return false, fmt.Errorf("scheduler certificate stores are not configured")
	}
	op := helmop.SchedulerCert(helmop.Cluster{
		Name:               cluster.Name,
		Namespace:          cluster.Namespace,
		SchedulerCertChart: c.opts.SchedulerCertChart,
	}, string(secret.Data[corev1.TLSCertKey]), string(secret.Data[corev1.TLSPrivateKeyKey]), string(secret.Data["ca.crt"]))
	if _, err := apply.Ensure(c.ctx, c.helmops, op); err != nil {
		return false, fmt.Errorf("apply scheduler certificate HelmOp %s/%s: %w", cluster.Namespace, op.Name, err)
	}
	// Fleet creates the Bundle after the HelmOp. Treat absence as not ready
	// instead of an error so the first reconcile can finish and requeue.
	current := &unstructured.Unstructured{}
	err := c.bundles.Get(c.ctx, cluster.Namespace, op.Name, current, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read scheduler certificate Bundle %s/%s: %w", cluster.Namespace, op.Name, err)
	}
	return bundleReady(current), nil
}

func bundleReady(bundle *unstructured.Unstructured) bool {
	if bundle == nil {
		return false
	}
	generation := bundle.GetGeneration()
	if generation == 0 {
		return false
	}
	observed, _, _ := unstructured.NestedInt64(bundle.Object, "status", "observedGeneration")
	ready, _, _ := unstructured.NestedInt64(bundle.Object, "status", "summary", "ready")
	desired, _, _ := unstructured.NestedInt64(bundle.Object, "status", "summary", "desiredReady")
	return observed == generation && desired > 0 && ready == desired
}

// deleteLegacySchedulerBundle removes the pre-HelmOp raw scheduler Bundle only
// when it still carries this Cluster's ownership labels.
func (c *Controller) deleteLegacySchedulerBundle(cluster *provv1.Cluster) error {
	if c.bundles == nil || cluster == nil {
		return nil
	}
	name := helmop.SchedulerBundleName(cluster.Name)
	current := &unstructured.Unstructured{}
	err := c.bundles.Get(c.ctx, cluster.Namespace, name, current, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get legacy scheduler Bundle %s/%s: %w", cluster.Namespace, name, err)
	}
	labels := current.GetLabels()
	if labels["p2p-dragonfly-io"] != "p2p-mtls" || labels["p2p.dragonfly.io/cluster-name"] != cluster.Name {
		return nil
	}
	if _, err := apply.Delete(c.ctx, c.bundles, cluster.Namespace, name); err != nil {
		return fmt.Errorf("delete legacy scheduler Bundle: %w", err)
	}
	return nil
}

func (c *Controller) deleteSchedulerPKI(cluster *provv1.Cluster) error {
	if c == nil || cluster == nil {
		return nil
	}
	var errs []error
	if err := c.deleteHelmOp(cluster, helmop.SchedulerCertName(cluster.Name)); err != nil {
		errs = append(errs, err)
	}
	if err := c.deleteBundle(cluster, helmop.SchedulerCertName(cluster.Name)); err != nil {
		errs = append(errs, err)
	}
	if err := c.deleteLegacySchedulerBundle(cluster); err != nil {
		errs = append(errs, err)
	}
	if c.secrets != nil {
		name := helmop.SchedulerSourceSecretName(cluster.Name)
		if _, err := c.secrets.Get(c.opts.OperatorNamespace, name, metav1.GetOptions{}); err == nil {
			if err := c.secrets.Delete(c.opts.OperatorNamespace, name, &metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				errs = append(errs, fmt.Errorf("delete scheduler Secret: %w", err))
			}
		} else if !apierrors.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("get scheduler Secret before delete: %w", err))
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

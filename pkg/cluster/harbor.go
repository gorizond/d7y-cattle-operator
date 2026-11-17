package cluster

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/gorizond/d7y-cattle-operator/pkg/apply"
	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
)

// ensureHarbor reconciles the owned Harbor HelmOp. An external Harbor
// (harbor-url) deletes any previously managed release and is left to the user.
func (c *Controller) ensureHarbor(cluster *provv1.Cluster, hc helmop.Cluster, roles Roles) error {
	if !roles.Harbor {
		if err := c.deleteHelmOp(cluster, helmop.HarborName(cluster.Name)); err != nil {
			c.event(cluster, corev1.EventTypeWarning, "HarborCleanupFailed", err.Error())
			return err
		}
		return nil
	}
	if roles.HarborURL != "" {
		if err := validateHarborURL(roles.HarborURL); err != nil {
			c.event(cluster, corev1.EventTypeWarning, "HarborURLInvalid", err.Error())
			return err
		}
		if err := c.deleteHelmOp(cluster, helmop.HarborName(cluster.Name)); err != nil {
			c.event(cluster, corev1.EventTypeWarning, "HarborCleanupFailed", err.Error())
			return err
		}
		return nil
	}
	if err := validateHarborDomain(roles.HarborDomain); err != nil {
		c.event(cluster, corev1.EventTypeWarning, "HarborDomainMissing", err.Error())
		return err
	}
	if err := c.ensureHarborAdminSecret(cluster, hc); err != nil {
		return err
	}
	dbSecretName := helmop.HarborDBSecretName(cluster.Name)
	if err := c.ensureGeneratedSecret(cluster, dbSecretName, "password"); err != nil {
		return err
	}
	dbSecret, err := c.secrets.Get(cluster.Namespace, dbSecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read generated Harbor DB Secret %s/%s: %w", cluster.Namespace, dbSecretName, err)
	}
	if len(dbSecret.Data["password"]) == 0 {
		return fmt.Errorf("generated Harbor DB Secret %s/%s has no password", cluster.Namespace, dbSecretName)
	}
	hc.HarborDBPassword = string(dbSecret.Data["password"])
	if err := c.ensureGeneratedSecret(cluster, hc.HarborSecretKey, "secretKey"); err != nil {
		return err
	}
	return c.applyHelmOp(cluster, helmop.Harbor(hc))
}

// ensureRegistryProxy reconciles the independent proxy HelmOp. It requires
// either harbor-url or proxy.harborURL when the operator does not own Harbor.
func (c *Controller) ensureRegistryProxy(cluster *provv1.Cluster, hc helmop.Cluster, roles Roles, harborErr error) error {
	if !roles.Proxy {
		if err := c.deleteHelmOp(cluster, helmop.RegistryProxyName(cluster.Name)); err != nil {
			c.event(cluster, corev1.EventTypeWarning, "RegistryProxyCleanupFailed", err.Error())
			return err
		}
		return nil
	}
	if hc.HarborManaged && harborErr != nil {
		return fmt.Errorf("harbor is not ready: %w", harborErr)
	}
	if hc.ProxyPublicURL == "" {
		err := fmt.Errorf("proxy requires the proxy.domain operator value")
		c.event(cluster, corev1.EventTypeWarning, "RegistryProxyDomainMissing", err.Error())
		return err
	}
	if len(hc.ProxyRegistries) == 0 {
		err := fmt.Errorf("proxy requires a non-empty registry allowlist")
		c.event(cluster, corev1.EventTypeWarning, "RegistryProxyAllowlistMissing", err.Error())
		return err
	}
	if roles.HarborURL != "" {
		if err := validateHarborURL(roles.HarborURL); err != nil {
			c.event(cluster, corev1.EventTypeWarning, "HarborURLInvalid", err.Error())
			return err
		}
	}
	if hc.HarborURL == "" {
		err := fmt.Errorf("proxy requires p2p.dragonfly.io/harbor-url or proxy.harborURL")
		c.event(cluster, corev1.EventTypeWarning, "RegistryProxyHarborURLMissing", err.Error())
		return err
	}
	if err := c.ensureHarborAdminSecret(cluster, hc); err != nil {
		return err
	}
	if err := c.ensureGeneratedSecret(cluster, hc.ProxyWebhookSecretName, "secret"); err != nil {
		return err
	}
	return c.applyHelmOp(cluster, helmop.RegistryProxy(hc))
}

// ensureGeneratedSecret creates a per-cluster random Secret once and preserves
// it on later reconciles. Values are never logged or placed in HelmOp values.
func (c *Controller) ensureGeneratedSecret(cluster *provv1.Cluster, name, key string) error {
	if _, err := c.secrets.Get(cluster.Namespace, name, metav1.GetOptions{}); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get generated Secret %s/%s: %w", cluster.Namespace, name, err)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Errorf("generate Secret %s/%s: %w", cluster.Namespace, name, err)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cluster.Namespace,
			Labels:    apply.Labels(cluster.Namespace, cluster.Name),
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{key: []byte(hex.EncodeToString(raw))},
	}
	if _, err := c.secrets.Create(secret); err != nil {
		return fmt.Errorf("create generated Secret %s/%s: %w", cluster.Namespace, name, err)
	}
	return nil
}

// ensureHarborAdminSecret replicates the local Harbor admin Secret into the
// Cluster namespace under the per-cluster downstream name. The Secret values
// stay in the Kubernetes Secret; they are never copied into code or HelmOp
// values beyond the name.
func (c *Controller) ensureHarborAdminSecret(cluster *provv1.Cluster, hc helmop.Cluster) error {
	sourceName := c.opts.ProxyHarborAdminSecret
	if sourceName == "" {
		c.event(cluster, corev1.EventTypeWarning, "HarborAdminSecretMissing", "proxy-harbor-admin-secret is empty")
		return fmt.Errorf("proxy-harbor-admin-secret is empty")
	}
	source, err := c.secrets.Get(c.opts.OperatorNamespace, sourceName, metav1.GetOptions{})
	if err != nil {
		message := fmt.Sprintf("Secret %s/%s: %v", c.opts.OperatorNamespace, sourceName, err)
		c.event(cluster, corev1.EventTypeWarning, "HarborAdminSecretMissing", message)
		return fmt.Errorf("get Harbor admin secret: %w", err)
	}
	if len(source.Data["HARBOR_ADMIN_PASSWORD"]) == 0 {
		err := fmt.Errorf("Harbor admin Secret %s/%s has no HARBOR_ADMIN_PASSWORD", c.opts.OperatorNamespace, sourceName)
		c.event(cluster, corev1.EventTypeWarning, "HarborAdminSecretMissing", err.Error())
		return err
	}

	copied := source.DeepCopy()
	copied.Name = hc.HarborAdminSecret
	copied.Namespace = cluster.Namespace
	if copied.Labels == nil {
		copied.Labels = map[string]string{}
	}
	for key, value := range apply.Labels(cluster.Namespace, cluster.Name) {
		copied.Labels[key] = value
	}
	if _, err := apply.ReplicateSecret(copied, cluster.Namespace, c.secrets); err != nil {
		message := fmt.Sprintf("replicate Harbor admin Secret to %s/%s: %v", cluster.Namespace, copied.Name, err)
		c.event(cluster, corev1.EventTypeWarning, "HarborAdminSecretCopyFailed", message)
		return fmt.Errorf("replicate Harbor admin Secret to %s/%s: %w", cluster.Namespace, copied.Name, err)
	}
	return nil
}

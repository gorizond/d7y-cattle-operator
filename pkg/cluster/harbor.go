package cluster

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/gorizond/d7y-cattle-operator/pkg/apply"
	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
	"github.com/gorizond/d7y-cattle-operator/pkg/registryproxy"
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
	if err := c.ensureGeneratedSecret(cluster, dbSecretName, "password", 32); err != nil {
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
	if err := c.ensureGeneratedSecret(cluster, hc.HarborSecretKey, "secretKey", 16); err != nil {
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
	if err := c.ensureGeneratedSecret(cluster, hc.ProxyWebhookSecretName, "secret", 32); err != nil {
		return err
	}
	if err := c.ensureRegistryProxyRobotSecret(cluster, hc); err != nil {
		return err
	}
	// A rotated robot Secret must roll the proxy pods, which read the mounted
	// credentials only at bootstrap.
	hc.ProxyRobotSecretRevision = c.robotSecretRevision(cluster, hc.ProxyRobotSecretName)
	return c.applyHelmOp(cluster, helmop.RegistryProxy(hc))
}

// ensureRegistryProxyRobotSecret creates or refreshes the Harbor robot Secret
// in the Cluster (HelmOp) namespace. The proxy only reads the Secret that
// Fleet copies downstream; it never creates or deletes it.
func (c *Controller) ensureRegistryProxyRobotSecret(cluster *provv1.Cluster, hc helmop.Cluster) error {
	admin, err := c.secrets.Get(cluster.Namespace, hc.HarborAdminSecret, metav1.GetOptions{})
	if err != nil {
		message := fmt.Sprintf("read Harbor admin Secret %s/%s: %v", cluster.Namespace, hc.HarborAdminSecret, err)
		c.event(cluster, corev1.EventTypeWarning, "RegistryProxyRobotFailed", message)
		return fmt.Errorf("read Harbor admin Secret for robot provisioning: %w", err)
	}
	password := strings.TrimSpace(string(admin.Data["HARBOR_ADMIN_PASSWORD"]))
	if password == "" {
		err := fmt.Errorf("Harbor admin Secret %s/%s has no HARBOR_ADMIN_PASSWORD", cluster.Namespace, hc.HarborAdminSecret)
		c.event(cluster, corev1.EventTypeWarning, "RegistryProxyRobotFailed", err.Error())
		return err
	}
	username := strings.TrimSpace(string(admin.Data["username"]))
	if username == "" {
		username = "admin"
	}
	store := &namespaceSecretStore{
		secrets:   c.secrets,
		namespace: cluster.Namespace,
		labels:    apply.Labels(cluster.Namespace, cluster.Name),
	}
	ensurer := c.robotEnsurer
	if ensurer == nil {
		ensurer = harborRobotEnsurer{}
	}
	if err := ensurer.Ensure(c.ctx, hc.HarborURL, username, password, hc.ProxyRobotSecretName,
		helmop.RegistryProxyMirrorProject, hc.ProxyRegistries, store); err != nil {
		message := fmt.Sprintf("ensure Harbor robot Secret %s/%s: %v", cluster.Namespace, hc.ProxyRobotSecretName, err)
		c.event(cluster, corev1.EventTypeWarning, "RegistryProxyRobotFailed", message)
		return fmt.Errorf("ensure Harbor robot Secret %s/%s: %w", cluster.Namespace, hc.ProxyRobotSecretName, err)
	}
	return nil
}

// robotSecretRevision returns a short hash of the robot Secret values. It is
// only used as a pod-template annotation to roll the proxy after a credential
// rotation; the hash is never logged and carries no Secret data.
func (c *Controller) robotSecretRevision(cluster *provv1.Cluster, name string) string {
	secret, err := c.secrets.Get(cluster.Namespace, name, metav1.GetOptions{})
	if err != nil {
		return ""
	}
	digest := sha256.New()
	for _, key := range []string{"username", "password", "permissions-fingerprint"} {
		digest.Write([]byte(key))
		digest.Write([]byte{0})
		digest.Write(secret.Data[key])
		digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))[:16]
}

// registryProxySourceProjects maps the proxy registry allowlist to Harbor
// project names. "*" is preserved because Harbor grants a wildcard pull.
func registryProxySourceProjects(registries []string) []string {
	projects := make([]string, 0, len(registries))
	for _, registry := range registries {
		registry = strings.TrimSpace(registry)
		if registry == "" {
			continue
		}
		if registry == "*" {
			projects = append(projects, "*")
			continue
		}
		projects = append(projects, registryproxy.ProjectName(registry))
	}
	return projects
}

// robotEnsurer is the Harbor robot provisioning surface, injectable in tests.
type robotEnsurer interface {
	Ensure(ctx context.Context, harborURL, username, password, secretName, mirrorProject string, registries []string, store registryproxy.SecretStore) error
}

type harborRobotEnsurer struct{}

// Ensure creates the Harbor projects the robot references, then provisions the
// system robot and writes its credentials to store. Project creation is a
// prerequisite: the robot grants project-scoped permissions.
func (harborRobotEnsurer) Ensure(ctx context.Context, harborURL, username, password, secretName, mirrorProject string, registries []string, store registryproxy.SecretStore) error {
	harbor := &registryproxy.HarborClient{BaseURL: harborURL, Username: username, Password: password}
	if _, err := harbor.EnsureProject(ctx, mirrorProject, 0); err != nil {
		return fmt.Errorf("ensure Harbor mirror project %q: %w", mirrorProject, err)
	}
	provisioner := &registryproxy.Provisioner{Harbor: harbor, MirrorProject: mirrorProject}
	for _, registry := range registries {
		registry = strings.TrimSpace(registry)
		if registry == "" || registry == "*" {
			continue
		}
		if err := provisioner.Ensure(ctx, registry); err != nil {
			return err
		}
	}
	_, err := registryproxy.EnsureRobotSecret(ctx, harbor, secretName, mirrorProject, registryProxySourceProjects(registries), store)
	return err
}

// namespaceSecretStore writes the robot Secret into one namespace through the
// operator's typed Secret client. It never logs Secret data.
type namespaceSecretStore struct {
	secrets   apply.SecretStore
	namespace string
	labels    map[string]string
}

func (s *namespaceSecretStore) Get(_ context.Context, name string) (map[string][]byte, bool, error) {
	secret, err := s.secrets.Get(s.namespace, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return secret.Data, true, nil
}

func (s *namespaceSecretStore) Put(_ context.Context, name string, data map[string][]byte) error {
	current, err := s.secrets.Get(s.namespace, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = s.secrets.Create(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: s.namespace, Labels: cloneLabels(s.labels)},
			Type:       corev1.SecretTypeOpaque,
			Data:       cloneSecretData(data),
		})
		return err
	}
	if err != nil {
		return err
	}
	current.Data = cloneSecretData(data)
	if current.Labels == nil {
		current.Labels = map[string]string{}
	}
	for key, value := range s.labels {
		current.Labels[key] = value
	}
	_, err = s.secrets.Update(current)
	return err
}

func cloneLabels(labels map[string]string) map[string]string {
	copied := make(map[string]string, len(labels))
	for key, value := range labels {
		copied[key] = value
	}
	return copied
}

func cloneSecretData(data map[string][]byte) map[string][]byte {
	copied := make(map[string][]byte, len(data))
	for key, value := range data {
		copied[key] = append([]byte(nil), value...)
	}
	return copied
}

// ensureGeneratedSecret creates a per-cluster random Secret once and preserves
// it on later reconciles. Values are never logged or placed in HelmOp values.
func (c *Controller) ensureGeneratedSecret(cluster *provv1.Cluster, name, key string, rawBytes int) error {
	if _, err := c.secrets.Get(cluster.Namespace, name, metav1.GetOptions{}); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get generated Secret %s/%s: %w", cluster.Namespace, name, err)
	}
	raw := make([]byte, rawBytes)
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

package cluster

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
	"github.com/gorizond/d7y-cattle-operator/pkg/managerapi"
	"github.com/gorizond/d7y-cattle-operator/pkg/registryproxy"
)

const (
	harborPreheatInstanceName = "dragonfly"

	harborPreheatPATSecretName = "dragonfly-harbor-preheat"
	harborPreheatPATSecretKey  = "token"
	harborPreheatPATName       = "harbor-preheat"
	harborPreheatPATUserID     = 1
	harborPreheatPATScope      = "job"
	harborPreheatPATLifetime   = 10 // years
)

// preheatEnsurer is the Harbor preheat surface, injectable in tests.
type preheatEnsurer interface {
	EnsurePreheatInstance(ctx context.Context, name, endpoint, token string, insecure bool) error
}

// ensureHarborPreheat registers the Dragonfly manager as a Harbor P2P preheat
// provider ("Distribution"). It is not a registry adapter: Harbor never gets a
// dragonfly registry endpoint. Failures are surfaced as Cluster events and
// retried on the next reconcile.
func (c *Controller) ensureHarborPreheat(cluster *provv1.Cluster, hc helmop.Cluster) error {
	if err := c.reconcileHarborPreheat(cluster, hc); err != nil {
		c.event(cluster, corev1.EventTypeWarning, "HarborPreheatFailed", err.Error())
		return err
	}
	return nil
}

func (c *Controller) reconcileHarborPreheat(cluster *provv1.Cluster, hc helmop.Cluster) error {
	roles := ResolveRoles(cluster)
	if !roles.Harbor || hc.HarborURL == "" || c.opts.ManagerAPIURL == "" {
		return nil
	}

	admin, err := c.secrets.Get(cluster.Namespace, hc.HarborAdminSecret, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read Harbor admin Secret %s/%s: %w", cluster.Namespace, hc.HarborAdminSecret, err)
	}
	password := strings.TrimSpace(string(admin.Data["HARBOR_ADMIN_PASSWORD"]))
	if password == "" {
		return fmt.Errorf("Harbor admin Secret %s/%s has no HARBOR_ADMIN_PASSWORD", cluster.Namespace, hc.HarborAdminSecret)
	}
	username := strings.TrimSpace(string(admin.Data["username"]))
	if username == "" {
		username = "admin"
	}

	token, err := c.ensureHarborPreheatPAT()
	if err != nil {
		return err
	}
	ensurer := c.preheatEnsurer
	if ensurer == nil {
		ensurer = &registryproxy.HarborClient{
			BaseURL:  hc.HarborURL,
			Username: username,
			Password: password,
		}
	}
	if err := ensurer.EnsurePreheatInstance(c.ctx, harborPreheatInstanceName, c.opts.ManagerAPIURL, token, false); err != nil {
		return fmt.Errorf("ensure Harbor Dragonfly distribution: %w", err)
	}
	return nil
}

// ensureHarborPreheatPAT returns the manager personal access token used by
// Harbor's Dragonfly driver. The token is created once and reused from the
// operator-namespace Secret; it is never logged.
func (c *Controller) ensureHarborPreheatPAT() (string, error) {
	name := harborPreheatPATSecretName
	namespace := c.opts.OperatorNamespace
	secret, err := c.secrets.Get(namespace, name, metav1.GetOptions{})
	if err == nil {
		if token := strings.TrimSpace(string(secret.Data[harborPreheatPATSecretKey])); token != "" {
			return token, nil
		}
	} else if !apierrors.IsNotFound(err) {
		return "", fmt.Errorf("read Harbor preheat PAT Secret %s/%s: %w", namespace, name, err)
	}

	credentials, err := c.secrets.Get(namespace, c.opts.ManagerAPICredentialsSecret, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("read manager API credentials Secret %s/%s: %w", namespace, c.opts.ManagerAPICredentialsSecret, err)
	}
	client, err := managerapi.New(managerapi.Options{
		BaseURL:    c.opts.ManagerAPIURL,
		Username:   strings.TrimSpace(string(credentials.Data["username"])),
		Password:   string(credentials.Data["password"]),
		HTTPClient: c.managerHTTPClient,
	})
	if err != nil {
		return "", fmt.Errorf("manager API client: %w", err)
	}
	token, err := c.createHarborPreheatPAT(client)
	if errors.Is(err, managerapi.ErrConflict) {
		token, err = c.recreateHarborPreheatPAT(client)
	}
	if err != nil {
		return "", fmt.Errorf("create Harbor preheat personal access token: %w", err)
	}

	if secret == nil {
		created := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Type:       corev1.SecretTypeOpaque,
			Data:       map[string][]byte{harborPreheatPATSecretKey: []byte(token)},
		}
		if _, err := c.secrets.Create(created); err != nil {
			return "", fmt.Errorf("create Harbor preheat PAT Secret %s/%s: %w", namespace, name, err)
		}
		return token, nil
	}
	secret.Data[harborPreheatPATSecretKey] = []byte(token)
	if _, err := c.secrets.Update(secret); err != nil {
		return "", fmt.Errorf("update Harbor preheat PAT Secret %s/%s: %w", namespace, name, err)
	}
	return token, nil
}

func (c *Controller) createHarborPreheatPAT(client *managerapi.Client) (string, error) {
	return client.CreatePersonalAccessToken(c.ctx, harborPreheatPATName, harborPreheatPATUserID,
		[]string{harborPreheatPATScope}, time.Now().AddDate(harborPreheatPATLifetime, 0, 0))
}

// recreateHarborPreheatPAT recovers a manager 409 caused by a PAT carrying the
// operator's name while the local Secret is missing. Only exact-name records
// are deleted; the PAT is then created once more.
func (c *Controller) recreateHarborPreheatPAT(client *managerapi.Client) (string, error) {
	tokens, err := client.ListPersonalAccessTokens(c.ctx)
	if err != nil {
		return "", fmt.Errorf("list personal access tokens for Harbor preheat recovery: %w", err)
	}
	deleted := false
	for _, token := range tokens {
		if token.Name != harborPreheatPATName {
			continue
		}
		if err := client.DeletePersonalAccessToken(c.ctx, token.ID); err != nil && !errors.Is(err, managerapi.ErrNotFound) {
			return "", fmt.Errorf("delete stale Harbor preheat personal access token: %w", err)
		}
		deleted = true
	}
	if !deleted {
		return "", fmt.Errorf("recover Harbor preheat personal access token: %w", managerapi.ErrConflict)
	}
	return c.createHarborPreheatPAT(client)
}

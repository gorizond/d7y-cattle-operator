package cluster

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"
	"strings"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/gorizond/d7y-cattle-operator/pkg/apply"
)

const (
	managerCredentialsUsernameKey = "username"
	managerCredentialsPasswordKey = "password"
	managerCredentialsJWTKeyKey   = "jwt-key"

	managerRootUsername    = "root"
	managerPasswordLength  = 16
	managerPasswordMinLen  = 8
	managerPasswordMaxLen  = 20
	managerJWTKeyByteCount = 32
)

const managerPasswordAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// ensureManagerCredentials makes the operator-managed manager credentials
// Secret available to Fleet: the source of truth lives in the operator
// namespace and is copied into the Cluster (HelmOp) namespace. Fleet then
// delivers the copy downstream through spec.downstreamResources.
func (c *Controller) ensureManagerCredentials(cluster *provv1.Cluster) error {
	source, err := c.ensureManagerCredentialsSecret()
	if err != nil {
		return err
	}
	if _, err := apply.ReplicateSecret(source, cluster.Namespace, c.secrets); err != nil {
		return fmt.Errorf("replicate manager credentials Secret %s/%s: %w", cluster.Namespace, source.Name, err)
	}
	return nil
}

// ensureManagerCredentialsSecret returns the operator-namespace Secret with a
// valid root username, password, and JWT signing key. Missing values are
// generated; existing valid values are preserved. Secret values are never
// logged or returned in errors.
func (c *Controller) ensureManagerCredentialsSecret() (*corev1.Secret, error) {
	name := c.opts.ManagerAPICredentialsSecret
	namespace := c.opts.OperatorNamespace
	if name == "" {
		return nil, fmt.Errorf("manager API credentials Secret name is empty")
	}

	current, err := c.secrets.Get(namespace, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		generated, genErr := newManagerCredentialsSecret(namespace, name)
		if genErr != nil {
			return nil, fmt.Errorf("generate manager credentials Secret %s/%s: %w", namespace, name, genErr)
		}
		current, err = c.secrets.Create(generated)
		if err == nil {
			return current, nil
		}
		if !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("create manager credentials Secret %s/%s: %w", namespace, name, err)
		}
		// Another reconcile created it first; complete that copy instead.
		current, err = c.secrets.Get(namespace, name, metav1.GetOptions{})
	}
	if err != nil {
		return nil, fmt.Errorf("get manager credentials Secret %s/%s: %w", namespace, name, err)
	}
	return c.completeManagerCredentials(current)
}

// completeManagerCredentials fills missing or invalid keys while preserving
// credentials that are already usable by the manager.
func (c *Controller) completeManagerCredentials(secret *corev1.Secret) (*corev1.Secret, error) {
	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	changed := false

	if strings.TrimSpace(string(secret.Data[managerCredentialsUsernameKey])) == "" {
		secret.Data[managerCredentialsUsernameKey] = []byte(managerRootUsername)
		changed = true
	}
	if password := secret.Data[managerCredentialsPasswordKey]; len(password) < managerPasswordMinLen || len(password) > managerPasswordMaxLen {
		generated, err := randomManagerPassword()
		if err != nil {
			return nil, fmt.Errorf("generate manager password for Secret %s/%s: %w", secret.Namespace, secret.Name, err)
		}
		secret.Data[managerCredentialsPasswordKey] = []byte(generated)
		changed = true
	}
	if len(secret.Data[managerCredentialsJWTKeyKey]) == 0 {
		generated, err := randomManagerJWTKey()
		if err != nil {
			return nil, fmt.Errorf("generate manager JWT key for Secret %s/%s: %w", secret.Namespace, secret.Name, err)
		}
		secret.Data[managerCredentialsJWTKeyKey] = []byte(generated)
		changed = true
	}
	if !changed {
		return secret, nil
	}

	updated, err := c.secrets.Update(secret)
	if err != nil {
		return nil, fmt.Errorf("update manager credentials Secret %s/%s: %w", secret.Namespace, secret.Name, err)
	}
	return updated, nil
}

func newManagerCredentialsSecret(namespace, name string) (*corev1.Secret, error) {
	password, err := randomManagerPassword()
	if err != nil {
		return nil, err
	}
	jwtKey, err := randomManagerJWTKey()
	if err != nil {
		return nil, err
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Type:       corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			managerCredentialsUsernameKey: []byte(managerRootUsername),
			managerCredentialsPasswordKey: []byte(password),
			managerCredentialsJWTKeyKey:   []byte(jwtKey),
		},
	}, nil
}

// randomManagerPassword returns an alphanumeric password inside the manager's
// accepted 8-20 character range.
func randomManagerPassword() (string, error) {
	max := big.NewInt(int64(len(managerPasswordAlphabet)))
	var password strings.Builder
	password.Grow(managerPasswordLength)
	for i := 0; i < managerPasswordLength; i++ {
		index, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		password.WriteByte(managerPasswordAlphabet[index.Int64()])
	}
	return password.String(), nil
}

// randomManagerJWTKey returns 32 random bytes encoded as a 43-character
// base64url string without padding.
func randomManagerJWTKey() (string, error) {
	raw := make([]byte, managerJWTKeyByteCount)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

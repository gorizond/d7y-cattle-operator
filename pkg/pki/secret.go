package pki

import (
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SecretStore is the subset of Wrangler's generated SecretController used to
// ensure the mTLS Secret.
type SecretStore interface {
	Get(namespace, name string, opts metav1.GetOptions) (*corev1.Secret, error)
	Create(secret *corev1.Secret) (*corev1.Secret, error)
}

// EnsureSecret creates the mTLS Secret if it does not exist. It never
// overwrites an existing Secret and reports whether it created one.
func EnsureSecret(store SecretStore, namespace, name string, bundle *Bundle) (bool, error) {
	if bundle == nil {
		return false, errors.New("pki: bundle is nil")
	}

	_, err := store.Get(namespace, name, metav1.GetOptions{})
	if err == nil {
		return false, nil
	}
	if !apierrors.IsNotFound(err) {
		return false, fmt.Errorf("pki: get secret %s/%s: %w", namespace, name, err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"ca.crt":                bundle.CACertPEM,
			corev1.TLSCertKey:       bundle.CertPEM,
			corev1.TLSPrivateKeyKey: bundle.KeyPEM,
		},
	}
	if _, err := store.Create(secret); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return false, nil
		}
		return false, fmt.Errorf("pki: create secret %s/%s: %w", namespace, name, err)
	}
	return true, nil
}

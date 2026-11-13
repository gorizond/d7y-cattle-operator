package pki

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// EnsureSecret creates the mTLS Secret if it does not exist. It never
// overwrites an existing Secret and reports whether it created one.
func EnsureSecret(ctx context.Context, client kubernetes.Interface, namespace, name string, bundle *Bundle) (bool, error) {
	if bundle == nil {
		return false, errors.New("pki: bundle is nil")
	}

	_, err := client.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
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
	if _, err := client.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return false, nil
		}
		return false, fmt.Errorf("pki: create secret %s/%s: %w", namespace, name, err)
	}
	return true, nil
}

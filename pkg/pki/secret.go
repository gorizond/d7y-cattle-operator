package pki

import (
	"errors"
	"fmt"
	"net"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// lastAppliedAnnotation is written by kubectl/client-side apply and contains a
// full copy of the Secret, including tls.key. The operator never needs it and
// removes it whenever it touches the Secret.
const lastAppliedAnnotation = "kubectl.kubernetes.io/last-applied-configuration"

// SecretStore is the subset of Wrangler's generated SecretController used to
// ensure the mTLS Secret.
type SecretStore interface {
	Get(namespace, name string, opts metav1.GetOptions) (*corev1.Secret, error)
	Create(secret *corev1.Secret) (*corev1.Secret, error)
	Update(secret *corev1.Secret) (*corev1.Secret, error)
}

// Result describes what EnsureSecret changed.
type Result struct {
	Created  bool
	Updated  bool
	Revision string
}

// EnsureSecret creates or updates the mTLS Secret so its leaf carries the
// configured SAN set. The CA private key is not persisted; when the SAN set
// changes the caller's freshly generated CA+leaf bundle replaces the old
// material. A stable SAN set keeps the existing certificate, so normal
// reconciles never rotate it.
func EnsureSecret(store SecretStore, namespace, name string, bundle *Bundle) (Result, error) {
	if bundle == nil {
		return Result{}, errors.New("pki: bundle is nil")
	}

	desiredDNS, desiredIPs, err := bundleSANs(bundle)
	if err != nil {
		return Result{}, err
	}

	current, err := store.Get(namespace, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
			Type: corev1.SecretTypeTLS,
			Data: secretData(bundle),
		}
		if _, err := store.Create(secret); err != nil {
			if apierrors.IsAlreadyExists(err) {
				// Another replica created it between Get and Create; fall
				// through to a normal comparison on the next reconcile.
				return Result{Revision: CertRevision(bundle.CertPEM)}, nil
			}
			return Result{}, fmt.Errorf("pki: create secret %s/%s: %w", namespace, name, err)
		}
		return Result{Created: true, Revision: CertRevision(bundle.CertPEM)}, nil
	}
	if err != nil {
		return Result{}, fmt.Errorf("pki: get secret %s/%s: %w", namespace, name, err)
	}

	currentCertPEM := current.Data[corev1.TLSCertKey]
	certChanged := !certMatches(currentCertPEM, desiredDNS, desiredIPs)
	annotationRemoved := removeLastApplied(current)
	if !certChanged {
		revision := CertRevision(currentCertPEM)
		if annotationRemoved {
			if _, err := store.Update(current); err != nil {
				return Result{}, fmt.Errorf("pki: update secret %s/%s: %w", namespace, name, err)
			}
			return Result{Updated: true, Revision: revision}, nil
		}
		return Result{Revision: revision}, nil
	}

	current.Type = corev1.SecretTypeTLS
	current.Data = secretData(bundle)
	if _, err := store.Update(current); err != nil {
		return Result{}, fmt.Errorf("pki: update secret %s/%s: %w", namespace, name, err)
	}
	return Result{Updated: true, Revision: CertRevision(bundle.CertPEM)}, nil
}

func certMatches(certPEM []byte, dnsNames []string, ips []net.IP) bool {
	if len(certPEM) == 0 {
		return false
	}
	certs, err := ParseCertificates(certPEM)
	if err != nil || len(certs) == 0 {
		return false
	}
	return SANsMatch(certs[0], dnsNames, ips)
}

func bundleSANs(bundle *Bundle) ([]string, []net.IP, error) {
	if len(bundle.DNSNames) > 0 || len(bundle.IPAddresses) > 0 {
		return bundle.DNSNames, bundle.IPAddresses, nil
	}
	certs, err := ParseCertificates(bundle.CertPEM)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: parse generated certificate: %w", err)
	}
	return certs[0].DNSNames, certs[0].IPAddresses, nil
}

func removeLastApplied(secret *corev1.Secret) bool {
	if secret.Annotations == nil {
		return false
	}
	if _, ok := secret.Annotations[lastAppliedAnnotation]; !ok {
		return false
	}
	delete(secret.Annotations, lastAppliedAnnotation)
	return true
}

func secretData(bundle *Bundle) map[string][]byte {
	return map[string][]byte{
		"ca.crt":                append([]byte(nil), bundle.CACertPEM...),
		corev1.TLSCertKey:       append([]byte(nil), bundle.CertPEM...),
		corev1.TLSPrivateKeyKey: append([]byte(nil), bundle.KeyPEM...),
	}
}

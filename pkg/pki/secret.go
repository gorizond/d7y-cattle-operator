package pki

import (
	"crypto/ecdsa"
	"crypto/x509"
	"errors"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// lastAppliedAnnotation is written by kubectl/client-side apply and contains a
// full copy of the Secret, including tls.key. The operator never needs it and
// removes it whenever it touches the Secret.
const lastAppliedAnnotation = "kubectl.kubernetes.io/last-applied-configuration"

// SecretStore is the subset of Wrangler's generated SecretController used to
// ensure the CA and leaf Secrets.
type SecretStore interface {
	Get(namespace, name string, opts metav1.GetOptions) (*corev1.Secret, error)
	Create(secret *corev1.Secret) (*corev1.Secret, error)
	Update(secret *corev1.Secret) (*corev1.Secret, error)
}

// Result describes what EnsureCA/EnsureLeaf changed.
type Result struct {
	Created  bool
	Updated  bool
	Revision string
}

// EnsureCA reads the stable CA Secret, creating it only when allowCreate is
// true. Invalid existing material is returned as an error and is never
// regenerated. The create path is create-only: an AlreadyExists response is
// re-read and returned.
func EnsureCA(store SecretStore, namespace, name string, allowCreate bool, legacyCACertPEM []byte) (*CA, bool, error) {
	current, err := store.Get(namespace, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if !allowCreate {
			return nil, false, ErrCAUnavailable
		}
		ca, err := NewCA(legacyCACertPEM)
		if err != nil {
			return nil, false, err
		}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Type:       corev1.SecretTypeOpaque,
			Data:       ca.SecretData(),
		}
		if _, err := store.Create(secret); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return nil, false, fmt.Errorf("pki: create CA secret %s/%s: %w", namespace, name, err)
			}
			current, err = store.Get(namespace, name, metav1.GetOptions{})
			if err != nil {
				return nil, false, fmt.Errorf("pki: get CA secret %s/%s after create race: %w", namespace, name, err)
			}
			loaded, err := loadCASecret(current)
			return loaded, false, err
		}
		return ca, true, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("pki: get CA secret %s/%s: %w", namespace, name, err)
	}
	loaded, err := loadCASecret(current)
	return loaded, false, err
}

func loadCASecret(secret *corev1.Secret) (*CA, error) {
	if secret == nil {
		return nil, errors.New("pki: CA Secret is nil")
	}
	return LoadCA(secret.Data["ca.crt"], secret.Data[CAKey], secret.Data[CABundleKey])
}

// EnsureLeaf creates or updates a leaf Secret. When reuseExisting is true and
// a certificate/key already exist, only ca.crt is refreshed to the trust
// bundle; this is the Phase A migration path that keeps the legacy identity.
// Otherwise a certificate is re-signed when the CA signature, SAN set, or EKU
// set differs.
func EnsureLeaf(store SecretStore, namespace, name string, ca *CA, opts IssueOptions, reuseExisting bool) (Result, error) {
	if ca == nil {
		return Result{}, errors.New("pki: CA is nil")
	}
	current, err := store.Get(namespace, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		leaf, err := ca.Sign(opts)
		if err != nil {
			return Result{}, err
		}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: leafLabels()},
			Type:       corev1.SecretTypeTLS,
			Data:       leafData(leaf),
		}
		if _, err := store.Create(secret); err != nil {
			if apierrors.IsAlreadyExists(err) {
				current, err = store.Get(namespace, name, metav1.GetOptions{})
				if err != nil {
					return Result{}, fmt.Errorf("pki: get leaf secret %s/%s after create race: %w", namespace, name, err)
				}
				return ensureExistingLeaf(store, current, ca, opts, reuseExisting)
			}
			return Result{}, fmt.Errorf("pki: create leaf secret %s/%s: %w", namespace, name, err)
		}
		return Result{Created: true, Revision: SecretRevision(secret.Data)}, nil
	}
	if err != nil {
		return Result{}, fmt.Errorf("pki: get leaf secret %s/%s: %w", namespace, name, err)
	}
	return ensureExistingLeaf(store, current, ca, opts, reuseExisting)
}

func ensureExistingLeaf(store SecretStore, current *corev1.Secret, ca *CA, opts IssueOptions, reuseExisting bool) (Result, error) {
	if current == nil {
		return Result{}, errors.New("pki: leaf Secret is nil")
	}
	changed := removeLastApplied(current)
	if ensureLeafLabels(current) {
		changed = true
	}
	certPEM := current.Data[corev1.TLSCertKey]
	keyPEM := current.Data[corev1.TLSPrivateKeyKey]
	keyOK := keyMatches(certPEM, keyPEM)
	reuse := reuseExisting && len(certPEM) > 0 && keyOK
	if reuse && opts.ClientAuth && !certHasClientAuth(certPEM) {
		reuse = false
	}
	if !reuse && (!leafMatches(certPEM, ca, opts) || !keyOK) {
		leaf, err := ca.Sign(opts)
		if err != nil {
			return Result{}, err
		}
		current.Data = leafData(leaf)
		current.Type = corev1.SecretTypeTLS
		changed = true
	} else if len(current.Data["ca.crt"]) == 0 || string(current.Data["ca.crt"]) != string(ca.BundlePEM) {
		if current.Data == nil {
			current.Data = map[string][]byte{}
		}
		current.Data["ca.crt"] = append([]byte(nil), ca.BundlePEM...)
		changed = true
	}
	if changed {
		if _, err := store.Update(current); err != nil {
			return Result{}, fmt.Errorf("pki: update leaf secret %s/%s: %w", current.Namespace, current.Name, err)
		}
	}
	return Result{Updated: changed, Revision: SecretRevision(current.Data)}, nil
}

func certHasClientAuth(certPEM []byte) bool {
	certs, err := ParseCertificates(certPEM)
	return err == nil && len(certs) > 0 && HasClientAuth(certs[0])
}

func keyMatches(certPEM, keyPEM []byte) bool {
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		return false
	}
	certs, err := ParseCertificates(certPEM)
	if err != nil || len(certs) == 0 {
		return false
	}
	key, err := ParsePrivateKey(keyPEM)
	if err != nil {
		return false
	}
	public, ok := certs[0].PublicKey.(*ecdsa.PublicKey)
	return ok && public.Equal(&key.PublicKey)
}

func leafLabels() map[string]string {
	return map[string]string{
		LeafGenerationLabel: LeafGenerationV2,
		ManagedByLabel:      ManagedByValue,
	}
}

func ensureLeafLabels(secret *corev1.Secret) bool {
	if secret == nil {
		return false
	}
	if secret.Labels == nil {
		secret.Labels = map[string]string{}
	}
	changed := false
	for key, value := range leafLabels() {
		if secret.Labels[key] != value {
			secret.Labels[key] = value
			changed = true
		}
	}
	return changed
}

func leafMatches(certPEM []byte, ca *CA, opts IssueOptions) bool {
	if len(certPEM) == 0 {
		return false
	}
	certs, err := ParseCertificates(certPEM)
	if err != nil || len(certs) == 0 {
		return false
	}
	cert := certs[0]
	if err := cert.CheckSignatureFrom(ca.Cert); err != nil {
		return false
	}
	if !SANsMatch(cert, opts.DNSNames, opts.IPAddresses) {
		return false
	}
	return ekuMatches(cert, opts)
}

func ekuMatches(cert *x509.Certificate, opts IssueOptions) bool {
	want := []x509.ExtKeyUsage{}
	if opts.ServerAuth {
		want = append(want, x509.ExtKeyUsageServerAuth)
	}
	if opts.ClientAuth {
		want = append(want, x509.ExtKeyUsageClientAuth)
	}
	got := append([]x509.ExtKeyUsage(nil), cert.ExtKeyUsage...)
	slices.Sort(got)
	slices.Sort(want)
	return slices.Equal(got, want)
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

func leafData(bundle *Bundle) map[string][]byte {
	return map[string][]byte{
		"ca.crt":                append([]byte(nil), bundle.CACertPEM...),
		corev1.TLSCertKey:       append([]byte(nil), bundle.CertPEM...),
		corev1.TLSPrivateKeyKey: append([]byte(nil), bundle.KeyPEM...),
	}
}

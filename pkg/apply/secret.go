package apply

import (
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const lastAppliedAnnotation = "kubectl.kubernetes.io/last-applied-configuration"

// SecretStore is the subset of Wrangler's generated SecretController used to
// replicate Secrets between namespaces.
type SecretStore interface {
	Get(namespace, name string, opts metav1.GetOptions) (*corev1.Secret, error)
	Create(secret *corev1.Secret) (*corev1.Secret, error)
	Update(secret *corev1.Secret) (*corev1.Secret, error)
	Delete(namespace, name string, opts *metav1.DeleteOptions) error
}

// ReplicateSecret copies source into namespace and updates the copy when its
// type, data, or the private-key-bearing last-applied annotation changes. It
// reports whether anything was created or updated.
func ReplicateSecret(source *corev1.Secret, namespace string, store SecretStore) (bool, error) {
	if source == nil {
		return false, errors.New("apply: source secret is nil")
	}

	current, err := store.Get(namespace, source.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := store.Create(secretCopy(source, namespace)); err != nil {
			return false, fmt.Errorf("create secret %s/%s: %w", namespace, source.Name, err)
		}
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("get secret %s/%s: %w", namespace, source.Name, err)
	}

	changed := current.Type != source.Type || !equality.Semantic.DeepEqual(current.Data, source.Data)
	if current.Annotations != nil {
		if _, ok := current.Annotations[lastAppliedAnnotation]; ok {
			delete(current.Annotations, lastAppliedAnnotation)
			changed = true
		}
	}
	if !changed {
		return false, nil
	}
	current.Type = source.Type
	current.Data = copyData(source.Data)
	if _, err := store.Update(current); err != nil {
		return false, fmt.Errorf("update secret %s/%s: %w", namespace, source.Name, err)
	}
	return true, nil
}

func secretCopy(source *corev1.Secret, namespace string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      source.Name,
			Namespace: namespace,
			Labels:    source.Labels,
		},
		Type: source.Type,
		Data: copyData(source.Data),
	}
}

func copyData(data map[string][]byte) map[string][]byte {
	copied := make(map[string][]byte, len(data))
	for key, value := range data {
		copied[key] = append([]byte(nil), value...)
	}
	return copied
}

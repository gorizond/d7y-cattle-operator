package apply

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestReplicateSecret(t *testing.T) {
	store := newFakeSecretStore()
	source := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "dragonfly-p2p-mtls",
			Namespace: "dragonfly-system",
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "d7y-cattle-operator"},
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"ca.crt":                []byte("ca"),
			corev1.TLSCertKey:       []byte("cert"),
			corev1.TLSPrivateKeyKey: []byte("key"),
		},
	}

	created, err := ReplicateSecret(source, "fleet-default", store)
	if err != nil {
		t.Fatalf("ReplicateSecret() error = %v", err)
	}
	if !created {
		t.Error("ReplicateSecret() created = false, want true")
	}

	copy, err := store.Get("fleet-default", source.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get copy: %v", err)
	}
	if copy.Type != corev1.SecretTypeTLS {
		t.Errorf("copy type = %q", copy.Type)
	}
	if string(copy.Data[corev1.TLSCertKey]) != "cert" {
		t.Errorf("copy tls.crt = %q", copy.Data[corev1.TLSCertKey])
	}
	if copy.Labels["app.kubernetes.io/managed-by"] != "d7y-cattle-operator" {
		t.Errorf("copy labels = %v", copy.Labels)
	}

	created, err = ReplicateSecret(source, "fleet-default", store)
	if err != nil {
		t.Fatalf("ReplicateSecret(second) error = %v", err)
	}
	if created {
		t.Error("ReplicateSecret(second) created = true, want false")
	}

	rotated := source.DeepCopy()
	rotated.Data[corev1.TLSCertKey] = []byte("rotated")
	created, err = ReplicateSecret(rotated, "fleet-default", store)
	if err != nil {
		t.Fatalf("ReplicateSecret(rotation) error = %v", err)
	}
	if !created {
		t.Error("ReplicateSecret(rotation) created = false, want true")
	}
	copy, err = store.Get("fleet-default", source.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get copy: %v", err)
	}
	if string(copy.Data[corev1.TLSCertKey]) != "rotated" {
		t.Error("rotated Secret was not replicated")
	}
}

func TestReplicateSecretNilSource(t *testing.T) {
	if _, err := ReplicateSecret(nil, "ns", newFakeSecretStore()); err == nil {
		t.Fatal("ReplicateSecret(nil) error = nil, want an error")
	}
}

type fakeSecretStore struct {
	secrets map[string]*corev1.Secret
}

func newFakeSecretStore() *fakeSecretStore {
	return &fakeSecretStore{secrets: make(map[string]*corev1.Secret)}
}

func (f *fakeSecretStore) Get(namespace, name string, _ metav1.GetOptions) (*corev1.Secret, error) {
	secret, ok := f.secrets[namespace+"/"+name]
	if !ok {
		return nil, apierrors.NewNotFound(corev1.Resource("secrets"), name)
	}
	return secret.DeepCopy(), nil
}

func (f *fakeSecretStore) Create(secret *corev1.Secret) (*corev1.Secret, error) {
	key := secret.Namespace + "/" + secret.Name
	if _, ok := f.secrets[key]; ok {
		return nil, apierrors.NewAlreadyExists(corev1.Resource("secrets"), secret.Name)
	}
	f.secrets[key] = secret.DeepCopy()
	return secret.DeepCopy(), nil
}

func (f *fakeSecretStore) Update(secret *corev1.Secret) (*corev1.Secret, error) {
	key := secret.Namespace + "/" + secret.Name
	if _, ok := f.secrets[key]; !ok {
		return nil, apierrors.NewNotFound(corev1.Resource("secrets"), secret.Name)
	}
	f.secrets[key] = secret.DeepCopy()
	return secret.DeepCopy(), nil
}

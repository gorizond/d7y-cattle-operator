package registryproxy

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// SecretStore is the small Kubernetes Secret surface needed for the robot
// credentials. Admin and webhook secrets remain mounted files.
type SecretStore interface {
	Get(ctx context.Context, name string) (map[string][]byte, bool, error)
	Put(ctx context.Context, name string, data map[string][]byte) error
}

// KubeSecrets is the in-cluster SecretStore implementation.
type KubeSecrets struct {
	Client    kubernetes.Interface
	Namespace string
}

// NewKubeSecrets creates an in-cluster Kubernetes client.
func NewKubeSecrets(namespace string) (*KubeSecrets, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return &KubeSecrets{Client: client, Namespace: namespace}, nil
}

func (k *KubeSecrets) Get(ctx context.Context, name string) (map[string][]byte, bool, error) {
	secret, err := k.Client.CoreV1().Secrets(k.Namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return secret.Data, true, nil
}

func (k *KubeSecrets) Put(ctx context.Context, name string, data map[string][]byte) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: k.Namespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       data,
	}
	_, err := k.Client.CoreV1().Secrets(k.Namespace).Create(ctx, secret, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		current, getErr := k.Client.CoreV1().Secrets(k.Namespace).Get(ctx, name, metav1.GetOptions{})
		if getErr != nil {
			return getErr
		}
		current.Data = data
		_, err = k.Client.CoreV1().Secrets(k.Namespace).Update(ctx, current, metav1.UpdateOptions{})
	}
	return err
}

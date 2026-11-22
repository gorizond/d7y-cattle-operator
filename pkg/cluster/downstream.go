package cluster

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// schedulerPod is the downstream information needed to locate scheduler nodes.
type schedulerPod struct {
	NodeName string
	HostIP   string
}

type schedulerPodLister interface {
	ListSchedulerPods(ctx context.Context, kubeconfig []byte) ([]schedulerPod, error)
}

type schedulerSecretChecker interface {
	SecretExists(ctx context.Context, kubeconfig []byte, namespace, name string) (bool, error)
}

// KubeSchedulerPodLister reads the downstream kubeconfig to list scheduler
// pods and verify the scheduler Secret. The Rancher kubeconfig points at the
// downstream apiserver proxy.
type KubeSchedulerPodLister struct{}

func downstreamClient(kubeconfig []byte) (*kubernetes.Clientset, error) {
	cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("parse downstream kubeconfig: %w", err)
	}
	cfg.Timeout = 15 * time.Second
	cfg.UserAgent = "d7y-cattle-operator"
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("downstream client: %w", err)
	}
	return client, nil
}

func (KubeSchedulerPodLister) ListSchedulerPods(ctx context.Context, kubeconfig []byte) ([]schedulerPod, error) {
	client, err := downstreamClient(kubeconfig)
	if err != nil {
		return nil, err
	}
	pods, err := client.CoreV1().Pods("dragonfly-system").List(ctx, metav1.ListOptions{
		LabelSelector: "component=scheduler",
	})
	if err != nil {
		return nil, fmt.Errorf("list downstream scheduler pods: %w", err)
	}
	result := make([]schedulerPod, 0, len(pods.Items))
	for i := range pods.Items {
		result = append(result, schedulerPod{
			NodeName: pods.Items[i].Spec.NodeName,
			HostIP:   pods.Items[i].Status.HostIP,
		})
	}
	return result, nil
}

// SecretExists checks a Secret through the Rancher downstream kubeconfig.
func (KubeSchedulerPodLister) SecretExists(ctx context.Context, kubeconfig []byte, namespace, name string) (bool, error) {
	client, err := downstreamClient(kubeconfig)
	if err != nil {
		return false, err
	}
	_, err = client.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get downstream Secret %s/%s: %w", namespace, name, err)
	}
	return true, nil
}

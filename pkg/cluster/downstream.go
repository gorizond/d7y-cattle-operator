package cluster

import (
	"context"
	"fmt"
	"time"

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

// KubeSchedulerPodLister reads the downstream kubeconfig and lists scheduler
// pods. The Rancher kubeconfig points at the downstream apiserver proxy.
type KubeSchedulerPodLister struct{}

func (KubeSchedulerPodLister) ListSchedulerPods(ctx context.Context, kubeconfig []byte) ([]schedulerPod, error) {
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

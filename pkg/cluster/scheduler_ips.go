package cluster

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
	"github.com/gorizond/d7y-cattle-operator/pkg/pki"
)

const (
	AnnotationSchedulerIPs = "p2p.dragonfly.io/scheduler-ips"
)

type clusterCache interface {
	List(namespace string, selector labels.Selector) ([]*provv1.Cluster, error)
}

type helmOpWatcher interface {
	Watch(ctx context.Context, namespace string, opts metav1.ListOptions) (watch.Interface, error)
}

// ensureSchedulerIPs computes the scheduler IPs for a managed cluster from the
// scheduler-public-ip annotations of other gated seed clusters.
func (c *Controller) ensureSchedulerIPs(cluster *provv1.Cluster) (*provv1.Cluster, error) {
	if cluster == nil || c.clusterCache == nil || c.clusters == nil {
		return nil, nil
	}
	clusters, err := c.clusterCache.List(cluster.Namespace, labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("list managed clusters: %w", err)
	}
	seen := map[string]bool{}
	var ips []string
	for _, candidate := range clusters {
		if candidate == nil || candidate.Name == cluster.Name {
			continue
		}
		if candidate.Labels[LabelEnabled] != "true" || candidate.Annotations[AnnotationSeed] != "true" {
			continue
		}
		raw := strings.TrimSpace(candidate.Annotations[helmop.AnnotationSchedulerPublicIP])
		ip := net.ParseIP(raw)
		if ip == nil || !pki.IsPublicIP(ip) {
			continue
		}
		value := ip.String()
		if seen[value] {
			continue
		}
		seen[value] = true
		ips = append(ips, value)
	}
	sort.Strings(ips)
	desired := strings.Join(ips, ",")
	current := strings.TrimSpace(cluster.Annotations[AnnotationSchedulerIPs])
	if desired == current {
		return nil, nil
	}

	updated := cluster.DeepCopy()
	if desired == "" {
		delete(updated.Annotations, AnnotationSchedulerIPs)
	} else {
		if updated.Annotations == nil {
			updated.Annotations = map[string]string{}
		}
		updated.Annotations[AnnotationSchedulerIPs] = desired
	}
	result, err := c.clusters.Update(updated)
	if err != nil {
		return nil, fmt.Errorf("update scheduler IPs annotation: %w", err)
	}
	message := "scheduler IPs updated: " + desired
	if desired == "" {
		message = "scheduler IPs removed"
	}
	c.event(cluster, corev1.EventTypeNormal, "SchedulerIPsUpdated", message)
	return result, nil
}

func (c *Controller) enqueueGatedClusters() {
	if c.clusterCache == nil || c.clusters == nil {
		return
	}
	clusters, err := c.clusterCache.List(metav1.NamespaceAll, labels.Everything())
	if err != nil {
		return
	}
	for _, cluster := range clusters {
		if cluster != nil && cluster.Labels[LabelEnabled] == "true" {
			c.clusters.Enqueue(cluster.Namespace, cluster.Name)
		}
	}
}

func (c *Controller) watchHelmOps(watcher helmOpWatcher) {
	for {
		if c.ctx.Err() != nil {
			return
		}
		watch, err := watcher.Watch(c.ctx, metav1.NamespaceAll, metav1.ListOptions{})
		if err != nil {
			select {
			case <-c.ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		for range watch.ResultChan() {
			c.enqueueGatedClusters()
		}
		watch.Stop()
		select {
		case <-c.ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

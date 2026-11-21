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
	capiv1beta2 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	"github.com/gorizond/d7y-cattle-operator/pkg/pki"
)

const (
	AnnotationSchedulerIPs = "p2p.dragonfly.io/scheduler-ips"
	capiClusterName        = "cluster.x-k8s.io/cluster-name"
	rkeClusterName         = "rke.cattle.io/cluster-name"
	// legacySchedulerPublicIP is removed from gated Clusters during reconcile.
	legacySchedulerPublicIP = "p2p.dragonfly.io/scheduler-public-ip"
)

type clusterCache interface {
	List(namespace string, selector labels.Selector) ([]*provv1.Cluster, error)
}

type machineCache interface {
	List(namespace string, selector labels.Selector) ([]*capiv1beta2.Machine, error)
}

type helmOpWatcher interface {
	Watch(ctx context.Context, namespace string, opts metav1.ListOptions) (watch.Interface, error)
}

// ensureSchedulerIPs computes scheduler-ips for a managed cluster. A seed
// cluster publishes its own machine IPs; other gated clusters aggregate the
// scheduler-ips of other seed clusters. The legacy scheduler-public-ip
// annotation is removed during the same reconcile.
func (c *Controller) ensureSchedulerIPs(cluster *provv1.Cluster, roles Roles, seedIPs []string) (*provv1.Cluster, error) {
	if cluster == nil || c.clusters == nil {
		return nil, nil
	}
	seen := map[string]bool{}
	var ips []string
	if roles.Seed {
		for _, raw := range seedIPs {
			addPublicIPv4(&ips, seen, raw)
		}
	} else {
		if c.clusterCache == nil {
			return nil, nil
		}
		clusters, err := c.clusterCache.List(cluster.Namespace, labels.Everything())
		if err != nil {
			return nil, fmt.Errorf("list managed clusters: %w", err)
		}
		for _, candidate := range clusters {
			if candidate == nil || candidate.Name == cluster.Name {
				continue
			}
			if candidate.Labels[LabelEnabled] != "true" || candidate.Annotations[AnnotationSeed] != "true" {
				continue
			}
			for _, raw := range strings.Split(candidate.Annotations[AnnotationSchedulerIPs], ",") {
				addPublicIPv4(&ips, seen, raw)
			}
		}
	}
	sort.Strings(ips)
	desired := strings.Join(ips, ",")
	current := strings.TrimSpace(cluster.Annotations[AnnotationSchedulerIPs])
	_, hasLegacy := cluster.Annotations[legacySchedulerPublicIP]
	if desired == current && !hasLegacy {
		return nil, nil
	}

	updated := cluster.DeepCopy()
	delete(updated.Annotations, legacySchedulerPublicIP)
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

func addPublicIPv4(ips *[]string, seen map[string]bool, raw string) {
	ip := net.ParseIP(strings.TrimSpace(raw))
	if ip == nil || ip.To4() == nil || !pki.IsPublicIP(ip) {
		return
	}
	value := ip.String()
	if seen[value] {
		return
	}
	seen[value] = true
	*ips = append(*ips, value)
}

func (c *Controller) seedMachineIPs(cluster *provv1.Cluster) ([]string, error) {
	if cluster == nil || c.machines == nil {
		return nil, nil
	}
	machines, err := c.machines.List(cluster.Namespace, labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("list machines for seed IPs: %w", err)
	}
	seen := map[string]bool{}
	var ips []string
	for i := range machines {
		machine := machines[i]
		if machine == nil || machine.DeletionTimestamp != nil || machine.Namespace != cluster.Namespace {
			continue
		}
		clusterName := machine.Labels[capiClusterName]
		if clusterName == "" {
			clusterName = machine.Labels[rkeClusterName]
		}
		if clusterName != cluster.Name {
			continue
		}
		for _, address := range machine.Status.Addresses {
			if address.Type == capiv1beta2.MachineInternalIP || address.Type == capiv1beta2.MachineExternalIP {
				addPublicIPv4(&ips, seen, address.Address)
			}
		}
	}
	sort.Strings(ips)
	return ips, nil
}

func (c *Controller) machineChanged(key string, machine *capiv1beta2.Machine) (*capiv1beta2.Machine, error) {
	if machine == nil || c.clusters == nil {
		return machine, nil
	}
	c.enqueueGatedClusters()
	return machine, nil
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

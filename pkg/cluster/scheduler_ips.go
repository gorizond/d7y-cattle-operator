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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/watch"
	capiv1beta2 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	"k8s.io/apimachinery/pkg/labels"
)

// AnnotationSchedulerIPs carries the node IPs of other gated seed clusters.
const (
	AnnotationSchedulerIPs = "p2p.dragonfly.io/scheduler-ips"
	capiClusterName        = "cluster.x-k8s.io/cluster-name"
	rkeClusterName         = "rke.cattle.io/cluster-name"
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

// ensureSchedulerIPs computes the scheduler node IPs for a managed cluster from
// the machines of other gated seed clusters. The target cluster's own machines
// are excluded so a bootstrapping first seed does not depend on itself.
func (c *Controller) ensureSchedulerIPs(cluster *provv1.Cluster) (*provv1.Cluster, error) {
	if cluster == nil || c.clusterCache == nil || c.machines == nil || c.clusters == nil {
		return nil, nil
	}
	clusters, err := c.clusterCache.List(cluster.Namespace, labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("list managed clusters: %w", err)
	}
	seedClusters := map[string]bool{}
	for _, candidate := range clusters {
		if candidate.Labels[LabelEnabled] == "true" && candidate.Annotations[AnnotationSeed] == "true" {
			seedClusters[candidate.Name] = true
		}
	}

	machines, err := c.machines.List(cluster.Namespace, labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("list machines: %w", err)
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
		if clusterName == "" || clusterName == cluster.Name || !seedClusters[clusterName] {
			continue
		}
		for _, ip := range c.machineIPs(machine) {
			if seen[ip] {
				continue
			}
			seen[ip] = true
			ips = append(ips, ip)
		}
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

func (c *Controller) machineIPs(machine *capiv1beta2.Machine) []string {
	ips := machineAddressIPs(machine.Status.Addresses)
	if len(ips) > 0 || c.infra == nil {
		return ips
	}
	ref := machine.Spec.InfrastructureRef
	if ref.Kind != "HetznerMachine" || ref.Name == "" {
		return nil
	}
	obj := &unstructured.Unstructured{}
	if err := c.infra.Get(c.ctx, machine.Namespace, ref.Name, obj, metav1.GetOptions{}); err != nil {
		return nil
	}
	return objectAddressIPs(obj.Object)
}

func machineAddressIPs(addresses []capiv1beta2.MachineAddress) []string {
	var ips []string
	for _, address := range addresses {
		switch address.Type {
		case capiv1beta2.MachineInternalIP, capiv1beta2.MachineExternalIP:
			if ip := validIPv4(address.Address); ip != "" {
				ips = append(ips, ip)
			}
		}
	}
	return ips
}

func objectAddressIPs(object map[string]any) []string {
	raw, _, _ := unstructured.NestedSlice(object, "status", "addresses")
	var ips []string
	for _, item := range raw {
		address, ok := item.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := address["type"].(string)
		if kind != "InternalIP" && kind != "ExternalIP" {
			continue
		}
		value, _ := address["address"].(string)
		if ip := validIPv4(value); ip != "" {
			ips = append(ips, ip)
		}
	}
	if len(ips) > 0 {
		return ips
	}
	for _, field := range []string{"ipv4", "ipAddress", "ip", "serverIP", "publicIP"} {
		value, _, _ := unstructured.NestedString(object, "status", field)
		if ip := validIPv4(value); ip != "" {
			return []string{ip}
		}
	}
	return nil
}

func validIPv4(raw string) string {
	ip := net.ParseIP(strings.TrimSpace(raw))
	if ip == nil || ip.To4() == nil {
		return ""
	}
	return ip.String()
}

func (c *Controller) machineChanged(key string, machine *capiv1beta2.Machine) (*capiv1beta2.Machine, error) {
	if machine == nil || c.clusters == nil {
		return machine, nil
	}
	// Every managed cluster's annotation uses seed-machine addresses, so any
	// machine change can change every annotation.
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

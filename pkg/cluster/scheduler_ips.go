package cluster

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

// errNoSchedulerPods reports a transiently empty downstream scheduler pod list
// (operator restart, scheduler rollout). Discovery must preserve the published
// scheduler-ips annotation instead of clearing it.
var errNoSchedulerPods = errors.New("no downstream scheduler pods found")

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
// cluster publishes its own scheduler addresses (private ones included); other
// gated clusters aggregate the public scheduler-ips of other seed clusters.
// The legacy scheduler-public-ip annotation is removed during the same
// reconcile.
func (c *Controller) ensureSchedulerIPs(cluster *provv1.Cluster, roles Roles, seedIPs []string) (*provv1.Cluster, error) {
	if cluster == nil || c.clusters == nil {
		return nil, nil
	}
	seen := map[string]bool{}
	var ips []string
	if roles.Seed {
		// Preserve discovery order: the cluster's own scheduler addresses come
		// first, internal/private before external/public.
		for _, raw := range seedIPs {
			appendUsableIPv4(&ips, seen, raw)
		}
	} else {
		if c.clusterCache == nil {
			return nil, nil
		}
		other, err := c.otherSeedSchedulerIPs(cluster)
		if err != nil {
			return nil, err
		}
		ips = other
	}
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

// usableIPv4 returns the canonical form of raw when it is an IPv4 address that
// a peer can dial. Private (RFC1918) and public addresses are both accepted;
// loopback, link-local, unspecified, multicast and broadcast are rejected.
func usableIPv4(raw string) (string, bool) {
	ip := net.ParseIP(strings.TrimSpace(raw))
	if ip == nil {
		return "", false
	}
	ip4 := ip.To4()
	if ip4 == nil || !ip4.IsGlobalUnicast() {
		return "", false
	}
	return ip4.String(), true
}

// appendUsableIPv4 appends raw when it is a usable IPv4 address that is not
// already present. Order is preserved so callers control locality priority.
func appendUsableIPv4(ips *[]string, seen map[string]bool, raw string) {
	value, ok := usableIPv4(raw)
	if !ok || seen[value] {
		return
	}
	seen[value] = true
	*ips = append(*ips, value)
}

// addPublicIPv4 appends raw when it is a public IPv4 address. Private
// addresses from another cluster are not reachable externally and are dropped.
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

// schedulerIPGroups collects usable local IPv4 addresses into an internal
// group and an external group. The internal group is emitted first so the
// closest address stays first, then each group is sorted for determinism.
type schedulerIPGroups struct {
	internal []string
	external []string
	seen     map[string]bool
}

func (g *schedulerIPGroups) add(raw string, internal bool) bool {
	value, ok := usableIPv4(raw)
	if !ok {
		return false
	}
	if g.seen == nil {
		g.seen = map[string]bool{}
	}
	if !g.seen[value] {
		g.seen[value] = true
		if internal {
			g.internal = append(g.internal, value)
		} else {
			g.external = append(g.external, value)
		}
	}
	return true
}

func (g *schedulerIPGroups) addInternal(raw string) bool { return g.add(raw, true) }
func (g *schedulerIPGroups) addExternal(raw string) bool { return g.add(raw, false) }

func (g *schedulerIPGroups) ips() []string {
	sort.Strings(g.internal)
	sort.Strings(g.external)
	return append(g.internal, g.external...)
}

// seedMachineIPs returns the usable IPv4 addresses of the seed Cluster's CAPI
// Machines, internal addresses first. It is the fallback when the downstream
// kubeconfig cannot be used; private addresses are valid for the local
// scheduler.
func (c *Controller) seedMachineIPs(cluster *provv1.Cluster) ([]string, error) {
	if cluster == nil || c.machines == nil {
		return nil, nil
	}
	machines, err := c.machines.List(cluster.Namespace, labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("list machines for seed IPs: %w", err)
	}
	var groups schedulerIPGroups
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
			switch address.Type {
			case capiv1beta2.MachineInternalIP:
				groups.addInternal(address.Address)
			case capiv1beta2.MachineExternalIP:
				groups.addExternal(address.Address)
			}
		}
	}
	return groups.ips(), nil
}

// downstreamAccessDenied reports whether a downstream API error means the
// Rancher kubeconfig identity is not allowed to use the cluster proxy.
func downstreamAccessDenied(err error) bool {
	if err == nil {
		return false
	}
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		return true
	}
	var status apierrors.APIStatus
	if errors.As(err, &status) {
		code := status.Status().Code
		if code == http.StatusForbidden || code == http.StatusUnauthorized {
			return true
		}
	}
	// Rancher's cluster proxy can return its APIError JSON as a raw/wrapped
	// body. The k8s client then produces a generic error whose text contains
	// these markers instead of a typed StatusError.
	text := strings.ToLower(err.Error())
	for _, marker := range []string{
		`"status":403`, `"status":401`,
		`"code":"forbidden"`, `"code":"unauthorized"`,
		" is forbidden: ", "cannot get resource", "cannot list resource",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// localSchedulerPodIPs follows the precise path: list scheduler pods through
// the Rancher kubeconfig and map them to the seed's CAPI Machines.
func (c *Controller) localSchedulerPodIPs(cluster *provv1.Cluster) ([]string, error) {
	if cluster == nil {
		return nil, nil
	}
	if c.downstream == nil || c.secrets == nil || c.machines == nil {
		return nil, fmt.Errorf("downstream scheduler discovery is not configured")
	}
	name := cluster.Name + "-kubeconfig"
	kubeconfigSecret, err := c.secrets.Get(cluster.Namespace, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("read kubeconfig Secret %s/%s: %w", cluster.Namespace, name, err)
	}
	kubeconfig := kubeconfigSecret.Data["value"]
	if len(kubeconfig) == 0 {
		return nil, fmt.Errorf("kubeconfig Secret %s/%s has no value", cluster.Namespace, name)
	}
	pods, err := c.downstream.ListSchedulerPods(c.ctx, kubeconfig)
	if err != nil {
		return nil, err
	}
	if len(pods) == 0 {
		return nil, errNoSchedulerPods
	}
	nodes, err := c.downstream.ListSchedulerNodes(c.ctx, kubeconfig)
	if err != nil {
		return nil, err
	}

	machines, err := c.machines.List(cluster.Namespace, labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("list machines for scheduler nodes: %w", err)
	}
	byNode := map[string]*capiv1beta2.Machine{}
	byIP := map[string]*capiv1beta2.Machine{}
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
		if machine.Status.NodeRef.Name != "" {
			byNode[machine.Status.NodeRef.Name] = machine
		}
		for _, address := range machine.Status.Addresses {
			if address.Type != capiv1beta2.MachineInternalIP && address.Type != capiv1beta2.MachineExternalIP {
				continue
			}
			if ip := net.ParseIP(strings.TrimSpace(address.Address)); ip != nil {
				byIP[ip.String()] = machine
			}
		}
	}

	var groups schedulerIPGroups
	for _, pod := range pods {
		node, hasNode := nodes[pod.NodeName]
		machine := byNode[pod.NodeName]
		if machine == nil {
			identity := firstInternalIP(node.InternalIPs)
			if identity == "" {
				identity = pod.HostIP
			}
			if ip := net.ParseIP(strings.TrimSpace(identity)); ip != nil {
				machine = byIP[ip.String()]
			}
		}
		if hasNode {
			// The Node is authoritative and may expose a private InternalIP
			// (the same isolated network reaches it) or a public one on
			// drivers such as Hetzner. Internal addresses stay first.
			published := false
			for _, raw := range node.InternalIPs {
				if groups.addInternal(raw) {
					published = true
				}
			}
			for _, raw := range node.ExternalIPs {
				if groups.addExternal(raw) {
					published = true
				}
			}
			if published {
				continue
			}
		}
		if machine == nil {
			continue
		}
		// No usable Node address: the Machine is the last-resort source.
		for _, address := range machine.Status.Addresses {
			switch address.Type {
			case capiv1beta2.MachineInternalIP:
				groups.addInternal(address.Address)
			case capiv1beta2.MachineExternalIP:
				groups.addExternal(address.Address)
			}
		}
	}
	return groups.ips(), nil
}

// firstInternalIP returns the first parseable InternalIP, which identifies the
// downstream Node when matching it to a CAPI Machine.
func firstInternalIP(addresses []string) string {
	for _, raw := range addresses {
		if ip := net.ParseIP(strings.TrimSpace(raw)); ip != nil {
			return ip.String()
		}
	}
	return ""
}

// otherSeedSchedulerIPs returns the PUBLIC scheduler IPs published by other
// gated seed Clusters. Private addresses are not reachable from another
// cluster, so they are never borrowed; external sharing is only a fallback.
// A new seed's scheduler runs in a reachable seed before its own RKE2 exists,
// so this is the correct fallback when local discovery is unavailable or
// empty.
func (c *Controller) otherSeedSchedulerIPs(cluster *provv1.Cluster) ([]string, error) {
	if cluster == nil || c.clusterCache == nil {
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
		for _, raw := range strings.Split(candidate.Annotations[AnnotationSchedulerIPs], ",") {
			addPublicIPv4(&ips, seen, raw)
		}
	}
	sort.Strings(ips)
	return ips, nil
}

func (c *Controller) schedulerFallback(cluster *provv1.Cluster, cause error, source string) {
	if cause != nil {
		slog.Warn("local scheduler pod discovery failed; using fallback scheduler IPs",
			"cluster", cluster.Namespace+"/"+cluster.Name,
			"source", source,
			"access_denied", downstreamAccessDenied(cause),
			"error_type", fmt.Sprintf("%T", cause),
			"error", cause,
		)
		c.event(cluster, corev1.EventTypeWarning, "SchedulerIPsFallback",
			"local scheduler pod discovery failed; using scheduler IPs from "+source)
		return
	}
	slog.Warn("local scheduler pods are absent; using fallback scheduler IPs",
		"cluster", cluster.Namespace+"/"+cluster.Name, "source", source)
	c.event(cluster, corev1.EventTypeWarning, "SchedulerIPsFallback",
		"local scheduler pods are absent; using scheduler IPs from "+source)
}

func (c *Controller) discoverSeedSchedulerIPs(cluster *provv1.Cluster) ([]string, error) {
	if cluster == nil {
		return nil, nil
	}
	local, localErr := c.localSchedulerPodIPs(cluster)
	if localErr == nil && len(local) > 0 {
		return local, nil
	}

	// A new seed's scheduler runs in a reachable seed until its own RKE2
	// exists. Prefer another gated seed's published scheduler IPs before
	// falling back to this seed's own machines.
	other, otherErr := c.otherSeedSchedulerIPs(cluster)
	if otherErr == nil && len(other) > 0 {
		c.schedulerFallback(cluster, localErr, "another gated seed")
		return other, nil
	}

	// If the local scheduler pods cannot be read at all (missing kubeconfig or
	// 401/403), the seed's own CAPI machine IPs are the last resort; private
	// addresses are valid for the local scheduler. Do not use them for a
	// transiently empty pod list: the scheduler may intentionally live in
	// another seed, and the existing annotation must be preserved until the
	// pods reappear.
	if localErr != nil {
		if !errors.Is(localErr, errNoSchedulerPods) {
			machines, machineErr := c.seedMachineIPs(cluster)
			if machineErr == nil && len(machines) > 0 {
				c.schedulerFallback(cluster, localErr, "the seed's CAPI machine IPs")
				return machines, nil
			}
		}
		return nil, localErr
	}
	if otherErr != nil {
		return nil, otherErr
	}
	return nil, nil
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

func (c *Controller) watchBundles(watcher helmOpWatcher) {
	for {
		if c.ctx.Err() != nil {
			return
		}
		watch, err := watcher.Watch(c.ctx, metav1.NamespaceAll, metav1.ListOptions{
			LabelSelector: "p2p-dragonfly-io=p2p-mtls",
		})
		if err != nil {
			select {
			case <-c.ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		for event := range watch.ResultChan() {
			obj, ok := event.Object.(metav1.Object)
			if !ok {
				continue
			}
			labels := obj.GetLabels()
			name := labels["p2p.dragonfly.io/cluster-name"]
			namespace := labels["p2p.dragonfly.io/cluster-namespace"]
			if name != "" && namespace != "" && c.clusters != nil {
				c.clusters.Enqueue(namespace, name)
			}
		}
		watch.Stop()
		select {
		case <-c.ctx.Done():
			return
		case <-time.After(time.Second):
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

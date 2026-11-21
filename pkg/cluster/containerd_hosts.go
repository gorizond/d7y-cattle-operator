package cluster

import (
	"fmt"
	"strings"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	rkev1 "github.com/rancher/rancher/pkg/apis/rke.cattle.io/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/gorizond/d7y-cattle-operator/pkg/apply"
	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
)

const (
	containerdHostsSuffix      = "-containerd-hosts"
	containerdHostsAuthorized  = "rke.cattle.io/object-authorized-for-clusters"
	containerdHostsPermissions = "0644"
)

// configMapStore is the subset of Wrangler's ConfigMapController used to
// reconcile the per-cluster containerd hosts ConfigMap.
type configMapStore interface {
	Get(namespace, name string, opts metav1.GetOptions) (*corev1.ConfigMap, error)
	Create(*corev1.ConfigMap) (*corev1.ConfigMap, error)
	Update(*corev1.ConfigMap) (*corev1.ConfigMap, error)
}

// ensureContainerdHosts creates the authorized ConfigMap and injects its file
// source into spec.rkeConfig.machineSelectorFiles. It returns the updated
// Cluster only when the spec changed.
func (c *Controller) ensureContainerdHosts(cluster *provv1.Cluster) (*provv1.Cluster, error) {
	if cluster == nil {
		return nil, nil
	}
	if c.configmaps == nil {
		return nil, fmt.Errorf("ConfigMap client is not configured")
	}
	port := c.opts.ContainerdMirrorPort
	if port == 0 {
		port = helmop.MirrorPort
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("containerd mirror port %d is outside 1-65535", port)
	}

	name := containerdHostsName(cluster.Name)
	if err := c.ensureContainerdHostsConfigMap(cluster, name, port); err != nil {
		return nil, err
	}

	updated := cluster.DeepCopy()
	if updated.Spec.RKEConfig == nil {
		updated.Spec.RKEConfig = &provv1.RKEConfig{}
	}
	includeDefault := strings.TrimSpace(cluster.Annotations[AnnotationSchedulerIPs]) != ""
	files, changed := withContainerdHostsSelector(
		updated.Spec.RKEConfig.MachineSelectorFiles,
		name,
		containerdCertsDir(updated.Spec.KubernetesVersion),
		includeDefault,
	)
	if !changed {
		return nil, nil
	}
	updated.Spec.RKEConfig.MachineSelectorFiles = files
	if c.clusters == nil {
		return nil, fmt.Errorf("Cluster client is not configured")
	}
	result, err := c.clusters.Update(updated)
	if err != nil {
		return nil, fmt.Errorf("patch Cluster machineSelectorFiles: %w", err)
	}
	return result, nil
}

func (c *Controller) ensureContainerdHostsConfigMap(cluster *provv1.Cluster, name string, port int) error {
	controller := true
	desired := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cluster.Namespace,
			Labels:    apply.Labels(cluster.Namespace, cluster.Name),
			Annotations: map[string]string{
				containerdHostsAuthorized: cluster.Name,
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "provisioning.cattle.io/v1",
				Kind:       "Cluster",
				Name:       cluster.Name,
				UID:        cluster.UID,
				Controller: &controller,
			}},
		},
		Data: containerdHostsData(port),
	}

	current, err := c.configmaps.Get(cluster.Namespace, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := c.configmaps.Create(desired); err != nil {
			return fmt.Errorf("create ConfigMap %s/%s: %w", cluster.Namespace, name, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get ConfigMap %s/%s: %w", cluster.Namespace, name, err)
	}

	updated := current.DeepCopy()
	labels := mergeStringMap(current.Labels, desired.Labels)
	annotations := mergeStringMap(current.Annotations, desired.Annotations)
	changed := !equality.Semantic.DeepEqual(current.Data, desired.Data) ||
		!equality.Semantic.DeepEqual(current.Labels, labels) ||
		!equality.Semantic.DeepEqual(current.Annotations, annotations) ||
		!equality.Semantic.DeepEqual(current.OwnerReferences, desired.OwnerReferences)
	if !changed {
		return nil
	}
	updated.Data = desired.Data
	updated.Labels = labels
	updated.Annotations = annotations
	updated.OwnerReferences = desired.OwnerReferences
	if _, err := c.configmaps.Update(updated); err != nil {
		return fmt.Errorf("update ConfigMap %s/%s: %w", cluster.Namespace, name, err)
	}
	return nil
}

func containerdHostsName(clusterName string) string {
	return clusterName + containerdHostsSuffix
}

func containerdCertsDir(kubernetesVersion string) string {
	if strings.Contains(strings.ToLower(kubernetesVersion), "+rke2") {
		return "/var/lib/rancher/rke2/agent/etc/containerd/certs.d"
	}
	return "/var/lib/rancher/k3s/agent/etc/containerd/certs.d"
}

func containerdHostsData(port int) map[string]string {
	mirror := fmt.Sprintf("http://127.0.0.1:%d", port)
	direct := fmt.Sprintf(`server = "https://registry-1.docker.io"

[host.%q]
  capabilities = ["pull", "resolve"]
  [host.%q.header]
    X-Dragonfly-Registry = ["https://registry-1.docker.io"]

[host."https://registry-1.docker.io"]
  capabilities = ["pull", "resolve"]
`, mirror, mirror)
	return map[string]string{
		"_default": fmt.Sprintf(`server = %q

[host.%q]
  capabilities = ["pull", "resolve"]
`, mirror, mirror),
		"docker.io":       direct,
		"index.docker.io": direct,
	}
}

func withContainerdHostsSelector(files []rkev1.RKEProvisioningFiles, name, dir string, includeDefault bool) ([]rkev1.RKEProvisioningFiles, bool) {
	if containerdHostsHasDefault(files, name) {
		includeDefault = true
	}
	desired := containerdHostsFileSource(name, dir, includeDefault)
	out := make([]rkev1.RKEProvisioningFiles, 0, len(files)+1)
	reused := false
	for i := range files {
		entry := *files[i].DeepCopy()
		filtered := make([]rkev1.ProvisioningFileSource, 0, len(entry.FileSources))
		found := false
		for _, source := range entry.FileSources {
			if source.ConfigMap.Name == name {
				found = true
				continue
			}
			filtered = append(filtered, source)
		}
		if !found {
			out = append(out, entry)
			continue
		}
		if len(filtered) == 0 {
			if reused {
				continue
			}
			entry.MachineLabelSelector = &metav1.LabelSelector{}
			entry.FileSources = []rkev1.ProvisioningFileSource{desired}
			out = append(out, entry)
			reused = true
			continue
		}
		entry.FileSources = filtered
		out = append(out, entry)
	}
	if !reused {
		out = append(out, rkev1.RKEProvisioningFiles{
			MachineLabelSelector: &metav1.LabelSelector{},
			FileSources:          []rkev1.ProvisioningFileSource{desired},
		})
	}
	return out, !equality.Semantic.DeepEqual(files, out)
}

func containerdHostsFileSource(name, dir string, includeDefault bool) rkev1.ProvisioningFileSource {
	items := make([]rkev1.KeyToPath, 0, 3)
	if includeDefault {
		items = append(items, rkev1.KeyToPath{Key: "_default", Path: dir + "/_default/hosts.toml"})
	}
	items = append(items,
		rkev1.KeyToPath{Key: "docker.io", Path: dir + "/docker.io/hosts.toml"},
		rkev1.KeyToPath{Key: "index.docker.io", Path: dir + "/index.docker.io/hosts.toml"},
	)
	return rkev1.ProvisioningFileSource{ConfigMap: rkev1.K8sObjectFileSource{
		Name:               name,
		DefaultPermissions: containerdHostsPermissions,
		Items:              items,
	}}
}

func containerdHostsHasDefault(files []rkev1.RKEProvisioningFiles, name string) bool {
	for i := range files {
		for _, source := range files[i].FileSources {
			if source.ConfigMap.Name != name {
				continue
			}
			for _, item := range source.ConfigMap.Items {
				if item.Key == "_default" {
					return true
				}
			}
		}
	}
	return false
}

func mergeStringMap(current, desired map[string]string) map[string]string {
	if len(desired) == 0 {
		return current
	}
	merged := make(map[string]string, len(current)+len(desired))
	for key, value := range current {
		merged[key] = value
	}
	for key, value := range desired {
		merged[key] = value
	}
	return merged
}

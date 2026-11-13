package cluster

import (
	"fmt"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/gorizond/d7y-cattle-operator/pkg/apply"
	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
)

type vpaTarget struct {
	name   string
	kind   string
	target string
}

// reconcileVPA applies VerticalPodAutoscalers when the CRD is installed. The
// VPAs run in recommendation-only mode; a missing CRD is not an error.
func (c *Controller) reconcileVPA(cluster *provv1.Cluster, roles Roles) error {
	desired := desiredVPA(cluster, roles)
	resource := c.dynamic.Resource(vpaGVR).Namespace(helmop.TargetNamespace)

	for _, target := range desired {
		obj := vpaObject(cluster, target)
		if _, err := apply.Ensure(c.ctx, resource, obj); err != nil {
			if apierrors.IsNotFound(err) {
				c.event(cluster, corev1.EventTypeWarning, "VPAUnavailable",
					"VerticalPodAutoscaler CRD is not installed; skipping VPA")
				return nil
			}
			return fmt.Errorf("apply VPA %s/%s: %w", helmop.TargetNamespace, target.name, err)
		}
	}

	keep := make(map[string]struct{}, len(desired))
	for _, target := range desired {
		keep[target.name] = struct{}{}
	}
	for _, target := range allVPA(cluster) {
		if _, ok := keep[target.name]; ok {
			continue
		}
		if _, err := apply.Delete(c.ctx, resource, target.name); err != nil {
			return fmt.Errorf("delete VPA %s/%s: %w", helmop.TargetNamespace, target.name, err)
		}
	}
	return nil
}

func (c *Controller) deleteAllVPA(cluster *provv1.Cluster) error {
	resource := c.dynamic.Resource(vpaGVR).Namespace(helmop.TargetNamespace)
	for _, target := range allVPA(cluster) {
		if _, err := apply.Delete(c.ctx, resource, target.name); err != nil {
			return fmt.Errorf("delete VPA %s/%s: %w", helmop.TargetNamespace, target.name, err)
		}
	}
	return nil
}

func desiredVPA(cluster *provv1.Cluster, roles Roles) []vpaTarget {
	targets := []vpaTarget{keydbVPA(cluster)}
	if roles.Manager {
		targets = append(targets, managerVPA(cluster))
	}
	if roles.Seed {
		targets = append(targets, controlVPA(cluster), dataVPA(cluster), nydusVPA(cluster))
	}
	return targets
}

func allVPA(cluster *provv1.Cluster) []vpaTarget {
	return []vpaTarget{
		keydbVPA(cluster),
		managerVPA(cluster),
		controlVPA(cluster),
		dataVPA(cluster),
		nydusVPA(cluster),
	}
}

func keydbVPA(cluster *provv1.Cluster) vpaTarget {
	target := helmop.KeyDBName(cluster.Name)
	return vpaTarget{name: target + "-vpa", kind: "Deployment", target: target}
}

func managerVPA(cluster *provv1.Cluster) vpaTarget {
	target := helmop.ManagerName(cluster.Name)
	return vpaTarget{name: target + "-vpa", kind: "Deployment", target: target}
}

func controlVPA(cluster *provv1.Cluster) vpaTarget {
	target := helmop.SchedulerServiceName(cluster.Name)
	return vpaTarget{name: helmop.ControlName(cluster.Name) + "-vpa", kind: "StatefulSet", target: target}
}

func dataVPA(cluster *provv1.Cluster) vpaTarget {
	target := helmop.DataName(cluster.Name) + "-seed-client"
	return vpaTarget{name: helmop.DataName(cluster.Name) + "-vpa", kind: "StatefulSet", target: target}
}

func nydusVPA(cluster *provv1.Cluster) vpaTarget {
	target := helmop.NydusName(cluster.Name)
	return vpaTarget{name: target + "-vpa", kind: "DaemonSet", target: target}
}

func vpaObject(cluster *provv1.Cluster, target vpaTarget) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "autoscaling.k8s.io/v1",
		"kind":       "VerticalPodAutoscaler",
		"metadata": map[string]any{
			"name":      target.name,
			"namespace": helmop.TargetNamespace,
			"labels":    apply.Labels(cluster.Namespace, cluster.Name),
		},
		"spec": map[string]any{
			"targetRef": map[string]any{
				"apiVersion": "apps/v1",
				"kind":       target.kind,
				"name":       target.target,
			},
			"updatePolicy": map[string]any{"updateMode": "Off"},
		},
	}}
}

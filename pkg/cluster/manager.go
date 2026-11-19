package cluster

import (
	"fmt"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
)

// ensureSharedManager reconciles the shared manager HelmOp. The manager
// credentials Secret is operator-global and replicated into the HelmOp
// namespace; the release itself fans out to manager-labeled Clusters. The
// per-cluster value inputs are Fleet templates, so no per-cluster manager
// HelmOp is created.
func (c *Controller) ensureSharedManager(cluster *provv1.Cluster, hc helmop.Cluster, roles Roles) error {
	if !roles.Manager {
		return nil
	}
	if err := c.ensureManagerCredentials(cluster); err != nil {
		c.event(cluster, corev1.EventTypeWarning, "ManagerCredentialsFailed", err.Error())
		return fmt.Errorf("ensure manager credentials: %w", err)
	}
	return c.applyHelmOp(cluster, helmop.SharedManager(hc))
}

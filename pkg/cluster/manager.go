package cluster

import (
	"fmt"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
)

// ensureManager reconciles the manager HelmOp. The manager Service stays
// cluster-internal; public endpoints are added by a follow-up MR.
func (c *Controller) ensureManager(cluster *provv1.Cluster, hc helmop.Cluster) error {
	if err := c.ensureManagerCredentials(cluster); err != nil {
		c.event(cluster, corev1.EventTypeWarning, "ManagerCredentialsFailed", err.Error())
		return fmt.Errorf("ensure manager credentials: %w", err)
	}
	return c.applyHelmOp(cluster, helmop.Manager(hc))
}

// deleteManager removes the manager release. KeyDB stays because it may still
// be used by the seed role.
func (c *Controller) deleteManager(cluster *provv1.Cluster) error {
	return c.deleteHelmOp(cluster, helmop.ManagerName(cluster.Name))
}

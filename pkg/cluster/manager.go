package cluster

import (
	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"

	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
)

// ensureManager reconciles the manager HelmOp. The manager Service stays
// cluster-internal; public endpoints are added by a follow-up MR.
func (c *Controller) ensureManager(cluster *provv1.Cluster, hc helmop.Cluster) error {
	return c.applyHelmOp(cluster, helmop.Manager(hc))
}

// deleteManager removes the manager release. KeyDB stays because it may still
// be used by the seed role.
func (c *Controller) deleteManager(cluster *provv1.Cluster) error {
	return c.deleteHelmOp(cluster, helmop.ManagerName(cluster.Name))
}

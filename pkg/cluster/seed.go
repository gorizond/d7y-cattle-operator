package cluster

import (
	"errors"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
)

// ensureSeed removes releases left by operator versions that installed the
// seed components per cluster. Control, data, and containerd-mirror are shared
// HelmOps now, so the seed role no longer creates copies here; the shared
// objects are reconciled once per pass from any enabled Cluster.
func (c *Controller) ensureSeed(cluster *provv1.Cluster) error {
	// Nydus is host-only. Delete the release left by older operator versions so
	// the in-cluster DaemonSet does not compete with the host unit.
	if err := c.deleteHelmOp(cluster, helmop.LegacyNydusName(cluster.Name)); err != nil {
		c.event(cluster, corev1.EventTypeWarning, "LegacyNydusCleanupFailed", err.Error())
	}
	return nil
}

// deleteSeed removes the legacy per-cluster seed releases. The shared
// control/data/mirror HelmOps are global: they stop targeting this Cluster when
// its d7y-operator-seed label is removed, and deleting them here would break
// every other seed Cluster. The scheduler-cluster-id annotation is owned by the
// Cluster and is never modified here.
func (c *Controller) deleteSeed(cluster *provv1.Cluster) error {
	var errs []error
	if err := c.deleteSchedulerPKI(cluster); err != nil {
		errs = append(errs, err)
	}
	for _, name := range []string{
		helmop.MirrorName(cluster.Name),
		helmop.DataName(cluster.Name),
		helmop.ControlName(cluster.Name),
		helmop.LegacyNydusName(cluster.Name),
	} {
		if err := c.deleteHelmOp(cluster, name); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

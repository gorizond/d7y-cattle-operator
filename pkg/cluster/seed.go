package cluster

import (
	"errors"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
)

// ensureSeed reconciles the control, data, and strict containerd-mirror
// releases. Replica counts stay Fleet templates and the scheduler cluster ID is
// read from the Cluster annotation; allocation is handled by MR-3. Without an
// ID the seed releases are not created, while manager and KeyDB reconciliation
// continues.
//
// Each error is returned separately so the controller can report a precise
// ControlReady/DataReady condition. The nydus snapshotter is installed on the
// node by p2p-install.sh and is not part of the seed releases.
func (c *Controller) ensureSeed(cluster *provv1.Cluster, hc helmop.Cluster) (controlErr, dataErr error) {
	if hc.SchedulerClusterID == 0 {
		c.event(cluster, corev1.EventTypeWarning, "SchedulerClusterIDMissing",
			"seed role is enabled but annotation "+AnnotationSchedulerClusterID+" is missing or not a positive integer")
		return nil, nil
	}

	controlErr = c.applyHelmOp(cluster, helmop.Control(hc))
	dataErr = errors.Join(
		c.applyHelmOp(cluster, helmop.Data(hc)),
		c.applyHelmOp(cluster, helmop.Mirror(hc)),
	)
	// Nydus is host-only now. Delete the release left by older operator
	// versions so the in-cluster DaemonSet does not compete with the host unit.
	if err := c.deleteHelmOp(cluster, helmop.LegacyNydusName(cluster.Name)); err != nil {
		c.event(cluster, corev1.EventTypeWarning, "LegacyNydusCleanupFailed", err.Error())
	}
	return controlErr, dataErr
}

// deleteSeed removes all seed releases. The scheduler-cluster-id annotation is
// owned by the Cluster and is never modified here.
func (c *Controller) deleteSeed(cluster *provv1.Cluster) error {
	var errs []error
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

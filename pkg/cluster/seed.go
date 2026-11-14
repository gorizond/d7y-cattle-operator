package cluster

import (
	"errors"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
)

// ensureSeed reconciles the control and data releases, plus nydus when
// NydusEnabled is true. Replica counts stay Fleet templates and the scheduler
// cluster ID is read from the Cluster annotation; allocation is handled by
// MR-3. Without an ID the seed releases are not created, while manager and
// KeyDB reconciliation continues.
//
// Each error is returned separately so the controller can report a precise
// ControlReady/DataReady/NydusReady condition. Nydus is deferred by default and
// any nydus error is intentionally not returned unless NydusEnabled is true.
func (c *Controller) ensureSeed(cluster *provv1.Cluster, hc helmop.Cluster) (controlErr, dataErr, nydusErr error) {
	if hc.SchedulerClusterID == 0 {
		c.event(cluster, corev1.EventTypeWarning, "SchedulerClusterIDMissing",
			"seed role is enabled but annotation "+AnnotationSchedulerClusterID+" is missing or not a positive integer")
		return nil, nil, nil
	}

	controlErr = c.applyHelmOp(cluster, helmop.Control(hc))
	dataErr = c.applyHelmOp(cluster, helmop.Data(hc))
	if c.opts.NydusEnabled {
		nydusErr = c.applyHelmOp(cluster, helmop.Nydus(hc))
		return controlErr, dataErr, nydusErr
	}
	if err := c.deleteHelmOp(cluster, helmop.NydusName(cluster.Name)); err != nil {
		c.event(cluster, corev1.EventTypeWarning, "NydusCleanupFailed", err.Error())
	}
	return controlErr, dataErr, nil
}

// deleteSeed removes all seed releases. The scheduler-cluster-id annotation is
// owned by the Cluster and is never modified here.
func (c *Controller) deleteSeed(cluster *provv1.Cluster) error {
	var errs []error
	for _, name := range []string{
		helmop.ControlName(cluster.Name),
		helmop.DataName(cluster.Name),
		helmop.NydusName(cluster.Name),
	} {
		if err := c.deleteHelmOp(cluster, name); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

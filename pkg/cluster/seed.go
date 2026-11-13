package cluster

import (
	"errors"

	fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"
	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
)

// ensureSeed reconciles the control, data, and nydus releases. Replica counts
// stay Fleet templates and the scheduler cluster ID is read from the Cluster
// annotation; allocation is handled by MR-3. Without an ID the seed releases
// are not created, while manager and KeyDB reconciliation continues.
func (c *Controller) ensureSeed(cluster *provv1.Cluster, hc helmop.Cluster) error {
	if hc.SchedulerClusterID == 0 {
		c.event(cluster, corev1.EventTypeWarning, "SchedulerClusterIDMissing",
			"seed role is enabled but annotation "+AnnotationSchedulerClusterID+" is missing or not a positive integer")
		return nil
	}

	var errs []error
	for _, op := range []*fleetv1alpha1.HelmOp{
		helmop.Control(hc),
		helmop.Data(hc),
		helmop.Nydus(hc),
	} {
		if err := c.applyHelmOp(cluster, op); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
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

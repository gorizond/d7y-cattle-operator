package cluster

import (
	"errors"
	"fmt"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	networkingv1 "k8s.io/api/networking/v1"

	"github.com/gorizond/d7y-cattle-operator/pkg/apply"
	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
)

// ensureManager reconciles the manager HelmOp and its two public Ingresses.
func (c *Controller) ensureManager(cluster *provv1.Cluster, hc helmop.Cluster) error {
	var errs []error
	if err := c.applyHelmOp(cluster, helmop.Manager(hc)); err != nil {
		errs = append(errs, err)
	}

	ingresses := []*networkingv1.Ingress{helmop.ManagerGRPCIngress(hc)}
	if c.opts.ManagerAPIHost != "" {
		ingresses = append(ingresses, helmop.ManagerRESTIngress(hc, c.opts.ManagerAPIHost))
	}
	for _, ingress := range ingresses {
		resource := c.dynamic.Resource(ingressGVR).Namespace(cluster.Namespace)
		if _, err := apply.Ensure(c.ctx, resource, ingress); err != nil {
			errs = append(errs, fmt.Errorf("apply Ingress %s/%s: %w", cluster.Namespace, ingress.Name, err))
		}
	}
	return errors.Join(errs...)
}

// deleteManager removes the manager release and its Ingresses. KeyDB stays
// because it may still be used by the seed role.
func (c *Controller) deleteManager(cluster *provv1.Cluster) error {
	var errs []error
	if err := c.deleteHelmOp(cluster, helmop.ManagerName(cluster.Name)); err != nil {
		errs = append(errs, err)
	}
	if err := c.deleteManagerIngresses(cluster); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

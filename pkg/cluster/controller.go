// Package cluster reconciles Rancher provisioning Clusters into Fleet HelmOps.
package cluster

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"
	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	provctl "github.com/rancher/rancher/pkg/generated/controllers/provisioning.cattle.io/v1"
	corectl "github.com/rancher/wrangler/v3/pkg/generated/controllers/core/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/gorizond/d7y-cattle-operator/pkg/apply"
	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
)

const controllerName = "d7y-cattle-operator"

// Options carries the operator configuration needed for reconciliation.
type Options struct {
	OperatorNamespace string
	MTLSSecretName    string
	DragonflyChart    helmop.Chart
}

// Controller reconciles provisioning.cattle.io/v1 Clusters.
type Controller struct {
	ctx     context.Context
	secrets corectl.SecretController
	events  corectl.EventController
	helmops apply.Store
	opts    Options
}

// Register wires the Cluster controller. Handlers must be registered before
// the factories are started.
func Register(ctx context.Context, clusters provctl.ClusterController, secrets corectl.SecretController, events corectl.EventController, helmops apply.Store, opts Options) {
	c := &Controller{
		ctx:     ctx,
		secrets: secrets,
		events:  events,
		helmops: helmops,
		opts:    opts,
	}
	clusters.OnChange(ctx, controllerName, c.reconcile)
}

func (c *Controller) reconcile(key string, cluster *provv1.Cluster) (*provv1.Cluster, error) {
	if cluster == nil {
		return nil, nil
	}

	roles := ResolveRoles(cluster)
	if !roles.Enabled {
		if err := c.cleanupAll(cluster); err != nil {
			c.event(cluster, corev1.EventTypeWarning, "CleanupFailed", err.Error())
			return cluster, err
		}
		return cluster, nil
	}

	hc := c.helmopCluster(cluster)
	var errs []error
	if err := c.applyHelmOp(cluster, helmop.KeyDB(hc)); err != nil {
		errs = append(errs, err)
	}

	if roles.Manager {
		if err := c.ensureManagerSecret(cluster); err != nil {
			errs = append(errs, err)
		} else if err := c.ensureManager(cluster, hc); err != nil {
			errs = append(errs, err)
		}
	} else if err := c.deleteManager(cluster); err != nil {
		errs = append(errs, err)
	}

	if roles.Seed {
		c.event(cluster, corev1.EventTypeNormal, "SeedRoleNotImplemented",
			fmt.Sprintf("annotation %s is set, but the seed role is not implemented in this release; ignoring it", AnnotationSeed))
	}

	if len(errs) > 0 {
		err := errors.Join(errs...)
		c.event(cluster, corev1.EventTypeWarning, "ReconcileFailed", err.Error())
		return cluster, err
	}
	return cluster, nil
}

// ensureManagerSecret replicates the operator mTLS Secret into the Cluster
// namespace so Fleet can deliver it downstream.
func (c *Controller) ensureManagerSecret(cluster *provv1.Cluster) error {
	mtls, err := c.secrets.Get(c.opts.OperatorNamespace, c.opts.MTLSSecretName, metav1.GetOptions{})
	if err != nil {
		message := fmt.Sprintf("Secret %s/%s: %v", c.opts.OperatorNamespace, c.opts.MTLSSecretName, err)
		c.event(cluster, corev1.EventTypeWarning, "MTLSSecretUnavailable", message)
		return fmt.Errorf("get mTLS secret: %w", err)
	}

	source := mtls.DeepCopy()
	if source.Labels == nil {
		source.Labels = map[string]string{}
	}
	for key, value := range apply.Labels(cluster.Namespace, cluster.Name) {
		source.Labels[key] = value
	}
	if _, err := apply.ReplicateSecret(source, cluster.Namespace, c.secrets); err != nil {
		c.event(cluster, corev1.EventTypeWarning, "SecretReplicationFailed", err.Error())
		return err
	}
	return nil
}

func (c *Controller) helmopCluster(cluster *provv1.Cluster) helmop.Cluster {
	return helmop.Cluster{
		Name:           cluster.Name,
		Namespace:      cluster.Namespace,
		MTLSSecretName: c.opts.MTLSSecretName,
		Chart:          c.opts.DragonflyChart,
		Labels:         apply.Labels(cluster.Namespace, cluster.Name),
	}
}

func (c *Controller) applyHelmOp(cluster *provv1.Cluster, op *fleetv1alpha1.HelmOp) error {
	if _, err := apply.Ensure(c.ctx, c.helmops, op); err != nil {
		return fmt.Errorf("apply HelmOp %s/%s: %w", cluster.Namespace, op.Name, err)
	}
	return nil
}

func (c *Controller) deleteHelmOp(cluster *provv1.Cluster, name string) error {
	if _, err := apply.Delete(c.ctx, c.helmops, cluster.Namespace, name); err != nil {
		return fmt.Errorf("delete HelmOp %s/%s: %w", cluster.Namespace, name, err)
	}
	return nil
}

func (c *Controller) cleanupAll(cluster *provv1.Cluster) error {
	var errs []error
	for _, name := range []string{
		helmop.KeyDBName(cluster.Name),
		helmop.ManagerName(cluster.Name),
	} {
		if err := c.deleteHelmOp(cluster, name); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (c *Controller) event(cluster *provv1.Cluster, eventType, reason, message string) {
	now := metav1.Now()
	event := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s.%x", cluster.Name, now.UnixNano()),
			Namespace: cluster.Namespace,
		},
		InvolvedObject: corev1.ObjectReference{
			APIVersion: "provisioning.cattle.io/v1",
			Kind:       "Cluster",
			Namespace:  cluster.Namespace,
			Name:       cluster.Name,
			UID:        cluster.UID,
		},
		Reason:         reason,
		Message:        message,
		Type:           eventType,
		Source:         corev1.EventSource{Component: controllerName},
		FirstTimestamp: now,
		LastTimestamp:  now,
		Count:          1,
	}
	if _, err := c.events.Create(event); err != nil {
		slog.Warn("failed to create Cluster event",
			"cluster", cluster.Namespace+"/"+cluster.Name,
			"reason", reason,
			"error", err,
		)
	}
}

package cluster

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/gorizond/d7y-cattle-operator/pkg/managerapi"
	"github.com/gorizond/d7y-cattle-operator/pkg/registry"
)

// managerRegistrar is the manager API surface used by reconciliation. The
// concrete implementation is *managerapi.Client.
type managerRegistrar interface {
	EnsureSchedulerCluster(ctx context.Context, req managerapi.SchedulerClusterRequest) (*managerapi.SchedulerCluster, error)
	EnsureSeedPeerCluster(ctx context.Context, req managerapi.SeedPeerClusterRequest) (*managerapi.SeedPeerCluster, error)
	LinkSeedPeerCluster(ctx context.Context, seedPeerID, schedulerID int64) error
}

// clusterStore is the subset of the Wrangler ClusterController used to persist
// the operator-written annotation and status conditions.
type clusterStore interface {
	Update(*provv1.Cluster) (*provv1.Cluster, error)
	UpdateStatus(*provv1.Cluster) (*provv1.Cluster, error)
}

func (c *Controller) managerClient(ctx context.Context) (managerRegistrar, *registry.Allocator, error) {
	c.managerMu.Lock()
	defer c.managerMu.Unlock()

	if c.registrar != nil {
		if c.allocator == nil {
			if lister, ok := c.registrar.(registry.SchedulerClusterLister); ok {
				c.allocator = registry.New(lister)
			}
		}
		if c.allocator != nil {
			return c.registrar, c.allocator, nil
		}
	}

	if c.opts.ManagerAPIURL == "" {
		return nil, nil, fmt.Errorf("%w: manager API URL is empty", managerapi.ErrInvalidBaseURL)
	}
	if c.opts.ManagerAPICredentialsSecret == "" {
		return nil, nil, fmt.Errorf("%w: manager API credentials Secret is empty", managerapi.ErrMissingCredentials)
	}
	if c.secrets == nil {
		return nil, nil, fmt.Errorf("manager API credentials store is not configured")
	}

	secret, err := c.secrets.Get(c.opts.OperatorNamespace, c.opts.ManagerAPICredentialsSecret, metav1.GetOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("read manager API credentials Secret %s/%s: %w", c.opts.OperatorNamespace, c.opts.ManagerAPICredentialsSecret, err)
	}
	client, err := managerapi.New(managerapi.Options{
		BaseURL:    c.opts.ManagerAPIURL,
		Username:   strings.TrimSpace(string(secret.Data["username"])),
		Password:   string(secret.Data["password"]),
		HTTPClient: c.managerHTTPClient,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("manager API client: %w", err)
	}

	c.registrar = client
	c.allocator = registry.New(client)
	return c.registrar, c.allocator, nil
}

// ensureSeedRegistration ensures both manager records and their link. It
// returns the ID to use in control/data values and whether the annotation and
// server ID disagree in user mode.
func (c *Controller) ensureSeedRegistration(ctx context.Context, cluster *provv1.Cluster) (int64, bool, *provv1.Cluster, error) {
	registrar, allocator, err := c.managerClient(ctx)
	if err != nil {
		return 0, false, nil, err
	}

	key := registry.ClusterKey(cluster.Namespace, cluster.Name)
	annotation := strings.TrimSpace(cluster.Annotations[AnnotationSchedulerClusterID])
	autoAnnotation := strings.TrimSpace(cluster.Annotations[AnnotationSchedulerClusterIDAuto])

	var annotationID int64
	if annotation != "" {
		annotationID, err = strconv.ParseInt(annotation, 10, 64)
		if err != nil || annotationID < 1 {
			return 0, false, nil, fmt.Errorf("%w: %q", registry.ErrInvalidAnnotation, annotation)
		}
	}
	var autoID int64
	if autoAnnotation != "" {
		autoID, _ = strconv.ParseInt(autoAnnotation, 10, 64)
	}
	userMode := annotation != "" && autoID != annotationID

	// The allocator candidate is only a cache reservation; the manager assigns
	// the ID. The candidate is never sent to create.
	if _, err := allocator.Allocate(ctx, key, annotation); err != nil {
		return 0, false, nil, fmt.Errorf("allocate scheduler cluster ID candidate: %w", err)
	}

	schedulerReq := managerapi.NewSchedulerClusterRequest(cluster.Namespace, cluster.Name)
	scheduler, err := registrar.EnsureSchedulerCluster(ctx, schedulerReq)
	if err != nil {
		return 0, false, nil, err
	}
	seedReq := managerapi.NewSeedPeerClusterRequest(cluster.Namespace, cluster.Name)
	seedPeer, err := registrar.EnsureSeedPeerCluster(ctx, seedReq)
	if err != nil {
		return 0, false, nil, err
	}
	if scheduler.ID < 1 || seedPeer.ID < 1 {
		return 0, false, nil, fmt.Errorf("manager returned invalid scheduler/seed IDs %d/%d", scheduler.ID, seedPeer.ID)
	}

	if err := registrar.LinkSeedPeerCluster(ctx, seedPeer.ID, scheduler.ID); err != nil {
		if !errors.Is(err, managerapi.ErrNotFound) {
			return 0, false, nil, err
		}
		// The records disappeared between ensure and link (DB loss). Recreate
		// once and link again; the link endpoint is idempotent.
		scheduler, err = registrar.EnsureSchedulerCluster(ctx, schedulerReq)
		if err != nil {
			return 0, false, nil, err
		}
		seedPeer, err = registrar.EnsureSeedPeerCluster(ctx, seedReq)
		if err != nil {
			return 0, false, nil, err
		}
		if scheduler.ID < 1 || seedPeer.ID < 1 {
			return 0, false, nil, fmt.Errorf("manager returned invalid scheduler/seed IDs %d/%d", scheduler.ID, seedPeer.ID)
		}
		if err := registrar.LinkSeedPeerCluster(ctx, seedPeer.ID, scheduler.ID); err != nil {
			return 0, false, nil, err
		}
	}

	if userMode {
		if annotationID != scheduler.ID {
			return annotationID, true, cluster, nil
		}
		return annotationID, false, cluster, nil
	}

	registrationCluster := cluster
	serverID := strconv.FormatInt(scheduler.ID, 10)
	if annotation != serverID || autoAnnotation != serverID {
		updated, err := c.writeSchedulerClusterID(cluster, scheduler.ID)
		if err != nil {
			return 0, false, nil, err
		}
		registrationCluster = updated
	}
	return scheduler.ID, false, registrationCluster, nil
}

func (c *Controller) writeSchedulerClusterID(cluster *provv1.Cluster, id int64) (*provv1.Cluster, error) {
	if c.clusters == nil {
		return nil, errors.New("cluster store is not configured")
	}
	updated := cluster.DeepCopy()
	if updated.Annotations == nil {
		updated.Annotations = map[string]string{}
	}
	updated.Annotations[AnnotationSchedulerClusterID] = strconv.FormatInt(id, 10)
	updated.Annotations[AnnotationSchedulerClusterIDAuto] = strconv.FormatInt(id, 10)
	result, err := c.clusters.Update(updated)
	if err != nil {
		return nil, fmt.Errorf("write scheduler cluster ID annotation: %w", err)
	}
	return result, nil
}

// registrationFailure classifies an error for status/backoff. Permanent errors
// set RegistrationFailed; transient manager/network errors set Retrying.
func registrationFailure(err error) bool {
	switch {
	case errors.Is(err, managerapi.ErrUnauthorized),
		errors.Is(err, managerapi.ErrUnprocessable),
		errors.Is(err, registry.ErrInvalidAnnotation),
		errors.Is(err, managerapi.ErrConflict),
		errors.Is(err, managerapi.ErrMissingCredentials),
		errors.Is(err, managerapi.ErrInvalidBaseURL):
		return false
	default:
		return true
	}
}

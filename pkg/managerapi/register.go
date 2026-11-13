package managerapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// LinkSeedPeerCluster links a seed peer cluster to a scheduler cluster using the
// explicit link endpoint. It returns nil on the documented 200 empty response.
// The endpoint clears all existing scheduler clusters of the target scheduler
// cluster and appends this seed peer cluster, so it is idempotent for the
// operator and re-applied on every reconcile/self-heal.
func (c *Client) LinkSeedPeerCluster(ctx context.Context, seedPeerID, schedulerID int64) error {
	if seedPeerID < 1 || schedulerID < 1 {
		return fmt.Errorf("manager api: link seed peer %d to scheduler %d: invalid ID", seedPeerID, schedulerID)
	}
	path := fmt.Sprintf("%s/%d/scheduler-clusters/%d", SeedPeerClustersPath, seedPeerID, schedulerID)
	return c.do(ctx, http.MethodPut, path, nil, nil, nil)
}

// EnsureSchedulerCluster returns the existing record by exact name or creates
// it. A 409 is treated as an existing record and resolved by re-reading.
func (c *Client) EnsureSchedulerCluster(ctx context.Context, req SchedulerClusterRequest) (*SchedulerCluster, error) {
	existing, err := c.findSchedulerCluster(ctx, req.Name)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	created, err := c.CreateSchedulerCluster(ctx, req)
	if err == nil {
		return created, nil
	}
	if !errors.Is(err, ErrConflict) {
		return nil, err
	}

	existing, err = c.findSchedulerCluster(ctx, req.Name)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, fmt.Errorf("manager api: scheduler cluster %q: %w", req.Name, ErrConflict)
	}
	return existing, nil
}

// EnsureSeedPeerCluster returns the existing record by exact name or creates
// it. A 409 is treated as an existing record and resolved by re-reading.
func (c *Client) EnsureSeedPeerCluster(ctx context.Context, req SeedPeerClusterRequest) (*SeedPeerCluster, error) {
	existing, err := c.findSeedPeerCluster(ctx, req.Name)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	created, err := c.CreateSeedPeerCluster(ctx, req)
	if err == nil {
		return created, nil
	}
	if !errors.Is(err, ErrConflict) {
		return nil, err
	}

	existing, err = c.findSeedPeerCluster(ctx, req.Name)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, fmt.Errorf("manager api: seed peer cluster %q: %w", req.Name, ErrConflict)
	}
	return existing, nil
}

func (c *Client) findSchedulerCluster(ctx context.Context, name string) (*SchedulerCluster, error) {
	clusters, err := c.ListSchedulerClustersByName(ctx, name)
	if err != nil {
		return nil, err
	}
	for i := range clusters {
		if clusters[i].Name == name {
			return &clusters[i], nil
		}
	}
	return nil, nil
}

func (c *Client) findSeedPeerCluster(ctx context.Context, name string) (*SeedPeerCluster, error) {
	clusters, err := c.ListSeedPeerClustersByName(ctx, name)
	if err != nil {
		return nil, err
	}
	for i := range clusters {
		if clusters[i].Name == name {
			return &clusters[i], nil
		}
	}
	return nil, nil
}

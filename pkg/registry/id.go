// Package registry allocates and caches scheduler cluster IDs. It contains no
// Kubernetes or controller wiring; callers provide the manager API lister and
// persist the annotation themselves.
package registry

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/gorizond/d7y-cattle-operator/pkg/managerapi"
)

var (
	ErrEmptyKey          = errors.New("registry: cluster key is empty")
	ErrInvalidAnnotation = errors.New("registry: scheduler cluster ID annotation is invalid")
	ErrNoLister          = errors.New("registry: scheduler cluster lister is nil")
)

// SchedulerClusterLister lists existing manager records so the allocator never
// reuses an ID that is already taken.
type SchedulerClusterLister interface {
	ListSchedulerClusters(ctx context.Context) ([]managerapi.SchedulerCluster, error)
}

// Allocator is a thread-safe scheduler cluster ID allocator. ID 1 is reserved
// and never allocated automatically; allocation starts at 2. Existing
// annotations are returned unchanged and cached so the allocator is set-once
// and idempotent.
type Allocator struct {
	mu       sync.Mutex
	lister   SchedulerClusterLister
	assigned map[string]int64
}

// New returns an allocator backed by lister. A nil lister is allowed only for
// callers that always pass an existing annotation.
func New(lister SchedulerClusterLister) *Allocator {
	return &Allocator{
		lister:   lister,
		assigned: map[string]int64{},
	}
}

// ClusterKey returns the stable cache key for a Cluster.
func ClusterKey(namespace, name string) string {
	return namespace + "/" + name
}

// Allocate returns the scheduler cluster ID for key.
//
// If annotation is non-empty it is parsed and returned unchanged (including a
// user-provided ID 1); it is never overwritten. Otherwise a cached assignment is
// returned, or the smallest free ID >= 2 is chosen using the manager list and
// the allocator cache. Concurrent calls are serialized so two keys never get the
// same ID.
func (a *Allocator) Allocate(ctx context.Context, key, annotation string) (int64, error) {
	if key == "" {
		return 0, ErrEmptyKey
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if raw := strings.TrimSpace(annotation); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id < 1 {
			return 0, fmt.Errorf("%w: %q", ErrInvalidAnnotation, annotation)
		}
		a.assigned[key] = id
		return id, nil
	}
	if id, ok := a.assigned[key]; ok {
		return id, nil
	}
	if a.lister == nil {
		return 0, ErrNoLister
	}

	clusters, err := a.lister.ListSchedulerClusters(ctx)
	if err != nil {
		return 0, fmt.Errorf("registry: list scheduler clusters: %w", err)
	}

	used := map[int64]struct{}{1: {}}
	for _, cluster := range clusters {
		if cluster.ID >= 1 {
			used[cluster.ID] = struct{}{}
		}
	}
	for _, id := range a.assigned {
		used[id] = struct{}{}
	}

	for id := int64(2); id > 0; id++ {
		if _, ok := used[id]; !ok {
			a.assigned[key] = id
			return id, nil
		}
	}
	return 0, fmt.Errorf("registry: no free scheduler cluster ID")
}

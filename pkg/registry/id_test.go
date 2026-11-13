package registry

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/gorizond/d7y-cattle-operator/pkg/managerapi"
)

func TestAllocateStartsAtTwo(t *testing.T) {
	allocator := New(newFakeLister())
	id, err := allocator.Allocate(context.Background(), ClusterKey("ns", "c1"), "")
	if err != nil {
		t.Fatalf("Allocate() error = %v", err)
	}
	if id != 2 {
		t.Errorf("id = %d, want 2", id)
	}
}

func TestAllocateSkipsReservedAndUsed(t *testing.T) {
	lister := newFakeLister(managerapi.SchedulerCluster{ID: 1}, managerapi.SchedulerCluster{ID: 2},
		managerapi.SchedulerCluster{ID: 3}, managerapi.SchedulerCluster{ID: 5})
	id, err := New(lister).Allocate(context.Background(), ClusterKey("ns", "c1"), "")
	if err != nil {
		t.Fatalf("Allocate() error = %v", err)
	}
	if id != 4 {
		t.Errorf("id = %d, want 4", id)
	}
}

func TestAllocateUserProvidedPreserved(t *testing.T) {
	lister := newFakeLister()
	allocator := New(lister)

	if id, err := allocator.Allocate(context.Background(), ClusterKey("ns", "c1"), "7"); err != nil || id != 7 {
		t.Fatalf("Allocate(7) = %d, %v", id, err)
	}
	if id, err := allocator.Allocate(context.Background(), ClusterKey("ns", "c2"), "1"); err != nil || id != 1 {
		t.Fatalf("Allocate(1) = %d, %v", id, err)
	}
	if lister.callCount() != 0 {
		t.Errorf("lister calls = %d, want 0 for user-provided IDs", lister.callCount())
	}
	if id, err := allocator.Allocate(context.Background(), ClusterKey("ns", "c3"), ""); err != nil || id != 2 {
		t.Fatalf("Allocate(auto) = %d, %v; want 2", id, err)
	}
}

func TestAllocateSetOnce(t *testing.T) {
	lister := newFakeLister()
	allocator := New(lister)
	key := ClusterKey("ns", "c1")

	first, err := allocator.Allocate(context.Background(), key, "")
	if err != nil {
		t.Fatalf("Allocate() error = %v", err)
	}
	lister.set(managerapi.SchedulerCluster{ID: first, Name: "ns-c1"})

	second, err := allocator.Allocate(context.Background(), key, "")
	if err != nil {
		t.Fatalf("Allocate(second) error = %v", err)
	}
	if second != first {
		t.Errorf("second id = %d, want cached %d", second, first)
	}
	if lister.callCount() != 1 {
		t.Errorf("lister calls = %d, want 1 (cache hit)", lister.callCount())
	}

	other, err := allocator.Allocate(context.Background(), ClusterKey("ns", "c2"), "")
	if err != nil {
		t.Fatalf("Allocate(other) error = %v", err)
	}
	if other != first+1 {
		t.Errorf("other id = %d, want %d", other, first+1)
	}
}

func TestAllocateConcurrentDistinctKeys(t *testing.T) {
	allocator := New(newFakeLister())
	const workers = 50
	ids := make(chan int64, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id, err := allocator.Allocate(context.Background(), ClusterKey("ns", "c"+strconv.Itoa(i)), "")
			if err != nil {
				t.Errorf("Allocate() error = %v", err)
				return
			}
			ids <- id
		}(i)
	}
	wg.Wait()
	close(ids)

	seen := map[int64]bool{}
	for id := range ids {
		if id < 2 {
			t.Errorf("id = %d, want >= 2", id)
		}
		if seen[id] {
			t.Errorf("duplicate id %d", id)
		}
		seen[id] = true
	}
	if len(seen) != workers {
		t.Errorf("unique ids = %d, want %d", len(seen), workers)
	}
}

func TestAllocateConcurrentSameKey(t *testing.T) {
	lister := newFakeLister()
	allocator := New(lister)
	key := ClusterKey("ns", "c1")
	const workers = 20
	ids := make(chan int64, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := allocator.Allocate(context.Background(), key, "")
			if err != nil {
				t.Errorf("Allocate() error = %v", err)
				return
			}
			ids <- id
		}()
	}
	wg.Wait()
	close(ids)

	var want int64
	for id := range ids {
		if want == 0 {
			want = id
		}
		if id != want {
			t.Errorf("id = %d, want %d for the same key", id, want)
		}
	}
	if lister.callCount() != 1 {
		t.Errorf("lister calls = %d, want 1", lister.callCount())
	}
}

func TestAllocateDuplicateManagerIDs(t *testing.T) {
	lister := newFakeLister(managerapi.SchedulerCluster{ID: 2}, managerapi.SchedulerCluster{ID: 2},
		managerapi.SchedulerCluster{ID: 2}, managerapi.SchedulerCluster{ID: 3})
	id, err := New(lister).Allocate(context.Background(), ClusterKey("ns", "c1"), "")
	if err != nil {
		t.Fatalf("Allocate() error = %v", err)
	}
	if id != 4 {
		t.Errorf("id = %d, want 4", id)
	}
}

func TestAllocateInvalidAnnotation(t *testing.T) {
	for _, annotation := range []string{"abc", "0", "-3", "1.5"} {
		t.Run(annotation, func(t *testing.T) {
			allocator := New(newFakeLister())
			if _, err := allocator.Allocate(context.Background(), ClusterKey("ns", "c1"), annotation); !errors.Is(err, ErrInvalidAnnotation) {
				t.Fatalf("Allocate(%q) error = %v, want ErrInvalidAnnotation", annotation, err)
			}
		})
	}
}

func TestAllocateListerError(t *testing.T) {
	lister := newFakeLister()
	lister.err = errors.New("manager down")
	allocator := New(lister)

	if _, err := allocator.Allocate(context.Background(), ClusterKey("ns", "c1"), ""); err == nil {
		t.Fatal("Allocate() error = nil, want lister error")
	}
	lister.err = nil
	id, err := allocator.Allocate(context.Background(), ClusterKey("ns", "c1"), "")
	if err != nil {
		t.Fatalf("Allocate(retry) error = %v", err)
	}
	if id != 2 {
		t.Errorf("id = %d, want 2", id)
	}
}

func TestAllocateNilLister(t *testing.T) {
	allocator := New(nil)
	if id, err := allocator.Allocate(context.Background(), ClusterKey("ns", "c1"), "5"); err != nil || id != 5 {
		t.Fatalf("Allocate(annotation) = %d, %v", id, err)
	}
	if _, err := allocator.Allocate(context.Background(), ClusterKey("ns", "c2"), ""); !errors.Is(err, ErrNoLister) {
		t.Fatalf("Allocate(auto) error = %v, want ErrNoLister", err)
	}
}

func TestAllocateEmptyKey(t *testing.T) {
	if _, err := New(newFakeLister()).Allocate(context.Background(), "", ""); !errors.Is(err, ErrEmptyKey) {
		t.Fatalf("Allocate() error = %v, want ErrEmptyKey", err)
	}
}

func TestAllocateCacheReservesIDs(t *testing.T) {
	lister := newFakeLister()
	allocator := New(lister)
	if id, err := allocator.Allocate(context.Background(), ClusterKey("ns", "c1"), ""); err != nil || id != 2 {
		t.Fatalf("Allocate(c1) = %d, %v", id, err)
	}
	lister.set(managerapi.SchedulerCluster{ID: 1}, managerapi.SchedulerCluster{ID: 3})
	id, err := allocator.Allocate(context.Background(), ClusterKey("ns", "c2"), "")
	if err != nil {
		t.Fatalf("Allocate(c2) error = %v", err)
	}
	if id != 4 {
		t.Errorf("id = %d, want 4", id)
	}
}

func TestAllocateDuplicateUserIDsAllowed(t *testing.T) {
	allocator := New(newFakeLister())
	if id, err := allocator.Allocate(context.Background(), ClusterKey("ns", "c1"), "5"); err != nil || id != 5 {
		t.Fatalf("Allocate(c1) = %d, %v", id, err)
	}
	if id, err := allocator.Allocate(context.Background(), ClusterKey("ns", "c2"), "5"); err != nil || id != 5 {
		t.Fatalf("Allocate(c2) = %d, %v", id, err)
	}
	if id, err := allocator.Allocate(context.Background(), ClusterKey("ns", "c3"), ""); err != nil || id != 2 {
		t.Fatalf("Allocate(c3) = %d, %v", id, err)
	}
}

func TestClusterKey(t *testing.T) {
	if got := ClusterKey("ns", "c1"); got != "ns/c1" {
		t.Errorf("ClusterKey = %q", got)
	}
}

type fakeLister struct {
	mu       sync.Mutex
	clusters []managerapi.SchedulerCluster
	err      error
	calls    int
}

func newFakeLister(clusters ...managerapi.SchedulerCluster) *fakeLister {
	return &fakeLister{clusters: clusters}
}

func (f *fakeLister) ListSchedulerClusters(_ context.Context) ([]managerapi.SchedulerCluster, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	out := make([]managerapi.SchedulerCluster, len(f.clusters))
	copy(out, f.clusters)
	return out, nil
}

func (f *fakeLister) set(clusters ...managerapi.SchedulerCluster) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clusters = clusters
}

func (f *fakeLister) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

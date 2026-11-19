package registryproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagerDeduplicatesAndCompletes(t *testing.T) {
	var calls atomic.Int32
	manager := NewManager(func(context.Context, Job) error {
		calls.Add(1)
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.Start(ctx)
	job := Job{Registry: "docker.io", Repository: "library/nginx", Reference: "1.27", Digest: "sha256:abc", Trigger: "pull"}
	if !manager.Enqueue(job) {
		t.Fatal("first Enqueue() = false")
	}
	if manager.Enqueue(job) {
		t.Fatal("duplicate Enqueue() = true")
	}
	waitState(t, manager, job.Key(), "ready")
	if calls.Load() != 1 {
		t.Errorf("work calls = %d, want 1", calls.Load())
	}
}

func TestManagerRetriesFailedJob(t *testing.T) {
	var calls atomic.Int32
	manager := NewManager(func(context.Context, Job) error {
		if calls.Add(1) <= 3 {
			return errors.New("transient")
		}
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.Start(ctx)
	job := Job{Registry: "docker.io", Repository: "library/nginx", Reference: "1.27", Digest: "sha256:abc"}
	if !manager.Enqueue(job) {
		t.Fatal("Enqueue() = false")
	}
	waitState(t, manager, job.Key(), "failed")
	if !manager.Enqueue(job) {
		t.Fatal("failed job was not accepted for retry")
	}
	waitState(t, manager, job.Key(), "ready")
}

func TestManagerRestartDoesNotRestoreInFlightJob(t *testing.T) {
	job := Job{Registry: "docker.io", Repository: "library/nginx", Reference: "1.27", Digest: "sha256:abc", Trigger: "pull"}

	before := NewManager(nil)
	if !before.Enqueue(job) {
		t.Fatal("initial Enqueue() = false")
	}
	if got := before.Pending(); got != 1 {
		t.Fatalf("before restart pending = %d, want 1", got)
	}

	// A restarted proxy has a new in-memory Manager. In-flight jobs are not
	// persisted; only the next webhook/pull may enqueue the job again.
	after := NewManager(nil)
	if got := after.Pending(); got != 0 {
		t.Fatalf("after restart pending = %d, want 0", got)
	}
	if _, ok := after.State(job.Key()); ok {
		t.Fatal("after restart State() found in-flight job")
	}
	if !after.Enqueue(job) {
		t.Fatal("next webhook/pull Enqueue() = false, want true")
	}
	if got := after.Pending(); got != 1 {
		t.Fatalf("after next trigger pending = %d, want 1", got)
	}
}

func TestManagerLogsJobFailure(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)

	manager := NewManager(func(context.Context, Job) error {
		return errors.New("copy exploded")
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.Start(ctx)
	job := Job{Registry: "docker.io", Repository: "library/nginx", Reference: "1.27", Digest: "sha256:abc"}
	if !manager.Enqueue(job) {
		t.Fatal("Enqueue() = false")
	}
	waitState(t, manager, job.Key(), "failed")
	if state, _ := manager.State(job.Key()); state.LastError != "copy exploded" {
		t.Errorf("LastError = %q", state.LastError)
	}
	logged := logs.String()
	for _, want := range []string{
		"registry proxy job failed",
		"key=" + job.Key(),
		"registry=docker.io",
		"repository=library/nginx",
		"attempt=3",
		"copy exploded",
	} {
		if !strings.Contains(logged, want) {
			t.Errorf("log does not contain %q: %s", want, logged)
		}
	}
	if strings.Contains(strings.ToLower(logged), "secret") {
		t.Errorf("log contains secret material: %s", logged)
	}
}

func waitState(t *testing.T, manager *Manager, key, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if state, ok := manager.State(key); ok && state.State == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	state, _ := manager.State(key)
	t.Fatalf("state for %s = %+v, want %s", key, state, want)
}

func TestJobKeyUsesTagNotDigest(t *testing.T) {
	tagged := Job{Registry: "docker.io", Repository: "library/nginx", Reference: "1.27", Tag: "1.27", Digest: "sha256:abc"}
	if got, want := tagged.Key(), "docker.io/library/nginx/1.27"; got != want {
		t.Errorf("tagged Key() = %q, want %q", got, want)
	}
	digestOnly := Job{Registry: "docker.io", Repository: "library/nginx", Reference: "sha256:abc", Digest: "sha256:abc"}
	if got, want := digestOnly.Key(), "docker.io/library/nginx/sha256:abc"; got != want {
		t.Errorf("digest Key() = %q, want %q", got, want)
	}
}

func TestManagerReconvertsMovedTag(t *testing.T) {
	var calls atomic.Int32
	manager := NewManager(func(context.Context, Job) error {
		calls.Add(1)
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.Start(ctx)

	first := Job{Registry: "docker.io", Repository: "library/nginx", Reference: "1.27", Tag: "1.27", Digest: "sha256:first"}
	if !manager.Enqueue(first) {
		t.Fatal("first Enqueue() = false")
	}
	waitState(t, manager, first.Key(), "ready")
	if manager.Enqueue(first) {
		t.Fatal("unchanged tag+digest Enqueue() = true")
	}
	moved := first
	moved.Digest = "sha256:second"
	if !manager.Enqueue(moved) {
		t.Fatal("moved tag Enqueue() = false, want a fresh conversion")
	}
	waitState(t, manager, moved.Key(), "ready")
	if calls.Load() != 2 {
		t.Errorf("work calls = %d, want 2", calls.Load())
	}
}

func TestManagerAttemptTimeoutDoesNotStarveWorker(t *testing.T) {
	manager := NewManager(func(ctx context.Context, job Job) error {
		if job.Tag == "stuck" {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	})
	manager.AttemptTimeout = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.Start(ctx)

	stuck := Job{Registry: "docker.io", Repository: "library/stuck", Reference: "stuck", Tag: "stuck", Digest: "sha256:stuck"}
	next := Job{Registry: "docker.io", Repository: "library/next", Reference: "next", Tag: "next", Digest: "sha256:next"}
	if !manager.Enqueue(stuck) || !manager.Enqueue(next) {
		t.Fatal("Enqueue() = false")
	}
	waitState(t, manager, stuck.Key(), "failed")
	waitState(t, manager, next.Key(), "ready")
}

func TestManagerRequeueRefreshesCachedJob(t *testing.T) {
	var calls atomic.Int32
	manager := NewManager(func(context.Context, Job) error {
		calls.Add(1)
		return nil
	})
	job := Job{Registry: "docker.io", Repository: "library/nginx", Reference: "1.27", Tag: "1.27", Digest: "sha256:abc"}
	manager.setState(job.Key(), "ready", 1, "")
	if !manager.Requeue(job) {
		t.Fatal("Requeue(ready) = false")
	}
	if got := manager.Pending(); got != 1 {
		t.Errorf("pending after requeue = %d, want 1", got)
	}
	manager.setState(job.Key(), "converting", 1, "")
	if manager.Requeue(job) {
		t.Fatal("Requeue(converting) = true, want false")
	}
}

func TestManagerDoesNotRetryNonConvertible(t *testing.T) {
	var calls atomic.Int32
	manager := NewManager(func(context.Context, Job) error {
		calls.Add(1)
		return fmt.Errorf("%w: source is linux/arm64, conversion platform is linux/amd64", ErrNonConvertible)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.Start(ctx)
	job := Job{Registry: "registry.access.redhat.com", Repository: "ubi9/ubi-micro", Reference: "sha256:4f69", Digest: "sha256:4f69"}
	if !manager.Enqueue(job) {
		t.Fatal("Enqueue() = false")
	}
	waitState(t, manager, job.Key(), "failed")
	if calls.Load() != 1 {
		t.Errorf("work calls = %d, want 1 (no retry for a permanent platform mismatch)", calls.Load())
	}
	state, _ := manager.State(job.Key())
	if state.Attempts != 1 || !strings.Contains(state.LastError, "not convertible") {
		t.Errorf("state = %+v", state)
	}
}

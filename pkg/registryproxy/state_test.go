package registryproxy

import (
	"bytes"
	"context"
	"errors"
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

package registryproxy

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Job is one requested image copy/conversion. Digest is preferred for
// deduplication; Reference is the tag or digest used for the Harbor copy.
type Job struct {
	Registry   string
	Repository string
	Reference  string
	Tag        string
	Digest     string
	Trigger    string
}

// TagOrReference returns the user-visible tag when known, otherwise the
// reference carried by the webhook (often a digest).
func (j Job) TagOrReference() string {
	if j.Tag != "" && !strings.HasPrefix(j.Tag, "sha256:") {
		return j.Tag
	}
	return j.Reference
}

// JobState is the in-memory state of a queued job.
type JobState struct {
	State     string
	Attempts  int
	LastError string
	UpdatedAt time.Time
}

// Manager deduplicates jobs by registry/repo/digest and runs one worker. It is
// intentionally in-memory: Harbor is the source of truth after restart.
type Manager struct {
	mu      sync.Mutex
	jobs    map[string]*JobState
	queue   chan Job
	work    func(context.Context, Job) error
	started bool

	enqueued  atomic.Uint64
	succeeded atomic.Uint64
	failed    atomic.Uint64
}

// NewManager creates an in-memory job manager. work is called by Start.
func NewManager(work func(context.Context, Job) error) *Manager {
	return &Manager{
		jobs:  map[string]*JobState{},
		queue: make(chan Job, 128),
		work:  work,
	}
}

// Key returns the deduplication key for a job.
func (j Job) Key() string {
	ref := j.Digest
	if ref == "" {
		ref = j.Reference
	}
	return j.Registry + "/" + j.Repository + "/" + ref
}

// Enqueue accepts a job once. Duplicate pending/converting/ready jobs are
// ignored; failed jobs may be retried by a later webhook/pull.
func (m *Manager) Enqueue(job Job) bool {
	key := job.Key()
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.jobs[key]; ok {
		if existing.State != "failed" {
			return false
		}
		existing.State = "pending"
		existing.LastError = ""
		existing.UpdatedAt = time.Now().UTC()
		select {
		case m.queue <- job:
			m.enqueued.Add(1)
			return true
		default:
			return false
		}
	}
	m.jobs[key] = &JobState{State: "pending", UpdatedAt: time.Now().UTC()}
	select {
	case m.queue <- job:
		m.enqueued.Add(1)
		return true
	default:
		delete(m.jobs, key)
		return false
	}
}

// Start runs the single worker until ctx is cancelled.
func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.mu.Unlock()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case job := <-m.queue:
				m.run(ctx, job)
			}
		}
	}()
}

func (m *Manager) run(ctx context.Context, job Job) {
	key := job.Key()
	m.setState(key, "converting", 0, "")
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		if m.work != nil {
			err = m.work(ctx, job)
		}
		if err == nil {
			m.setState(key, "ready", attempt, "")
			m.succeeded.Add(1)
			return
		}
		slog.Error("registry proxy job failed",
			"key", key,
			"registry", job.Registry,
			"repository", job.Repository,
			"reference", job.TagOrReference(),
			"attempt", attempt,
			"error", err,
		)
		m.setState(key, "converting", attempt, err.Error())
	}
	m.setState(key, "failed", 3, err.Error())
	m.failed.Add(1)
}

// Metrics returns the accepted, succeeded, and failed job counters.
func (m *Manager) Metrics() (enqueued, succeeded, failed uint64) {
	return m.enqueued.Load(), m.succeeded.Load(), m.failed.Load()
}

func (m *Manager) setState(key, state string, attempts int, lastError string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[key]
	if !ok {
		job = &JobState{}
		m.jobs[key] = job
	}
	job.State = state
	job.Attempts = attempts
	job.LastError = lastError
	job.UpdatedAt = time.Now().UTC()
}

// State returns a copy of the current job state.
func (m *Manager) State(key string) (JobState, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.jobs[key]
	if !ok {
		return JobState{}, false
	}
	return *state, true
}

// Pending returns the number of queued or converting jobs.
func (m *Manager) Pending() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, state := range m.jobs {
		if state.State == "pending" || state.State == "converting" {
			count++
		}
	}
	return count
}

func (m *Manager) String() string {
	return fmt.Sprintf("registryproxy.Manager{queue=%d pending=%d}", len(m.queue), m.Pending())
}

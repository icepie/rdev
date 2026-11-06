package server

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	agentMaxRunningJobs  = 8
	agentMaxRetainedJobs = 64
	agentJobOutputLimit  = 32 * 1024
)

type agentJobSnapshot struct {
	ID       string     `json:"id"`
	Kind     string     `json:"kind"`
	Label    string     `json:"label"`
	State    string     `json:"state"`
	Output   string     `json:"output,omitempty"`
	Result   string     `json:"result,omitempty"`
	Error    string     `json:"error,omitempty"`
	Started  time.Time  `json:"started"`
	Finished *time.Time `json:"finished,omitempty"`
}

type agentJob struct {
	snapshot agentJobSnapshot
	cancel   context.CancelFunc
	done     chan struct{}
}

// agentJobs owns thread-local work independently of a foreground turn. A
// browser disconnect or turn completion does not cancel jobs; Close does.
// Callbacks run without the jobs mutex, so they may enter server state locks.
type agentJobs struct {
	mu         sync.Mutex
	jobs       map[string]*agentJob
	closed     bool
	onComplete func(agentJobSnapshot)
}

func newAgentJobs(onComplete func(agentJobSnapshot)) *agentJobs {
	return &agentJobs{jobs: make(map[string]*agentJob), onComplete: onComplete}
}

func (m *agentJobs) Start(kind, label string, run func(context.Context, func(string)) (string, error)) (agentJobSnapshot, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return agentJobSnapshot{}, fmt.Errorf("thread jobs are closed")
	}
	running := 0
	var oldest *agentJob
	for _, job := range m.jobs {
		if job.snapshot.State == "running" {
			running++
		} else if oldest == nil || job.snapshot.Started.Before(oldest.snapshot.Started) {
			oldest = job
		}
	}
	if running >= agentMaxRunningJobs {
		m.mu.Unlock()
		return agentJobSnapshot{}, fmt.Errorf("at most %d background jobs may run per thread", agentMaxRunningJobs)
	}
	if len(m.jobs) >= agentMaxRetainedJobs && oldest != nil {
		delete(m.jobs, oldest.snapshot.ID)
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &agentJob{snapshot: agentJobSnapshot{ID: generateID(), Kind: kind, Label: label, State: "running", Started: time.Now()}, cancel: cancel, done: make(chan struct{})}
	m.jobs[job.snapshot.ID] = job
	initial := job.snapshot
	m.mu.Unlock()
	go m.run(ctx, job, run)
	return initial, nil
}

func (m *agentJobs) run(ctx context.Context, job *agentJob, run func(context.Context, func(string)) (string, error)) {
	var result string
	var err error
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("background task panicked: %v", p)
		}
		m.mu.Lock()
		finished := time.Now()
		job.snapshot.Finished = &finished
		job.snapshot.Result = agentJobTail(result)
		switch {
		case ctx.Err() != nil:
			job.snapshot.State = "canceled"
			job.snapshot.Error = ctx.Err().Error()
		case err != nil:
			job.snapshot.State = "failed"
			job.snapshot.Error = agentJobTail(err.Error())
		default:
			job.snapshot.State = "completed"
		}
		snapshot := job.snapshot
		notify := !m.closed
		close(job.done)
		m.mu.Unlock()
		job.cancel()
		if notify && m.onComplete != nil {
			m.onComplete(snapshot)
		}
	}()
	result, err = run(ctx, func(output string) {
		m.mu.Lock()
		job.snapshot.Output = agentJobTail(job.snapshot.Output + output)
		m.mu.Unlock()
	})
}

func agentJobTail(output string) string {
	if len(output) <= agentJobOutputLimit {
		return output
	}
	start := len(output) - agentJobOutputLimit
	for start < len(output) && !utf8.RuneStart(output[start]) {
		start++
	}
	return output[start:]
}

func (m *agentJobs) List() []agentJobSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]agentJobSnapshot, 0, len(m.jobs))
	for _, job := range m.jobs {
		snapshot := job.snapshot
		snapshot.Output = ""
		snapshot.Result = ""
		result = append(result, snapshot)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Started.Before(result[j].Started) })
	return result
}

func (m *agentJobs) Read(id string) (agentJobSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if job := m.jobs[id]; job != nil {
		return job.snapshot, nil
	}
	return agentJobSnapshot{}, fmt.Errorf("unknown job %q in this thread", id)
}

func (m *agentJobs) Cancel(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[id]
	if job == nil {
		return fmt.Errorf("unknown job %q in this thread", id)
	}
	job.cancel()
	return nil
}

// CancelAll affects only this thread's jobs, atomically with job admission.
func (m *agentJobs) CancelAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, job := range m.jobs {
		if job.snapshot.State == "running" {
			job.cancel()
		}
	}
}

func (m *agentJobs) Wait(ctx context.Context, id string) (agentJobSnapshot, error) {
	m.mu.Lock()
	job := m.jobs[id]
	m.mu.Unlock()
	if job == nil {
		return agentJobSnapshot{}, fmt.Errorf("unknown job %q in this thread", id)
	}
	select {
	case <-job.done:
		// Keep the captured job: retention may evict it while this waiter wakes.
		m.mu.Lock()
		defer m.mu.Unlock()
		return job.snapshot, nil
	case <-ctx.Done():
		return agentJobSnapshot{}, ctx.Err()
	}
}

func (m *agentJobs) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	for _, job := range m.jobs {
		job.cancel()
	}
}

func (m *agentJobs) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, job := range m.jobs {
		if job.snapshot.State == "running" {
			return true
		}
	}
	return false
}

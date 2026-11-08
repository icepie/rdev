package server

import (
	"context"
	"fmt"
	"sort"
	"strings"
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
	ID              string     `json:"id"`
	Kind            string     `json:"kind"`
	Label           string     `json:"label"`
	State           string     `json:"state"`
	Output          string     `json:"output,omitempty"`
	Result          string     `json:"result,omitempty"`
	Error           string     `json:"error,omitempty"`
	Started         time.Time  `json:"started"`
	Finished        *time.Time `json:"finished,omitempty"`
	PendingGuidance []string   `json:"pendingGuidance,omitempty"`
	CancelRequested bool       `json:"cancelRequested,omitempty"`
}

type agentJob struct {
	guidance *agentJobGuidance
	output   agentJobOutput
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
	onChange   func()
	onComplete func(agentJobSnapshot)
}

func newAgentJobs(onComplete func(agentJobSnapshot)) *agentJobs {
	return &agentJobs{jobs: make(map[string]*agentJob), onComplete: onComplete}
}

func (m *agentJobs) Start(kind, label string, run func(context.Context, func(string)) (string, error)) (agentJobSnapshot, error) {
	return m.start(kind, label, run, nil)
}

func (m *agentJobs) start(kind, label string, run func(context.Context, func(string)) (string, error), guidance *agentJobGuidance) (agentJobSnapshot, error) {
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
	job := &agentJob{guidance: guidance, snapshot: agentJobSnapshot{ID: generateID(), Kind: kind, Label: label, State: "running", Started: time.Now()}, cancel: cancel, done: make(chan struct{})}
	m.jobs[job.snapshot.ID] = job
	initial := job.snapshot
	m.mu.Unlock()
	if m.onChange != nil {
		m.onChange()
	}
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
		job.snapshot.Output = job.output.String()
		job.output = agentJobOutput{}
		job.snapshot.Finished = &finished
		job.snapshot.CancelRequested = false
		job.snapshot.Result = agentJobTail(result)
		if job.guidance != nil {
			job.guidance.close()
			job.snapshot.PendingGuidance = job.guidance.snapshot()
		}
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
		snapshot.PendingGuidance = append([]string(nil), snapshot.PendingGuidance...)
		notify := !m.closed
		close(job.done)
		m.mu.Unlock()
		job.cancel()
		if notify && m.onChange != nil {
			m.onChange()
		}
		if notify && m.onComplete != nil {
			m.onComplete(snapshot)
		}
	}()
	result, err = run(ctx, func(output string) {
		m.mu.Lock()
		if job.snapshot.Finished == nil {
			job.output.append(output)
		}
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
	// Do not retain a potentially huge result/error backing string for a
	// bounded finished-job snapshot. This copy happens only when truncated.
	return strings.Clone(output[start:])
}

func (m *agentJobs) List() []agentJobSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]agentJobSnapshot, 0, len(m.jobs))
	for _, job := range m.jobs {
		snapshot := job.snapshot
		snapshot.Output = ""
		snapshot.Result = ""
		snapshot.PendingGuidance = nil
		result = append(result, snapshot)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Started.Before(result[j].Started) })
	return result
}

func (m *agentJobs) Read(id string) (agentJobSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if job := m.jobs[id]; job != nil {
		snapshot := job.snapshot
		snapshot.PendingGuidance = append([]string(nil), snapshot.PendingGuidance...)
		if snapshot.Finished == nil {
			snapshot.Output = job.output.String()
			if job.guidance != nil {
				snapshot.PendingGuidance = job.guidance.snapshot()
			}
		}
		return snapshot, nil
	}
	return agentJobSnapshot{}, fmt.Errorf("unknown job %q in this thread", id)
}

func (m *agentJobs) Cancel(id string) error {
	m.mu.Lock()
	job := m.jobs[id]
	if job == nil {
		m.mu.Unlock()
		return fmt.Errorf("unknown job %q in this thread", id)
	}
	changed := job.requestCancel() && !m.closed
	m.mu.Unlock()
	if changed && m.onChange != nil {
		m.onChange()
	}
	return nil
}

// Called with the manager lock held. Requesting cancellation does not free a
// running slot: the worker must actually exit before completion is published.
func (job *agentJob) requestCancel() bool {
	if job.snapshot.State != "running" || job.snapshot.CancelRequested {
		return false
	}
	job.snapshot.CancelRequested = true
	if job.guidance != nil {
		job.guidance.close()
	}
	job.cancel()
	return true
}

// CancelAll affects only this thread's jobs, atomically with job admission.
func (m *agentJobs) CancelAll() {
	m.mu.Lock()
	changed := false
	for _, job := range m.jobs {
		changed = job.requestCancel() || changed
	}
	changed = changed && !m.closed
	m.mu.Unlock()
	if changed && m.onChange != nil {
		m.onChange()
	}
}

func (m *agentJobs) Wait(ctx context.Context, id string) (agentJobSnapshot, error) {
	job, _, err := m.wait(ctx, id, nil)
	return job, err
}

// wait can release a passive waiter without canceling the underlying job.
// Keep the captured job: retention can evict it while the waiter wakes.
func (m *agentJobs) wait(ctx context.Context, id string, interrupt <-chan struct{}) (agentJobSnapshot, bool, error) {
	m.mu.Lock()
	job := m.jobs[id]
	m.mu.Unlock()
	if job == nil {
		return agentJobSnapshot{}, false, fmt.Errorf("unknown job %q in this thread", id)
	}
	select {
	case <-job.done:
	case <-interrupt:
	case <-ctx.Done():
		return agentJobSnapshot{}, false, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return agentJobSnapshot{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	snapshot := job.snapshot
	snapshot.PendingGuidance = append([]string(nil), snapshot.PendingGuidance...)
	interrupted := snapshot.Finished == nil
	if interrupted {
		snapshot.Output = job.output.String()
		if job.guidance != nil {
			snapshot.PendingGuidance = job.guidance.snapshot()
		}
	}
	return snapshot, interrupted, nil
}

func (m *agentJobs) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	for _, job := range m.jobs {
		job.requestCancel()
	}
}

func (m *agentJobs) RunningCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, job := range m.jobs {
		if job.snapshot.State == "running" {
			count++
		}
	}
	return count
}

func (m *agentJobs) Running() bool { return m.RunningCount() > 0 }

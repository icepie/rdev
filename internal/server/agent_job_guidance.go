package server

import (
	"fmt"
	"strings"
	"sync"

	"rdev/internal/agent"
)

const (
	agentMaxPendingGuidance = 8
	agentMaxGuidanceBytes   = 4096
)

// Guidance belongs to one child, not the parent's foreground turn. Closing
// admission and taking a final queued message must be atomic: otherwise a
// successful submit could land between the child's last boundary and return.
type agentJobGuidance struct {
	mu      sync.Mutex
	closed  bool
	pending []string
}

func (g *agentJobGuidance) submit(text string) error {
	text = strings.TrimSpace(text)
	if text == "" || len(text) > agentMaxGuidanceBytes {
		return fmt.Errorf("guidance must contain 1–%d bytes", agentMaxGuidanceBytes)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return fmt.Errorf("subagent is no longer accepting guidance")
	}
	if len(g.pending) >= agentMaxPendingGuidance {
		return fmt.Errorf("at most %d guidance messages may be pending", agentMaxPendingGuidance)
	}
	g.pending = append(g.pending, text)
	return nil
}

func (g *agentJobGuidance) take() *agent.Message { return g.next(false) }

func (g *agentJobGuidance) next(closeIfEmpty bool) *agent.Message {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil
	}
	if len(g.pending) == 0 {
		g.closed = closeIfEmpty
		return nil
	}
	text := g.pending[0]
	g.pending[0] = ""
	g.pending = g.pending[1:]
	return &agent.Message{Role: "user", Content: text}
}

func (g *agentJobGuidance) close() {
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()
}

func (g *agentJobGuidance) snapshot() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.pending...)
}

func (m *agentJobs) Steer(id, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[id]
	if job == nil {
		return fmt.Errorf("unknown job %q in this thread", id)
	}
	if m.closed || job.snapshot.State != "running" || job.guidance == nil {
		return fmt.Errorf("only a running subagent can receive guidance")
	}
	return job.guidance.submit(text)
}

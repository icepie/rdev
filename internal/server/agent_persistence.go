package server

import (
	"context"
	"rdev/internal/agent"
)

// lockAgentThreadPersistence returns with both persistMu and agentSessionsMu
// held, or neither when the thread disappeared. Never call with either held.
func (s *Server) lockAgentThreadPersistence(entry *agentSessionEntry, id string) *agentThread {
	s.agentSessionsMu.Lock()
	th := entry.threads[id]
	s.agentSessionsMu.Unlock()
	if th == nil {
		return nil
	}
	th.persistMu.Lock()
	s.agentSessionsMu.Lock()
	if entry.threads[id] != th {
		s.agentSessionsMu.Unlock()
		th.persistMu.Unlock()
		return nil
	}
	return th
}

// Captured while starting a turn, before reset can replace its session.
type agentTurnSnapshot struct {
	ctx     context.Context
	cancel  context.CancelFunc
	session *agent.Session
	epoch   int
	model   string
}

func snapshotAgentTurnLocked(th *agentThread) agentTurnSnapshot {
	return agentTurnSnapshot{th.turnCtx, th.cancel, th.session, th.epoch, th.model}
}

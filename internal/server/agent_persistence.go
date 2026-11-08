package server

import (
	"context"
	"encoding/json"
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
	display      json.RawMessage
	historyIndex int
	started      int64
	ctx          context.Context
	cancel       context.CancelFunc
	session      *agent.Session
	epoch        int
	model        string
	rpc          *rpcTurnCompletion
}

func snapshotAgentTurnLocked(th *agentThread) agentTurnSnapshot {
	return agentTurnSnapshot{display: th.turnDisplay, historyIndex: th.turnHistIdx, started: th.workingSince.UnixMilli(), ctx: th.turnCtx, cancel: th.cancel, session: th.session, epoch: th.epoch, model: th.model}
}

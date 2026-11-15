package server

import "rdev/internal/agent"

// Caller holds the thread persistence lock. Append checkpoints rather than
// rewriting the transcript at every provider round; compaction folds them into
// the meta record. A null checkpoint is a durable drop/off tombstone.
func (st *agentSessionStore) saveAutonomy(device, thread, model string, goal *agentGoal, loop *agentLoop) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.writableLocked(device, thread) {
		return
	}
	x := st.extras[device+"/"+thread]
	x.Goal, x.Loop = cloneGoal(goal), cloneLoop(loop)
	st.appendCheckpointLocked(device, thread, model, agentHistoryLine{T: "autonomy", Goal: x.Goal, Loop: x.Loop}, x)
}

func (st *agentSessionStore) loadAutonomy(device, thread string) (*agentGoal, *agentLoop) {
	st.mu.Lock()
	defer st.mu.Unlock()
	x := st.extras[device+"/"+thread]
	goal, loop := cloneGoal(x.Goal), cloneLoop(x.Loop)
	if goal != nil && goal.Status == "active" {
		goal.Status = "paused"
	}
	if loop != nil {
		if loop.Status == "active" {
			loop.Status = "paused"
		}
		// A restored duration never silently grants a fresh budget.
		loop.remaining = 0
	}
	return goal, loop
}

func (s *Server) persistAutonomy(entry *agentSessionEntry, th *agentThread) {
	th.persistMu.Lock()
	defer th.persistMu.Unlock()
	s.agentSessionsMu.Lock()
	if entry.threads[th.id] != th {
		s.agentSessionsMu.Unlock()
		return
	}
	goal, loop, model := cloneGoal(th.goal), cloneLoop(th.loop), th.model
	s.agentSessionsMu.Unlock()
	if store := s.agentStore(); store != nil {
		store.saveAutonomy(entry.deviceID, th.id, model, goal, loop)
	}
}

func (s *Server) accountGoalUsage(entry *agentSessionEntry, th *agentThread, goal *agentGoal, epoch int, usage agent.Usage) {
	delta := int64(max(0, usage.InputTokens-usage.CacheReadTokens)) + int64(max(0, usage.OutputTokens))
	if delta == 0 {
		return
	}
	s.agentSessionsMu.Lock()
	if entry.threads[th.id] != th || th.epoch != epoch || th.goal != goal {
		s.agentSessionsMu.Unlock()
		return
	}
	// A user pause can race a provider response. Work already admitted to this
	// goal still consumes budget; pausing must not discard its final usage.
	goal.TokensUsed += delta
	if goal.TokenBudget != nil && goal.TokensUsed >= *goal.TokenBudget && goal.Status == "active" {
		goal.Status = "budget-limited"
		th.pendingReasoning = true
		cancelAutonomyPendingLocked(th)
		if th.cancel != nil {
			th.cancel()
		}
	}
	s.agentSessionsMu.Unlock()
	s.persistAutonomy(entry, th)
	s.publishAutonomy(entry, th, "")
}

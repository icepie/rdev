package server

import "time"

const queueEditLease = time.Minute

func queueHeadReady(entry *agentSessionEntry, th *agentThread, now time.Time) bool {
	return len(th.queue) > 0 && (entry.deviceID == agentServerDeviceID || entry.client != nil) && !now.Before(th.queue[0].editUntil)
}

func (s *Server) applyQueuedModelLocked(entry *agentSessionEntry, th *agentThread, item agentQueuedChat) bool {
	if item.model == "" || item.model == th.model {
		return false
	}
	provider, model, canonical, err := s.Agent.Resolve(item.model)
	if err != nil {
		return false
	}
	session := s.buildAgentSession(entry, th, entry.client, provider, model)
	session.SetHistory(th.session.History())
	th.session, th.sessionClient, th.model, entry.model = session, entry.client, canonical, canonical
	return true
}

// Both turn completion and lease release admit under agentSessionsMu. Only
// one can own the next turn; deleting/resetting a thread invalidates the lease.
func (s *Server) resumeAgentQueue(entry *agentSessionEntry, th *agentThread) {
	s.agentSessionsMu.Lock()
	if entry.threads[th.id] != th || th.running || !queueHeadReady(entry, th, time.Now()) {
		s.agentSessionsMu.Unlock()
		s.scheduleAutonomy(entry, th)
		return
	}
	next := takeQueuedChat(&th.queue, 0)
	dirty := s.applyQueuedModelLocked(entry, th, next)
	frame := s.startAgentTurnLocked(entry, th, next.text, next.images, next.display)
	turn := snapshotAgentTurnLocked(th)
	s.agentSessionsMu.Unlock()
	if dirty {
		s.writeAgentPrefs(entry)
	}
	s.broadcastQueue(entry, th)
	s.agentBroadcastTurn(entry, th, frame)
	s.goAgent(func() { s.runAgentTurn(entry, th, turn, next.text, next.images) })
}

func (s *Server) expireQueueEdit(entry *agentSessionEntry, th *agentThread, id, token string, until time.Time) {
	s.agentSessionsMu.Lock()
	if entry.threads[th.id] != th {
		s.agentSessionsMu.Unlock()
		return
	}
	changed := false
	for i := range th.queue {
		item := &th.queue[i]
		if item.id == id && item.editToken == token && item.editUntil.Equal(until) && !time.Now().Before(until) {
			item.editOwner = nil
			item.editToken = ""
			item.editUntil = time.Time{}
			changed = true
			break
		}
	}
	s.agentSessionsMu.Unlock()
	if changed {
		s.resumeAgentQueue(entry, th)
	}
}

func (s *Server) releaseQueueEdits(ac *agentConn) {
	s.agentSessionsMu.Lock()
	affected := make([]*agentThread, 0)
	for _, th := range ac.entry.threads {
		changed := false
		for i := range th.queue {
			item := &th.queue[i]
			if item.editOwner == ac.socket && item.editToken != "" {
				item.editOwner = nil
				item.editToken = ""
				item.editUntil = time.Time{}
				changed = true
			}
		}
		if changed {
			affected = append(affected, th)
		}
	}
	s.agentSessionsMu.Unlock()
	for _, th := range affected {
		s.resumeAgentQueue(ac.entry, th)
	}
}

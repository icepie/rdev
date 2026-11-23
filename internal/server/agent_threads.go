package server

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// handleReset clears a thread's history, todos and stored file. A running
// turn is canceled via the epoch mechanism: its unwind skips the save and
// the terminal broadcast, so the cleared state is never resurrected.
func (h *agentWSHandler) handleReset(ac *agentConn, threadRef string) {
	entry := ac.entry
	if entry == nil {
		ac.sendError("agent session not ready")
		return
	}
	threadID := ac.current
	if threadRef != "" {
		threadID = threadRef
	}
	srv := h.srv
	th := srv.lockAgentThreadPersistence(entry, threadID)
	if th == nil {
		ac.sendErrorThread(threadID, "unknown thread "+threadID)
		return
	}
	defer th.persistMu.Unlock()
	if store := srv.agentStore(); store != nil {
		store.mu.Lock()
		writable := store.writableLocked(entry.deviceID, threadID)
		store.mu.Unlock()
		if !writable {
			srv.agentSessionsMu.Unlock()
			ac.sendErrorThread(threadID, "recover damaged history before resetting it")
			return
		}
	}
	cancel := th.cancel
	oldAutomation := th.automation
	th.automation = nil
	cancelAutonomyPendingLocked(th)
	th.goal, th.loop = nil, nil
	th.goalActivity = ""
	th.epoch++
	th.scheduled = nil
	th.running = false
	th.turnHistory = nil
	th.archive = nil
	th.cancel = nil
	th.turnCtx = nil
	th.workingTool = ""
	if th.jobs != nil {
		th.jobs.Close()
		th.jobs = nil
	}
	// Fresh session with the thread's own model and an empty history.
	provider, model, _, err := srv.Agent.Resolve(th.model)
	if err != nil {
		provider, model, _, _ = srv.Agent.Resolve("")
	}
	th.session = srv.buildAgentSession(entry, th, entry.client, provider, model)
	th.sessionClient = entry.client
	th.todos = nil
	th.todoPhases = nil
	th.turnFiles = nil
	th.queue = nil
	th.lastUsage = nil
	th.lastOutcome = ""
	th.outcomeAt = ""
	th.diagnostic = nil
	th.alwaysAllow = make(map[string]bool)
	th.branch = ""
	th.live.Reset()
	th.updatedAt = time.Now()
	denyApprovalsLocked(th)
	srv.agentSessionsMu.Unlock()
	srv.invalidateScheduledThread(entry.deviceID, threadID, "target thread reset")
	srv.retireAutomation(oldAutomation)
	if cancel != nil {
		cancel()
	}
	if store := srv.agentStore(); store != nil {
		store.clear(ac.deviceID, threadID)
	}
	srv.agentBroadcast(entry, agentMsg{Op: "reset_ok", Thread: threadID})
	srv.publishAutonomy(entry, th, "")
	srv.broadcastThreadList(entry)
}

// handleNewThread creates an empty thread and switches to it.
func (h *agentWSHandler) handleNewThread(ac *agentConn) {
	entry := ac.entry
	if entry == nil {
		ac.sendError("agent session not ready")
		return
	}
	srv := h.srv
	srv.agentSessionsMu.Lock()
	th := srv.newAgentThreadLocked(entry, newAgentThreadID(), nil, "", "")
	th.persistMu.Lock() // new thread is not published yet; no competing owner
	entry.threads[th.id] = th
	entry.current = th.id // resume default for future connections
	ac.current = th.id
	srv.agentSessionsMu.Unlock()

	// Persist explicit empty conversations too, so restoring a split after a
	// server restart does not silently replace its newly-created thread.
	srv.agentSessionsMu.Lock()
	current := entry.threads[th.id] == th
	model := th.model
	srv.agentSessionsMu.Unlock()
	if current {
		if store := srv.agentStore(); store != nil {
			store.replace(entry.deviceID, th.id, model, nil)
		}
	}
	th.persistMu.Unlock()

	srv.writeAgentPrefs(entry)
	srv.broadcastThreadList(entry)
	// Only the requesting connection switches; other browsers keep their
	// current thread.
	ac.writeJSON(agentMsg{
		Op:      "thread_switched",
		Thread:  th.id,
		History: []agentHistoryEntry{},
		Todos:   []todoItem{},
		Queue:   []string{},
	})
	srv.scheduleThreadMetaProbe(entry, th.id)
}

// handleSwitchThread makes another thread current and replays its state.
func (h *agentWSHandler) handleSwitchThread(ac *agentConn, threadID string) {
	entry := ac.entry
	if entry == nil {
		ac.sendError("agent session not ready")
		return
	}
	srv := h.srv
	srv.agentSessionsMu.Lock()
	th, ok := entry.threads[threadID]
	if !ok {
		srv.agentSessionsMu.Unlock()
		ac.sendErrorThread(threadID, "unknown thread "+threadID)
		return
	}
	if threadID == ac.current {
		// Already viewing this thread (double-click, retry, or the auth
		// URL adoption where auth_ok already delivered the snapshot):
		// replaying history again would flash the transcript.
		srv.agentSessionsMu.Unlock()
		return
	}
	entry.current = threadID // resume default
	ac.current = threadID
	snap := srv.threadSnapshotLocked(entry, th)
	model := th.model
	srv.agentSessionsMu.Unlock()

	// thread_switched is per-connection: only the requester's view changes.
	ac.writeJSON(agentMsg{
		Goal: snap.goal, Loop: snap.loop,
		Op:             "thread_switched",
		Thread:         threadID,
		Model:          model,
		History:        snap.history,
		Diagnostic:     snap.diagnostic,
		HistoryHasMore: snap.historyHasMore, HistoryBefore: snap.historyBefore,
		Todos:        snap.todos,
		Queue:        snap.queue,
		QueueIDs:     snap.queueIDs,
		QueueModes:   snap.queueModes,
		Running:      snap.running,
		Working:      snap.working,
		Live:         snap.live,
		Automation:   snap.automation,
		Participants: snap.participants,
		Usage:        snap.usage,
	})
	srv.writeAgentPrefs(entry)
	srv.broadcastThreadList(entry)
	srv.scheduleThreadMetaProbe(entry, threadID)
}

// handlePeekThread replays a thread's state (history + live frames) to the
// requester WITHOUT making it current: split-screen panes subscribe to
// threads they do not drive. Turn frames are already broadcast to all conns
// tagged with their thread id, so after this seed the browser routes them by
// thread id itself.
func (h *agentWSHandler) handlePeekThread(ac *agentConn, threadID string) {
	entry := ac.entry
	if entry == nil {
		ac.sendError("agent session not ready")
		return
	}
	srv := h.srv
	srv.agentSessionsMu.Lock()
	th, ok := entry.threads[threadID]
	if !ok {
		srv.agentSessionsMu.Unlock()
		ac.sendErrorThread(threadID, "unknown thread "+threadID)
		return
	}
	snap := srv.threadSnapshotLocked(entry, th)
	srv.agentSessionsMu.Unlock()

	ac.writeJSON(snap.peekMessage(threadID))
}

func (snap agentThreadSnapshot) peekMessage(threadID string) agentMsg {
	return agentMsg{
		Goal: snap.goal, Loop: snap.loop,
		Op:             "thread_peeked",
		Thread:         threadID,
		History:        snap.history,
		HistoryHasMore: snap.historyHasMore, HistoryBefore: snap.historyBefore,
		HistoryError: snap.historyError,
		Diagnostic:   snap.diagnostic,
		Todos:        snap.todos,
		Queue:        snap.queue,
		QueueIDs:     snap.queueIDs,
		QueueModes:   snap.queueModes,
		Running:      snap.running,
		Working:      snap.working,
		Live:         snap.live,
		Automation:   snap.automation,
		Participants: snap.participants,
		Usage:        snap.usage,
	}
}

func (h *agentWSHandler) handleRecoverHistory(ac *agentConn, threadID string) {
	if threadID == "" {
		threadID = ac.current
	}
	entry := ac.entry
	if entry == nil {
		ac.sendError("agent session not ready")
		return
	}
	th := h.srv.lockAgentThreadPersistence(entry, threadID)
	if th == nil {
		ac.sendErrorThread(threadID, "unknown thread "+threadID)
		return
	}
	defer th.persistMu.Unlock()
	if th.running || th.autonomyPending {
		h.srv.agentSessionsMu.Unlock()
		ac.sendErrorThread(threadID, "stop the active turn before recovering history")
		return
	}
	h.srv.agentSessionsMu.Unlock()
	store := h.srv.agentStore()
	if store == nil {
		ac.sendErrorThread(threadID, "history persistence is disabled")
		return
	}
	count, backup, err := store.recover(entry.deviceID, threadID)
	if err != nil {
		ac.sendErrorThread(threadID, "history recovery failed: "+err.Error())
		return
	}
	messages, _, _, _ := store.load(entry.deviceID, threadID)
	goal, loop := store.loadAutonomy(entry.deviceID, threadID)
	store.mu.Lock()
	extras := store.extras[entry.deviceID+"/"+threadID]
	store.mu.Unlock()
	h.srv.agentSessionsMu.Lock()
	if entry.threads[threadID] == th {
		th.session.SetHistory(messages)
		th.goal, th.loop = goal, loop
		th.lastOutcome, th.outcomeAt = restoredAgentOutcome(extras.Outcome), extras.OutcomeAt
		th.diagnostic = extras.Diagnostic
		th.turnHistory = nil
		th.live.Reset()
	}
	snap := h.srv.threadSnapshotLocked(entry, th)
	h.srv.agentSessionsMu.Unlock()
	ac.writeJSON(agentMsg{Op: "history_recovered", Thread: threadID, Recovered: count, Message: "Recovered valid records; original preserved at " + backup})
	ac.writeJSON(snap.peekMessage(threadID))
	h.srv.agentBroadcast(entry, snap.peekMessage(threadID))
	h.srv.broadcastThreadList(entry)
}

// connsViewThreadLocked reports whether any subscribed connection has the
// given thread as its current. Caller holds agentSessionsMu.
func connsViewThreadLocked(entry *agentSessionEntry, threadID string) bool {
	for sock := range entry.subs {
		sub, _ := sock.Session().Load("agentConn")
		if conn, _ := sub.(*agentConn); conn != nil && conn.current == threadID {
			return true
		}
	}
	return false
}

// handleDeleteThread cancels active work and removes a thread. Every
// connection viewing the deleted thread is switched to the most recently
// updated remaining one, or a fresh thread when none is left.
func (h *agentWSHandler) handleDeleteThread(ac *agentConn, threadID string) {
	entry := ac.entry
	if entry == nil {
		ac.sendError("agent session not ready")
		return
	}
	srv := h.srv
	srv.agentSessionsMu.Lock()
	target := entry.threads[threadID]
	srv.agentSessionsMu.Unlock()
	if target != nil {
		if err := srv.AgentWorktreeDeletionGuard(context.Background(), entry, target); err != nil {
			ac.sendErrorThread(threadID, err.Error())
			return
		}
	}
	th := srv.lockAgentThreadPersistence(entry, threadID)
	if th == nil {
		ac.sendError("unknown thread " + threadID)
		return
	}
	defer th.persistMu.Unlock()
	// Recheck after acquiring persistence ownership: a create may have completed
	// between the preliminary guard and this lock. Never orphan its checkout.
	srv.agentSessionsMu.Unlock()
	_, managed, worktreeErr := srv.AgentWorktreeRecord(entry.deviceID, threadID)
	srv.agentSessionsMu.Lock()
	if worktreeErr != nil || managed || entry.worktreeBusy {
		srv.agentSessionsMu.Unlock()
		message := "remove the managed worktree before deleting its conversation"
		if worktreeErr != nil {
			message = worktreeErr.Error()
		}
		ac.sendErrorThread(threadID, message)
		return
	}
	if store := srv.agentStore(); store != nil {
		store.mu.Lock()
		writable := store.writableLocked(entry.deviceID, threadID)
		store.mu.Unlock()
		if !writable {
			srv.agentSessionsMu.Unlock()
			ac.sendErrorThread(threadID, "recover damaged history before deleting it")
			return
		}
	}
	// Invalidate the old generation before canceling: its late events and
	// final save cannot recreate the deleted conversation.
	th.epoch++
	cancelAutonomyPendingLocked(th)
	cancel := th.cancel
	oldAutomation := th.automation
	th.automation = nil
	th.queue = nil
	th.turnFiles = nil
	denyApprovalsLocked(th)
	if th.jobs != nil {
		th.jobs.Close()
	}
	delete(entry.threads, threadID)
	// Remap every connection viewing the deleted thread (plus the resume
	// default) to the most recently updated remaining one, or a fresh
	// thread when none is left.
	var affected []*agentConn
	switchedTo := ""
	if entry.current == threadID || connsViewThreadLocked(entry, threadID) {
		var best *agentThread
		for _, other := range entry.threads {
			if best == nil || other.updatedAt.After(best.updatedAt) {
				best = other
			}
		}
		if best == nil {
			best = srv.newAgentThreadLocked(entry, newAgentThreadID(), nil, "", "")
			entry.threads[best.id] = best
		}
		switchedTo = best.id
		if entry.current == threadID {
			entry.current = best.id
		}
		for sock := range entry.subs {
			sub, _ := sock.Session().Load("agentConn")
			conn, _ := sub.(*agentConn)
			if conn == nil || conn.current != threadID {
				continue
			}
			conn.current = best.id
			affected = append(affected, conn)
		}
	}
	var snap agentThreadSnapshot
	var switchModel string
	if switchedTo != "" {
		cur := entry.threads[switchedTo]
		snap = srv.threadSnapshotLocked(entry, cur)
		switchModel = cur.model
	}
	srv.agentSessionsMu.Unlock()
	srv.invalidateScheduledThread(entry.deviceID, threadID, "target thread deleted")
	srv.retireAutomation(oldAutomation)
	if cancel != nil {
		cancel()
	}

	if store := srv.agentStore(); store != nil {
		store.clear(ac.deviceID, threadID)
		store.dropExtras(ac.deviceID, threadID)
	}
	ac.writeJSON(agentMsg{Op: "thread_deleted", Thread: threadID, Device: ac.deviceID})
	// The request acknowledgment carries its correlation ID. The separate
	// notification also lets other attached browsers discard local drafts.
	srv.agentBroadcast(entry, agentMsg{Op: "thread_deleted", Thread: threadID, Device: ac.deviceID})
	srv.broadcastThreadList(entry)
	if switchedTo != "" {
		srv.writeAgentPrefs(entry)
		// Each affected connection is switched individually; unaffected
		// browsers stay on their thread.
		for _, conn := range affected {
			conn.writeJSON(agentMsg{
				Goal: snap.goal, Loop: snap.loop,
				Op:             "thread_switched",
				Thread:         switchedTo,
				Model:          switchModel,
				History:        snap.history,
				Diagnostic:     snap.diagnostic,
				HistoryHasMore: snap.historyHasMore, HistoryBefore: snap.historyBefore,
				Todos:        snap.todos,
				Queue:        snap.queue,
				QueueIDs:     snap.queueIDs,
				QueueModes:   snap.queueModes,
				Running:      snap.running,
				Working:      snap.working,
				Live:         snap.live,
				Automation:   snap.automation,
				Participants: snap.participants,
				Usage:        snap.usage,
			})
		}
		srv.scheduleThreadMetaProbe(entry, switchedTo)
	}
}

// handleRenameThread sets a thread's explicit title (persisted in the meta
// line) and rebroadcasts the thread list.
func (h *agentWSHandler) handleRenameThread(ac *agentConn, threadID, title string) {
	entry := ac.entry
	if entry == nil {
		ac.sendError("agent session not ready")
		return
	}
	title = strings.TrimSpace(title)
	if title == "" {
		ac.sendError("title must not be empty")
		return
	}
	srv := h.srv
	th := srv.lockAgentThreadPersistence(entry, threadID)
	if th == nil {
		ac.sendError("unknown thread " + threadID)
		return
	}
	defer th.persistMu.Unlock()
	th.title = title
	history := th.session.History()
	srv.agentSessionsMu.Unlock()

	if store := srv.agentStore(); store != nil {
		store.setTitle(ac.deviceID, threadID, title, history)
	}
	ac.writeJSON(agentMsg{Op: "thread_renamed", Thread: threadID, Title: title, Device: ac.deviceID})
	srv.broadcastThreadList(entry)
}

// handleSetCwd sets the current thread's working directory ("" restores the
// device default): the session is rebuilt so tools and the system prompt
// pick it up, the meta line is rewritten, and a branch probe is scheduled.
func (h *agentWSHandler) handleSetCwd(ac *agentConn, path string) {
	entry := ac.entry
	if entry == nil {
		ac.sendError("agent session not ready")
		return
	}
	path = strings.TrimSpace(path)
	srv := h.srv
	th := srv.lockAgentThreadPersistence(entry, ac.current)
	if th == nil {
		ac.sendError("agent session not ready")
		return
	}
	defer th.persistMu.Unlock()
	if th.running || entry.worktreeBusy {
		srv.agentSessionsMu.Unlock()
		ac.sendError("agent is busy; stop first")
		return
	}
	th.cwd = path
	if th.deviceScope != nil {
		th.deviceScope.mu.Lock()
		th.deviceScope.members[entry.deviceID] = path
		th.deviceScope.mu.Unlock()
	}
	th.branch = ""
	// Invalidate an already-running probe immediately. This matters when a
	// probe for the same path races with a rapid cwd reset before the next
	// probe has been scheduled.
	th.metaProbeSeq++
	// Rebuild the session so tools and the system prompt use the new cwd;
	// the thread keeps its own model.
	if provider, model, err := srv.resolveAgentModel(th.model); err == nil {
		newSession := srv.buildAgentSession(entry, th, entry.client, provider, model)
		newSession.SetHistory(th.session.History())
		th.session = newSession
		th.sessionClient = entry.client
	}
	history := th.session.History()
	threadID := th.id
	cwd := agentEffectiveCwdLocked(entry, th)
	srv.agentSessionsMu.Unlock()

	if store := srv.agentStore(); store != nil {
		store.setCwd(ac.deviceID, threadID, path, history)
	}
	srv.agentBroadcast(entry, agentMsg{Op: "thread_meta", Thread: threadID, Cwd: cwd})
	srv.scheduleThreadMetaProbe(entry, threadID)
}

// threadActionEntry allows authenticated management of offline history without
// switching the browser's active device or disrupting its live conversation.
func (h *agentWSHandler) threadActionEntry(ac *agentConn, device string) (*agentSessionEntry, error) {
	if device == "" || device == ac.deviceID {
		return ac.entry, nil
	}
	s := h.srv
	s.mu.RLock()
	client := s.clients[device]
	s.mu.RUnlock()
	if !h.adminBypass && (s.AdminToken != "" || client == nil || client.Password != "") {
		return nil, fmt.Errorf("administrator authentication is required to manage another device's conversations")
	}
	return s.agentSessionFor(device, client)
}

func (h *agentWSHandler) handleThreadAction(ac *agentConn, msg agentMsg) {
	device := msg.Device
	if device == "" {
		device = ac.deviceID
	}
	entry, err := h.threadActionEntry(ac, device)
	if err != nil {
		ac.writeJSON(agentMsg{Op: "thread_action_error", ID: msg.ID, Device: device, Thread: msg.Thread, Message: err.Error()})
		return
	}
	scoped := &agentConn{responseTo: ac, deviceID: device, entry: entry, responseThread: msg.Thread, responseID: msg.ID}
	if msg.Op == "delete_thread" {
		h.handleDeleteThread(scoped, msg.Thread)
	} else if msg.Op == "pin_thread" {
		h.handlePinThread(scoped, msg.Thread, msg.Pinned)
	} else {
		h.handleRenameThread(scoped, msg.Thread, msg.Title)
	}
}

func (h *agentWSHandler) handlePinThread(ac *agentConn, id string, pinned bool) {
	if ac.entry == nil {
		ac.sendError("agent session not ready")
		return
	}
	s := h.srv
	// Serialize persistence with other preference edits, without holding the
	// global state lock over disk I/O. Roll back failed writes before publishing.
	ac.entry.prefsMu.Lock()
	s.agentSessionsMu.Lock()
	th := ac.entry.threads[id]
	if th == nil {
		s.agentSessionsMu.Unlock()
		ac.entry.prefsMu.Unlock()
		ac.sendError("unknown thread")
		return
	}
	previous := th.pinnedAt
	if pinned && th.pinnedAt == 0 {
		th.pinnedAt = time.Now().UnixMilli()
	} else if !pinned {
		th.pinnedAt = 0
	}
	prefs := s.agentPrefsLocked(ac.entry)
	s.agentSessionsMu.Unlock()
	err := s.saveAgentPrefs(ac.deviceID, prefs)
	if err != nil {
		s.agentSessionsMu.Lock()
		if ac.entry.threads[id] == th {
			th.pinnedAt = previous
		}
		s.agentSessionsMu.Unlock()
	}
	ac.entry.prefsMu.Unlock()
	if err != nil {
		ac.sendError("could not save pinned conversation")
		return
	}
	ac.writeJSON(agentMsg{Op: "thread_pinned", Device: ac.deviceID, Thread: id, Pinned: pinned})
	s.broadcastThreadList(ac.entry)
}

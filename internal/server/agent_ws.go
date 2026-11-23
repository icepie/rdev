package server

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"

	"rdev/internal/agent"

	"github.com/lxzan/gws"
)

// agentConn is the per-connection (browser tab) state stored in
// socket.Session(); the shared session/turn state lives in entry.
// current is THIS connection's selected thread: browsers are independent
// (one browser switching threads must not yank another browser's view);
// entry.current only seeds new connections and persists as the resume
// default.
type agentConn struct {
	// Optional synchronous reply sink for in-process management tools.
	reply func(agentMsg)
	// Management views share the original connection write lock.
	responseTo     *agentConn
	responseThread string
	responseID     string
	socket         *gws.Conn
	deviceID       string
	client         *ClientConn
	authed         bool
	entry          *agentSessionEntry
	current        string

	writeMu sync.Mutex
}

// writeData sends one pre-marshaled frame; reports whether the write worked
// so broadcasts can prune dead subscribers.
func (ac *agentConn) writeData(data []byte) bool {
	ac.writeMu.Lock()
	defer ac.writeMu.Unlock()
	_ = ac.socket.SetWriteDeadline(time.Now().Add(wsWriteWait))
	defer ac.socket.SetWriteDeadline(time.Time{})
	if err := ac.socket.WriteMessage(gws.OpcodeText, data); err != nil {
		log.Printf("agent write failed: device=%s err=%v", ac.deviceID, err)
		return false
	}
	return true
}

func (ac *agentConn) writeJSON(msg agentMsg) {
	if ac.reply != nil {
		ac.reply(msg)
		return
	}
	if ac.responseTo != nil {
		if msg.Op == "error" {
			msg.Op = "thread_action_error"
			msg.Thread = ac.responseThread
		}
		msg.Device = ac.deviceID
		msg.ID = ac.responseID
		ac.responseTo.writeJSON(msg)
		return
	}
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("agent marshal error: %v", err)
		return
	}
	ac.writeData(data)
}

// sendError reports an unattributable failure (auth, device misconfig):
// the frame carries no thread tag and only lands on the page status.
func (ac *agentConn) sendError(message string) {
	ac.writeJSON(agentMsg{Op: "error", Message: message})
}

// sendErrorThread reports a failure attributable to one thread: the tagged
// frame routes to that thread's transcript sink.
func (ac *agentConn) sendErrorThread(thread, message string) {
	ac.writeJSON(agentMsg{Op: "error", Thread: thread, Message: message})
}

// writeChanges sends a get_changes reply to this socket.
func (ac *agentConn) writeChanges(msg agentChangesMsg) {
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("agent marshal error: %v", err)
		return
	}
	ac.writeData(data)
}

// writeDiff sends a get_diff reply to this socket.
func (ac *agentConn) writeDiff(msg agentDiffMsg) {
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("agent marshal error: %v", err)
		return
	}
	ac.writeData(data)
}

type agentWSHandler struct {
	gws.BuiltinEventHandler
	srv         *Server
	adminBypass bool // valid admin token: skip device password
}

func (h *agentWSHandler) OnOpen(socket *gws.Conn) {
	deviceIDI, _ := socket.Session().Load("deviceID")
	if deviceIDI == nil {
		socket.WriteClose(1000, []byte("missing device parameter"))
		return
	}
	deviceID := deviceIDI.(string)

	h.srv.mu.RLock()
	client, ok := h.srv.clients[deviceID]
	h.srv.mu.RUnlock()
	if deviceID == agentServerDeviceID {
		client = &ClientConn{ID: agentServerDeviceID}
		ok = true
	}
	if !ok && !h.adminBypass {
		// Offline history needs admin authorization: device passwords cannot be checked without a live device.
		payload, _ := json.Marshal(agentMsg{Op: "error", Code: "offline_admin_required", Message: "Offline history requires a server administrator token; the device password cannot be verified until the device reconnects."})
		_ = socket.WriteMessage(gws.OpcodeText, payload)
		socket.WriteClose(1013, []byte("device is temporarily offline"))
		return
	}

	ac := &agentConn{socket: socket, deviceID: deviceID, client: client}
	socket.Session().Store("agentConn", ac)

	if h.srv.Agent == nil || !h.srv.Agent.Enabled() {
		ac.sendError("agent not configured: set --agent-api-key / RDEV_AGENT_API_KEY")
		socket.WriteClose(1000, nil)
		return
	}

	if client != nil && client.Password != "" && !h.adminBypass {
		ac.writeJSON(agentMsg{Op: "auth", Message: "Device '" + deviceID + "' requires password"})
		return
	}

	ac.authed = true
	h.establish(ac, client)
}

// denyApprovalsLocked resolves every pending approval of a thread with deny
// and every pending question with a dismissal. Caller must hold
// agentSessionsMu.
func denyApprovalsLocked(th *agentThread) {
	for id, ch := range th.approvals {
		delete(th.approvals, id)
		ch <- approvalDecision{allow: false}
	}
	for id, ch := range th.questions {
		delete(th.questions, id)
		ch <- ""
	}
}

// establish finishes the auth handshake: attach this socket to the
// server-owned per-device agent entry and tell the browser it can chat.
func (h *agentWSHandler) establish(ac *agentConn, client *ClientConn) {
	entry, err := h.srv.agentSessionFor(ac.deviceID, client)
	if err != nil {
		ac.sendError("agent misconfigured: " + err.Error())
		return
	}
	h.srv.agentSessionsMu.Lock()
	if entry.client != client {
		// Device reconnected since the entry was created.
		entry.client = client
	}
	// Compare against the persisted prefs (not entry.os, which was just
	// seeded from the same client) so a first-ever attach also writes.
	osLearned := ""
	if os := clientOS(client); os != "" {
		entry.os = os
		if h.srv.loadAgentPrefs(entry.deviceID).OS != os {
			osLearned = os
		}
	}
	h.srv.rebindAgentSessionLocked(entry)
	entry.subs[ac.socket] = true
	entry.lastActive = time.Now()
	// Seed this connection's current thread from the entry's remembered
	// default; afterwards the two diverge freely.
	current := entry.current
	th := entry.threads[current]
	// auth_ok reports the current thread's model (threads may differ).
	model := entry.model
	threadCwd, threadBranch := "", ""
	var snap agentThreadSnapshot
	if th != nil {
		if th.model != "" {
			model = th.model
		}
		threadCwd = agentEffectiveCwdLocked(entry, th)
		threadBranch = th.branch
		snap = h.srv.threadSnapshotLocked(entry, th)
	}
	threads := agentThreadListLocked(entry)
	permMode := entry.permMode
	reasoningLevel := entry.reasoningLevel
	h.srv.agentSessionsMu.Unlock()
	ac.entry = entry
	ac.current = current
	// Persist a freshly learned OS so the global thread list can render an
	// icon for this device even while it is offline.
	if osLearned != "" {
		h.srv.writeAgentPrefs(entry)
	}

	modelName := model
	if _, mc, _, rerr := h.srv.Agent.Resolve(model); rerr == nil {
		modelName = mc.Name
		if modelName == "" {
			modelName = mc.ID
		}
	}
	ac.writeJSON(agentMsg{
		Goal: snap.goal, Loop: snap.loop,
		Op:             "auth_ok",
		Offline:        ac.deviceID != agentServerDeviceID && client == nil,
		Device:         ac.deviceID,
		Model:          model,
		ModelName:      modelName,
		Models:         h.srv.Agent.Models(),
		HistoryError:   snap.historyError,
		Diagnostic:     snap.diagnostic,
		History:        snap.history,
		HistoryHasMore: snap.historyHasMore, HistoryBefore: snap.historyBefore,
		Running:        snap.running,
		Thread:         current,
		Threads:        threads,
		Todos:          snap.todos,
		Queue:          snap.queue,
		QueueIDs:       snap.queueIDs,
		QueueModes:     snap.queueModes,
		AutoApprove:    permMode == agentPermFull, // legacy field
		PermissionMode: permMode,
		ReasoningLevel: reasoningLevel,
		Working:        snap.working,
		Cwd:            threadCwd,
		Branch:         threadBranch,
		Live:           snap.live,
		Automation:     snap.automation,
		Participants:   snap.participants,
		Usage:          snap.usage,
	})
	// The sidebar shows threads of every device (not partitioned per
	// device), so a freshly attached browser gets the global list too.
	ac.writeJSON(agentMsg{Op: "all_threads", Devices: h.srv.collectAllThreads()})
	// Probe the current thread's git branch (off-lock) and broadcast the
	// result as thread_meta.
	h.srv.scheduleThreadMetaProbe(entry, current)
}

func (h *agentWSHandler) OnClose(socket *gws.Conn, err error) {
	acRaw, _ := socket.Session().Load("agentConn")
	if acRaw == nil {
		return
	}
	ac := acRaw.(*agentConn)
	// Only detach: the turn (if any) keeps running server-side so a reopened
	// tab can reattach and receive remaining events and pending approvals.
	// Approval timeout and turn cancellation still resolve unanswered requests.
	if ac.entry != nil {
		h.srv.agentSessionsMu.Lock()
		delete(ac.entry.subs, socket)
		h.srv.agentSessionsMu.Unlock()
		h.srv.releaseQueueEdits(ac)
	}
}

func (h *agentWSHandler) OnMessage(socket *gws.Conn, message *gws.Message) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC in agent OnMessage: %v", r)
		}
	}()
	defer message.Close()

	if message.Opcode != gws.OpcodeText {
		return
	}

	acRaw, _ := socket.Session().Load("agentConn")
	if acRaw == nil {
		return
	}
	ac := acRaw.(*agentConn)

	if h.handleAgentRPCFrame(ac, message.Bytes()) {
		return
	}
	var msg agentMsg
	if err := json.Unmarshal(message.Bytes(), &msg); err != nil {
		return
	}

	// In-band device password auth (same flow as terminal.go).
	if !ac.authed {
		if msg.Op != "auth" {
			return
		}
		h.srv.mu.RLock()
		client, ok := h.srv.clients[ac.deviceID]
		h.srv.mu.RUnlock()
		if !ok {
			ac.sendError("device '" + ac.deviceID + "' disconnected")
			socket.WriteClose(1000, nil)
			return
		}
		// Brute-force throttle: reject during cooldown without comparing.
		if h.srv.authLimiter.wait(ac.deviceID) > 0 {
			ac.writeJSON(agentMsg{Op: "auth_fail", Message: "Too many attempts, retry later"})
			return
		}
		if client.Password != "" && constantTimeEqual(client.Password, msg.Password) {
			h.srv.authLimiter.success(ac.deviceID)
			ac.authed = true
			h.establish(ac, client)
		} else {
			h.srv.authLimiter.fail(ac.deviceID)
			ac.writeJSON(agentMsg{Op: "auth_fail", Message: "Wrong password"})
		}
		return
	}

	if ac.deviceID != agentServerDeviceID {
		if _, online := h.srv.GetClient(ac.deviceID); !online && !h.hasOnlineParticipant(ac, msg.Thread) && (msg.Op == "steer" || msg.Op == "promote_queue" || msg.Op == "steer_job" || msg.Op == "steer_subagent") {
			ac.sendErrorThread(msg.Thread, "Device offline: guidance and device jobs are unavailable")
			return
		}
	}
	switch msg.Op {
	case "goal", "loop":
		h.handleAutonomy(ac, msg)
	case "automation_control":
		h.handleAutomation(ac, msg)
	case "get_jobs":
		h.handleJobs(ac, msg.Thread, msg.ID, false)
	case "cancel_job":
		if msg.JobID != "" {
			h.handleCancelJob(ac, msg)
		} else {
			h.handleJobs(ac, msg.Thread, msg.ID, true)
		}
	case "cancel_subagent":
		h.handleCancelSubagent(ac, msg)
	case "get_subagents":
		h.handleGetSubagents(ac, msg)
	case "steer_job", "steer_subagent":
		h.handleJobGuidance(ac, msg)
	case "chat":
		if h.handleAutonomySlash(ac, msg.Text, msg.Thread, msg.Images) {
			return
		}
		if ac.deviceID != agentServerDeviceID {
			live, online := h.srv.GetClient(ac.deviceID)
			collaboratorsOnline := h.hasOnlineParticipant(ac, msg.Thread)
			if !online && !h.hasOnlineParticipant(ac, msg.Thread) && (!msg.OfflineConfirmed || strings.HasPrefix(strings.TrimSpace(msg.Text), "/")) {
				ac.sendErrorThread(msg.Thread, "Device offline: confirm historical-context chat first; device commands are unavailable")
				return
			}
			h.srv.agentSessionsMu.Lock()
			threadID := msg.Thread
			if threadID == "" {
				threadID = ac.current
			}
			if th := ac.entry.threads[threadID]; !online && !collaboratorsOnline && th != nil && th.running {
				h.srv.agentSessionsMu.Unlock()
				ac.sendErrorThread(threadID, "Wait for the active turn to finish before starting offline discussion")
				return
			}
			ac.entry.client = live
			h.srv.rebindAgentSessionLocked(ac.entry)
			h.srv.agentSessionsMu.Unlock()
		}
		h.handleChat(ac, msg.Text, msg.Model, msg.Thread, msg.Images)
	case "approve":
		h.handleApprove(ac, msg.ID, msg.Allow, msg.Always)
	case "ask_response":
		h.handleAskResponse(ac, msg.ID, msg.Answer)
	case "steer":
		h.handleSteer(ac, msg.Text, msg.Thread, msg.Images)
	case "hold_queue", "release_queue", "update_queue", "reorder_queue":
		h.handleQueueMutation(ac, msg)
	case "promote_queue":
		h.handlePromoteQueued(ac, msg.Thread, msg.QueueID)
	case "dequeue":
		h.handleDequeueItem(ac, msg.Thread, msg.Index, msg.QueueID, msg.ID, msg.Restore)
	case "stop":
		if ac.entry == nil {
			return
		}
		threadID := ac.current
		if msg.Thread != "" {
			threadID = msg.Thread
		}
		var cancel context.CancelFunc
		var th *agentThread
		h.srv.agentSessionsMu.Lock()
		th = ac.entry.threads[threadID]
		if th != nil {
			pauseAutonomyLocked(th)
		}
		if th != nil && th.running {
			cancel = th.cancel
			// Stop kills the pending queue too: a stopped turn must not
			// start follow-ups the user just aborted.
			th.queue = nil
			denyApprovalsLocked(th)
		}
		h.srv.agentSessionsMu.Unlock()
		if th != nil {
			h.srv.persistAutonomy(ac.entry, th)
			h.srv.publishAutonomy(ac.entry, th, "")
		}
		if cancel != nil {
			// The turn unwind emits the single stopped{canceled:true}.
			cancel()
			h.srv.broadcastQueue(ac.entry, th)
		} else {
			// Idle stop: plain ack, no transcript effect.
			ac.writeJSON(agentMsg{Op: "stopped", Thread: threadID, Canceled: false})
		}
	case "reset":
		h.handleReset(ac, msg.Thread)
	case "set_auto_approve":
		// Legacy binary toggle: true → full, false → write.
		if ac.entry == nil {
			return
		}
		mode := agentPermWrite
		if msg.Value {
			mode = agentPermFull
		}
		h.setPermissionMode(ac, mode)
	case "set_permission_mode":
		if ac.entry == nil {
			return
		}
		if !validPermMode(msg.Mode) {
			ac.sendError("unknown permission mode " + msg.Mode)
			return
		}
		h.setPermissionMode(ac, msg.Mode)
	case "set_reasoning_level":
		h.handleSetReasoningLevel(ac, msg.Level)
	case "new_thread":
		h.handleNewThread(ac)
	case "history_page":
		h.handleHistoryPage(ac, msg)
	case "search_history":
		h.handleSearchHistory(ac, msg)
	case "fork_thread":
		h.handleForkThread(ac, msg)
	case "worktree":
		h.handleWorktree(ac, msg)
	case "switch_thread":
		h.handleSwitchThread(ac, msg.Thread)
	case "peek_thread":
		h.handlePeekThread(ac, msg.Thread)
	case "recover_history":
		h.handleRecoverHistory(ac, msg.Thread)
	case "delete_thread":
		h.handleThreadAction(ac, msg)
	case "rename_thread", "pin_thread":
		h.handleThreadAction(ac, msg)
	case "participants":
		h.handleParticipants(ac, msg)
	case "set_cwd":
		h.handleSetCwd(ac, msg.Path)
	case "get_changes":
		h.handleGetChanges(ac, msg.ID)
	case "get_diff":
		h.handleGetDiff(ac, msg.Path, msg.ID)
	case "file_read":
		h.handleFileRead(ac, msg.Path, msg.ID)
	case "dir_list":
		h.handleDirList(ac, msg.Path, msg.ID)
	case "dir_mkdir":
		h.handleDirMkdir(ac, msg.Path, msg.ID)
	case "search_files":
		h.handleSearchFiles(ac, msg.Q, msg.ID)
	case "ping":
		ac.writeJSON(agentMsg{Op: "pong"})
	}
}

// handleApprove resolves one pending approval request (by apr_id, across
// all of the device's threads).
func (h *agentWSHandler) handleApprove(ac *agentConn, id string, allow, always bool) {
	if ac.entry == nil || id == "" {
		return
	}
	h.srv.agentSessionsMu.Lock()
	for _, th := range ac.entry.threads {
		if ch, ok := th.approvals[id]; ok {
			delete(th.approvals, id)
			ch <- approvalDecision{allow: allow, always: always}
			break
		}
	}
	h.srv.agentSessionsMu.Unlock()
}

// handleAskResponse resolves one pending ask-tool question (by ask id,
// across all of the device's threads); requestQuestion broadcasts the
// ask_resolved frame once it wakes.
func (h *agentWSHandler) handleAskResponse(ac *agentConn, id, answer string) {
	if ac.entry == nil || id == "" {
		return
	}
	h.srv.agentSessionsMu.Lock()
	for _, th := range ac.entry.threads {
		if ch, ok := th.questions[id]; ok {
			delete(th.questions, id)
			ch <- answer
			break
		}
	}
	h.srv.agentSessionsMu.Unlock()
}

// setPermissionMode switches the entry's approval mode and broadcasts the
// change to all attached browsers.
func (h *agentWSHandler) setPermissionMode(ac *agentConn, mode string) {
	h.srv.agentSessionsMu.Lock()
	ac.entry.permMode = mode
	h.srv.agentSessionsMu.Unlock()
	h.srv.writeAgentPrefs(ac.entry)
	h.srv.agentBroadcast(ac.entry, agentMsg{Op: "permission_mode", Mode: mode})
}

// handleSetReasoningLevel switches the entry's reasoning effort and rebuilds
// idle threads' sessions (history preserved) so the next turn picks it up;
// running threads rebuild when their next turn starts.
func (h *agentWSHandler) handleSetReasoningLevel(ac *agentConn, level string) {
	entry := ac.entry
	if entry == nil {
		return
	}
	if !validReasoningLevel(level) {
		ac.sendError("unknown reasoning level " + level)
		return
	}
	srv := h.srv
	srv.agentSessionsMu.Lock()
	srv.setReasoningLevelLocked(entry, level)
	srv.agentSessionsMu.Unlock()
	srv.writeAgentPrefs(entry)
	srv.agentBroadcast(entry, agentMsg{Op: "reasoning_level", Level: level})
}

// Caller holds agentSessionsMu so read-modify-write controls stay atomic.
func (srv *Server) setReasoningLevelLocked(entry *agentSessionEntry, level string) {
	entry.reasoningLevel = level
	for _, th := range entry.threads {
		if th.running {
			th.pendingReasoning = true
			continue
		}
		// Each thread keeps its own model across the rebuild.
		provider, model, err := srv.resolveAgentModel(th.model)
		if err != nil {
			continue
		}
		newSession := srv.buildAgentSession(entry, th, entry.client, provider, model)
		newSession.SetHistory(th.session.History())
		th.session = newSession
		th.sessionClient = entry.client
	}
}

func (h *agentWSHandler) handleChat(ac *agentConn, text, modelRef, threadRef string, imagesRaw json.RawMessage) {
	entry := ac.entry
	if entry == nil {
		ac.sendError("agent session not ready")
		return
	}
	images, err := parseAgentImages(imagesRaw)
	if err != nil {
		ac.sendError(err.Error())
		return
	}
	h.handleChatWithImages(ac, entry, text, modelRef, threadRef, images)
}

// Chat and idle steering share admission after decoding attachments once.
func (h *agentWSHandler) handleChatWithImages(ac *agentConn, entry *agentSessionEntry, text, modelRef, threadRef string, images []agent.MessageImage) {
	if request, ok := autonomySlash(text); ok {
		request.Thread = threadRef
		if len(images) > 0 {
			ac.writeJSON(agentMsg{Op: "autonomy", Thread: threadRef, Message: "autonomy commands do not accept images"})
			return
		}
		h.handleAutonomy(ac, request)
		return
	}
	srv := h.srv
	targetThread := threadRef
	if targetThread == "" {
		targetThread = ac.current
	}
	srv.agentStoreMu.RLock()
	store := srv.agentStoreInst
	srv.agentStoreMu.RUnlock()
	if store != nil {
		if fault := store.historyFault(entry.deviceID, targetThread); fault != "" {
			ac.sendErrorThread(targetThread, "history is incomplete; recover it before chatting: "+fault)
			return
		}
	}
	// One lock hold covers the queue check and the turn start: two chats
	// arriving together can never both start a turn (one queues).
	srv.agentSessionsMu.Lock()
	th := entry.threads[ac.current]
	threadID := ac.current
	if threadRef != "" && threadRef != ac.current {
		// Split-screen panes chat into threads they do not drive; the turn
		// frames are tagged with the thread id, so every pane stays in sync.
		th = entry.threads[threadRef]
		threadID = threadRef
	}
	if th == nil {
		srv.agentSessionsMu.Unlock()
		if threadRef != "" && threadRef != ac.current {
			ac.sendErrorThread(threadRef, "unknown thread "+threadRef)
		} else {
			ac.sendError("agent session not ready")
		}
		return
	}
	if entry.worktreeBusy {
		srv.agentSessionsMu.Unlock()
		ac.sendErrorThread(threadID, "managed worktree operation in progress")
		return
	}
	cancelAutonomyPendingLocked(th)
	th.goalActivity = ""
	if th.loop != nil && th.loop.Status == "active" && th.loop.Prompt == "" {
		th.loop.Prompt = text
		th.loop.Iteration++
	}
	if th.running || (len(th.queue) > 0 && (entry.deviceID == agentServerDeviceID || entry.client != nil)) {
		// Queue the message; it becomes a follow-up turn when the current
		// one finishes. Queued chats remember their requested model.
		th.queue = append(th.queue, agentQueuedChat{text: text, images: images, model: modelRef})
		entry.lastActive = time.Now()
		srv.agentSessionsMu.Unlock()
		srv.broadcastQueue(entry, th)
		srv.resumeAgentQueue(entry, th)
		return
	}
	// Pick up a device reconnect that happened while a turn was running.
	srv.rebindAgentSessionLocked(entry)
	// Optional model switch: resolve before touching state so an unknown
	// model aborts without running.
	modelChanged := false
	if modelRef != "" && modelRef != th.model {
		provider, model, canonical, err := srv.Agent.Resolve(modelRef)
		if err != nil {
			srv.agentSessionsMu.Unlock()
			ac.sendErrorThread(threadID, "unknown model "+modelRef)
			return
		}
		// Rebuild the session for the new provider/model, preserving history.
		newSession := srv.buildAgentSession(entry, th, entry.client, provider, model)
		newSession.SetHistory(th.session.History())
		th.session = newSession
		th.sessionClient = entry.client
		th.model = canonical
		entry.model = canonical // last-used default for new threads
		modelChanged = true
	}
	th.updatedAt = time.Now()
	startFrame := srv.startAgentTurnLocked(entry, th, text, images)
	turn := snapshotAgentTurnLocked(th)
	srv.agentSessionsMu.Unlock()

	if modelChanged {
		srv.writeAgentPrefs(entry)
	}
	srv.agentBroadcastTurn(entry, th, startFrame)
	srv.goAgent(func() { srv.runAgentTurn(entry, th, turn, text, images) })
}

// handleSteer prioritizes input at the next settled model/tool boundary.
// When idle it behaves like a normal chat; commands stay in the turn queue.
func (h *agentWSHandler) handleSteer(ac *agentConn, text, threadRef string, imagesRaw json.RawMessage) {
	if h.handleAutonomySlash(ac, text, threadRef, imagesRaw) {
		return
	}
	text = strings.TrimSpace(text)
	images, err := parseAgentImages(imagesRaw)
	if err != nil {
		ac.sendError(err.Error())
		return
	}
	if text == "" && len(images) == 0 {
		return
	}
	entry := ac.entry
	if entry == nil {
		ac.sendError("agent session not ready")
		return
	}
	srv := h.srv
	srv.agentSessionsMu.Lock()
	threadID := ac.current
	if threadRef != "" {
		threadID = threadRef
	}
	th := entry.threads[threadID]
	if th == nil || !th.running {
		srv.agentSessionsMu.Unlock()
		h.handleChatWithImages(ac, entry, text, "", threadRef, images)
		return
	}
	queueAgentSteeringLocked(th, agentQueuedChat{text: text, images: images})
	srv.agentSessionsMu.Unlock()
	srv.broadcastQueue(entry, th)
}

// handleDequeue retains the legacy index operation for older callers.
// Current browsers use queue identity plus a correlated acknowledgement.
func (h *agentWSHandler) handleDequeue(ac *agentConn, threadRef string, index int) {
	h.handleDequeueItem(ac, threadRef, index, "", "", false)
}

func (h *agentWSHandler) handleDequeueItem(ac *agentConn, threadRef string, index int, queueID, requestID string, restore bool) {
	entry := ac.entry
	if entry == nil {
		return
	}
	srv := h.srv
	srv.agentSessionsMu.Lock()
	threadID := ac.current
	if threadRef != "" {
		threadID = threadRef
	}
	th := entry.threads[threadID]
	var removed *agentQueuedChat
	if th != nil {
		if queueID != "" {
			index = -1
			for i := range th.queue {
				if th.queue[i].id == queueID {
					index = i
					break
				}
			}
		}
		if index >= 0 && index < len(th.queue) {
			item := takeQueuedChat(&th.queue, index)
			removed = &item
			entry.lastActive = time.Now()
		}
	}
	srv.agentSessionsMu.Unlock()
	if requestID != "" {
		reply := agentMsg{Op: "dequeued", Thread: threadID, ID: requestID, QueueID: queueID, Removed: removed != nil}
		if removed != nil {
			reply.Text = removed.text
			if restore && len(removed.images) > 0 {
				reply.Images, _ = json.Marshal(removed.images)
			}
		}
		ac.writeJSON(reply)
	}
	if th != nil {
		srv.broadcastQueue(entry, th)
		if removed != nil && removed.editToken != "" {
			srv.resumeAgentQueue(entry, th)
		}
	}
}

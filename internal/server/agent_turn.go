package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"rdev/internal/agent"

	"github.com/lxzan/gws"
)

// startAgentTurnLocked performs the running-state transition of a new turn
// and returns the turn_start frame to broadcast after unlocking. Caller
// must hold agentSessionsMu.
func (s *Server) startAgentTurnLocked(entry *agentSessionEntry, th *agentThread, text string, images []agent.MessageImage, display ...json.RawMessage) agentMsg {
	cancelAutonomyPendingLocked(th)
	th.turnAutonomy = ""
	if th.pendingReasoning {
		if provider, model, err := s.resolveAgentModel(th.model); err == nil {
			session := s.buildAgentSession(entry, th, entry.client, provider, model)
			session.SetHistory(th.session.History())
			th.session = session
			th.sessionClient = entry.client
		}
	}
	th.turnPage = s.recentTranscriptPageLocked(entry, th)
	th.running = true
	command, _ := splitAgentCommand(text)
	th.turnCommand = isAgentSessionCommand(command)
	th.lastOutcome = ""
	th.diagnostic = nil
	th.turnFiles = nil
	th.toolPaths = make(map[string]string)
	th.workingSince = time.Now()
	th.workingTool = ""
	if command, _ := splitAgentCommand(text); command == "/compact" {
		th.workingTool = "/compact"
	}
	th.updatedAt = th.workingSince
	th.live.Reset()
	th.turnHistory = th.session.History()
	th.turnHistIdx = len(th.turnHistory)
	entry.lastActive = th.workingSince
	th.turnMessageID = agent.NewMessageID()
	ctx, cancel := context.WithCancel(agent.WithUserMessageID(context.Background(), th.turnMessageID))
	th.turnCtx = ctx
	th.cancel = cancel

	metadata := map[string]any{}
	if len(display) > 0 && len(display[0]) > 0 {
		_ = json.Unmarshal(display[0], &metadata)
	}
	metadata["time"] = th.workingSince.UnixMilli()
	th.turnDisplay, _ = json.Marshal(metadata)
	start := agentMsg{MessageID: th.turnMessageID, Display: th.turnDisplay, Op: "turn_start", Text: text, ImagePreviews: agentImagePreviews(images)}
	if names := agentImageNames(images); len(names) > 0 {
		if raw, err := json.Marshal(names); err == nil {
			start.Images = raw
		}
	}
	return start
}

// startAgentTurn marks the thread running and spawns the turn goroutine.
func (s *Server) startAgentTurn(entry *agentSessionEntry, th *agentThread, text string, images []agent.MessageImage) {
	s.agentSessionsMu.Lock()
	if entry.worktreeBusy {
		th.queue = append(th.queue, agentQueuedChat{text: text, images: images})
		s.agentSessionsMu.Unlock()
		s.broadcastQueue(entry, th)
		return
	}
	startFrame := s.startAgentTurnLocked(entry, th, text, images)
	turn := snapshotAgentTurnLocked(th)
	s.agentSessionsMu.Unlock()

	s.agentBroadcastTurn(entry, th, startFrame)
	s.goAgent(func() { s.runAgentTurn(entry, th, turn, text, images) })
}

// runAgentTurn executes one turn to completion, broadcasts its lifecycle
// events, persists the history, then dequeues the next queued turn.
//
// The goroutine only streams frames and computes; every turn-state
// transition happens under agentSessionsMu: the terminal frame is appended
// to th.live and running flips in one lock hold, so no attach/snapshot can
// miss it (broadcast after unlock).
func (s *Server) runAgentTurn(entry *agentSessionEntry, th *agentThread, turn agentTurnSnapshot, text string, images []agent.MessageImage) {
	ctx, sess, cancel := turn.ctx, turn.session, turn.cancel
	startEpoch, turnModel := turn.epoch, turn.model
	cleanedAutomation := false
	cleanupAutomation := func() {
		if !cleanedAutomation {
			cleanedAutomation = true
			s.finishAutomationTurn(turn.automation)
		}
	}
	defer cleanupAutomation()
	// A reset/delete or panic must also settle the originating RPC exactly once.
	var rpcData any
	rpcErr := fmt.Errorf("conversation was canceled or replaced")
	defer func() {
		if turn.scheduled != nil && turn.scheduled.result.State == "" {
			turn.scheduled.finish(ctx, rpcErr)
		}
	}()
	defer func() {
		if turn.rpc != nil {
			if rpcErr != nil {
				turn.rpc.write(false, nil, "execution_error", rpcErr.Error())
			} else {
				turn.rpc.write(true, rpcData, "", "")
			}
		}
	}()
	// A panic anywhere in the turn must not wedge the thread: without this,
	// th.running would stay true forever, rejecting every future chat.
	defer func() {
		if r := recover(); r != nil {
			cleanupAutomation()
			rpcErr = fmt.Errorf("internal error: %v", r)
			log.Printf("PANIC in agent turn for %s/%s: %v", entry.deviceID, th.id, r)
			s.agentSessionsMu.Lock()
			current := th.epoch == startEpoch && entry.threads[th.id] == th && th.turnCtx == ctx
			if current {
				pauseAutonomyLocked(th)
				s.restoreScheduledSessionLocked(entry, th, turn)
				th.running = false
				th.turnHistory = nil
				th.cancel = nil
				th.turnCtx = nil
				th.workingTool = ""
				denyApprovalsLocked(th)
			}
			s.agentSessionsMu.Unlock()
			cancel()
			if current {
				s.agentBroadcast(entry, agentMsg{Op: "error", Thread: th.id, Message: fmt.Sprintf("internal error: %v", r)})
				s.persistAutonomy(entry, th)
				s.publishAutonomy(entry, th, "Agent error paused automatic continuation")
			}
		}
	}()

	var (
		doneUsage     *agentUsage
		doneReasoning string
	)
	emit := func(e agent.Event) {
		if turn.scheduled != nil {
			turn.scheduled.emit(e)
		}
		if s.emitAgentTurnEvent(entry, th, startEpoch, e) && e.Type == agent.EventDone {
			doneUsage = &agentUsage{InputTokens: e.Usage.InputTokens, OutputTokens: e.Usage.OutputTokens}
			doneReasoning = e.Reasoning
		}
	}

	// Record admission before execution, without holding the global session
	// lock during disk I/O. Reset/delete share the thread persistence lock.
	th.persistMu.Lock()
	s.agentSessionsMu.Lock()
	admitted := th.epoch == startEpoch && entry.threads[th.id] == th
	s.agentSessionsMu.Unlock()
	if admitted {
		if store := s.agentStore(); store != nil {
			store.saveOutcome(entry.deviceID, th.id, turnModel, "running", "", nil)
		}
	}
	th.persistMu.Unlock()
	if !admitted {
		cancel()
		return
	}

	takeSteering := func() *agent.Message {
		if turn.scheduled != nil {
			return nil
		}
		s.agentSessionsMu.Lock()
		if ctx.Err() != nil || th.epoch != startEpoch || entry.threads[th.id] != th || len(th.queue) == 0 || !th.queue[0].steering {
			s.agentSessionsMu.Unlock()
			return nil
		}
		next := th.queue[0]
		// Slash commands need their own dispatcher/turn lifecycle.
		if strings.HasPrefix(next.text, "/") {
			s.agentSessionsMu.Unlock()
			return nil
		}
		takeQueuedChat(&th.queue, 0)
		th.goalActivity = ""
		s.agentSessionsMu.Unlock()
		s.broadcastQueue(entry, th)
		display, _ := json.Marshal(map[string]any{"steering": true, "time": time.Now().UnixMilli()})
		return &agent.Message{Role: "user", Content: next.text, Images: next.images, Display: display}
	}
	revision := sess.HistoryRevision()
	explicitCompaction, runErr := runAgentConversation(ctx, sess, text, images, emit, takeSteering)
	historyReplaced := explicitCompaction || sess.HistoryRevision() != revision
	if command, _ := splitAgentCommand(text); !isAgentSessionCommand(command) {
		turn.historyIndex = sess.TurnHistoryIndex()
	}
	cleanupAutomation()
	activity := ""
	if turn.autonomy == "goal" {
		history := sess.History()
		if turn.historyIndex >= 0 && turn.historyIndex < len(history) {
			activity = goalTurnActivity(history[turn.historyIndex:])
		}
	}
	outcomeAt := time.Now().UTC().Format(time.RFC3339Nano)
	outcome := "done"
	if runErr != nil {
		if ctx.Err() == context.Canceled {
			outcome = "stopped"
		} else {
			outcome = "error"
		}
	}
	timing, _ := json.Marshal(map[string]int64{"time": turn.started, "endedAt": time.Now().UnixMilli()})
	if !explicitCompaction && turn.historyIndex >= 0 {
		sess.AnnotateTurn(turn.historyIndex, turn.display, timing)
	}

	// Serialize history with reset/delete for this thread, leaving other
	// conversations and streaming/control messages free during slow storage.
	th.persistMu.Lock()
	s.agentSessionsMu.Lock()
	valid := th.epoch == startEpoch && entry.threads[th.id] == th
	s.agentSessionsMu.Unlock()
	if valid {
		if store := s.agentStore(); store != nil {
			if err := store.captureTranscript(entry.deviceID, th.id, sess.History()); err != nil && runErr == nil {
				runErr = fmt.Errorf("history persistence: %w", err)
				outcome = "error"
			}
			if historyReplaced {
				store.replace(entry.deviceID, th.id, turnModel, sess.History())
			} else {
				store.save(entry.deviceID, th.id, turnModel, sess.History())
			}
			store.saveOutcome(entry.deviceID, th.id, turnModel, outcome, outcomeAt, agent.Diagnostic(runErr))
			if fault := store.historyFault(entry.deviceID, th.id); fault != "" && runErr == nil {
				runErr = fmt.Errorf("history persistence: %s", fault)
				outcome = "error"
			}
		}
	}
	// Remain running until persistence completes.
	s.agentSessionsMu.Lock()
	// Reset/delete may have admitted a replacement while this turn unwound.
	// Its state, approvals and queue belong to that replacement, so an old
	// completion must not clear or drain them (nor publish a terminal frame).
	if th.epoch != startEpoch || entry.threads[th.id] != th || th.turnCtx != ctx {
		s.agentSessionsMu.Unlock()
		th.persistMu.Unlock()
		cancel()
		return
	}
	finishAutonomyLocked(th, turn, outcome, activity)
	if turn.scheduled != nil {
		turn.scheduled.finish(ctx, runErr)
		s.restoreScheduledSessionLocked(entry, th, turn)
	}
	var terminal agentMsg
	switch outcome {
	case "done":
		terminal = agentMsg{
			Op:        "done",
			Thread:    th.id,
			Model:     turnModel,
			Usage:     doneUsage,
			Reasoning: doneReasoning,
			Files:     append([]string(nil), th.turnFiles...),
			Cwd:       agentEffectiveCwdLocked(entry, th),
			Branch:    th.branch,
		}
	case "stopped":
		terminal = agentMsg{Op: "stopped", Thread: th.id, Canceled: true}
	default:
		terminal = agentMsg{Op: "error", Thread: th.id, Diagnostic: agent.Diagnostic(runErr)}
		terminal.Code = terminal.Diagnostic.Code
		terminal.Message = terminal.Diagnostic.Summary()
		if terminal.Code == "agent_error" {
			// Preserve actionable local-command errors live; never persist their
			// arbitrary text in the diagnostic checkpoint.
			terminal.Message = runErr.Error()
		}
	}
	if outcome == "stopped" {
		terminal.Diagnostic = agent.Diagnostic(runErr)
	}
	terminal.Compacted = historyReplaced
	page, pageErr := s.completedTranscriptPageLocked(entry, th)
	terminal.History, terminal.HistoryHasMore, terminal.HistoryBefore = page.History, page.HasMore, page.Before
	if pageErr != nil {
		terminal.HistoryError = pageErr.Error()
	}
	if turn.rpc != nil {
		rpcErr = runErr
		if runErr == nil && !explicitCompaction {
			rpcData = map[string]any{"thread": th.id, "status": "completed"}
		}
		if explicitCompaction && runErr == nil {
			history := sess.History()
			// rdev has no persistent OMP entry IDs or pre-compaction token
			// estimate; do not fabricate those fields.
			rpcData = map[string]any{"summary": strings.TrimPrefix(history[0].Content, "Context from earlier conversation (summary):\n\n")}
		}
	}
	terminal.Display = timing
	var terminalData []byte
	th.lastOutcome = terminal.Op
	th.outcomeAt = outcomeAt
	th.diagnostic = terminal.Diagnostic
	if data, err := json.Marshal(terminal); err == nil {
		terminalData = data
		th.live.Append(terminal, data)
	}
	th.running = false
	th.turnHistory = nil
	th.cancel = nil
	th.turnCtx = nil
	th.workingTool = ""
	th.updatedAt = time.Now()
	denyApprovalsLocked(th)

	turnFiles := th.turnFiles
	var next agentQueuedChat
	var nextFrame agentMsg
	var nextTurn agentTurnSnapshot
	hasNext := queueHeadReady(entry, th, time.Now())
	prefsDirty := false
	if hasNext {
		next = takeQueuedChat(&th.queue, 0)
		prefsDirty = s.applyQueuedModelLocked(entry, th, next)
		nextFrame = s.startAgentTurnLocked(entry, th, next.text, next.images, next.display)
		nextTurn = snapshotAgentTurnLocked(th)
	}
	s.agentSessionsMu.Unlock()
	th.persistMu.Unlock()
	cancel()

	s.persistAutonomy(entry, th)
	s.publishAutonomy(entry, th, "")
	if terminalData != nil {
		s.agentBroadcastData(entry, terminalData)
	}
	if prefsDirty {
		s.writeAgentPrefs(entry)
	}
	s.broadcastThreadList(entry)
	if len(turnFiles) > 0 {
		s.scheduleThreadMetaProbe(entry, th.id)
	}
	if hasNext {
		s.broadcastQueue(entry, th)
		s.agentBroadcastTurn(entry, th, nextFrame)
		s.goAgent(func() { s.runAgentTurn(entry, th, nextTurn, next.text, next.images) })
	}
	if !hasNext {
		s.scheduleAutonomy(entry, th)
	}
}

// HandleAgentWS handles browser agent WebSocket connections on /agent?device=<id>.
func (s *Server) HandleAgentWS(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuth(w, r) {
		return
	}
	if !requireOrigin(w, r) {
		return
	}
	deviceID := r.URL.Query().Get("device")
	if deviceID == "" {
		http.Error(w, "missing device parameter", http.StatusBadRequest)
		return
	}

	upgrader := gws.NewUpgrader(&agentWSHandler{srv: s, adminBypass: s.deviceAuthBypassed(r)}, &gws.ServerOption{
		// Chat frames may carry up to agentMaxImages base64 images of
		// agentMaxImageBytes each (~22MB), plus envelope slack.
		ReadMaxPayloadSize: 24 * 1024 * 1024,
		ParallelGolimit:    1,
		Authorize: func(r *http.Request, session gws.SessionStorage) bool {
			session.Store("deviceID", deviceID)
			return true
		},
		PermessageDeflate: gws.PermessageDeflate{Enabled: false},
	})

	socket, err := upgrader.Upgrade(w, r)
	if err != nil {
		log.Printf("agent ws upgrade error: %v", err)
		return
	}
	socket.ReadLoop()
}

const agentDesktopGuidance = `

Desktop capability and execution:
- Tool definitions and fresh results are authoritative for capabilities; old assistant claims and summaries may be wrong. Before declaring desktop/CUA unsupported, check computer status on the intended online device. Distinguish unsupported capability from offline transport, missing permission, and another conversation's control lease. A failed call does not prove the entire driver is unavailable.
- When cua=true, read skill cua-driver/SKILL.md and WORKFLOW.md. Discover the compact catalog with cua action=tools, then request action=tools with tool=<exact name> for the needed schema before calling it. Prefer semantic/accessibility actions. RDev owns the embedded session: do not invent create_session, supply session IDs, or install a CLI/cloud service to use it.
- Screenshots are optional for native CUA calls and computer input. Use screenshots when visual context or pixel coordinates are needed, and verify meaningful actions with fresh observations. Follow each native tool's coordinate space; computer screenshot pixels are not automatically native screen points. Never blindly retry uncertain input or switch backends to bypass permission/takeover.
- An agent cursor overlay is not the system pointer. Follow the skill's enable/place/verify workflow; a successful call or enabled=true is not visual proof. Turn completion and desktop cleanup end the session and its overlay; do not promise a persistent cursor.
- The browser tool uses a separate server-hosted browser. Use computer/CUA for the device's installed browser, existing tabs or logged-in desktop session. Do not silently substitute one environment for the other.
- Complete authorized, bounded actions without asking the user to reconfirm them. Ask only for unresolved material choices or additional consequential actions. Report observed results, uncertainty and exact blockers; never claim a fix, visibility or deployment without evidence.`

func buildAgentSystemPrompt(deviceID, cwd string) string {
	if deviceID == agentServerDeviceID {
		return "You are the server agent in rdev. The server is your coordination environment only. No server-host shell or filesystem access is available. Call list_devices to discover currently connected remote devices and their tool schemas, then device_tool with an explicit device ID. Use agent_sessions to list or read Agent conversations (including offline history), and manage_agent_session to create, rename or delete a specified conversation; discover exact device and thread IDs before managing them. No device registration is needed. Check the live device list rather than inventing device IDs. Use subagent for isolated delegated tasks, jobs to inspect or cancel them, and verify their results. Work autonomously within these capabilities and answer in the user's language." + agentDesktopGuidance
	}
	prompt := `You are an agent inside rdev, a remote-device management server. Device shell and file tools operate on the device identified below, not on the server or the user's machine. Other tools run where their descriptions specify; never assume a shared filesystem, browser session or authentication context.

Tools:
- shell: run a shell command. Prefer it for discovery (uname, ls, cat small files, system info).
- bg: start a device shell command in the background; completion is automatically delivered.
- jobs: list/read/wait/cancel background work. Waiting does not cancel a job.
- subagent: delegate a self-contained task with an explicit subset of tools; children report back automatically. Verify their claimed results.
- list_dir: list a directory.
- read_file / write_file: read/write file contents. read_file pages long files with offset/limit; with anchors=true every line gets an N#hash anchor.
- edit: modify an existing file. Prefer line_edits mode (hashline anchors from read_file anchors=true — cheaper and verified) over old_string for targeted changes; stale anchors are rejected, just re-read and retry.
- grep / glob: search file contents / find files by name on the device.
- web_search / web_fetch: search the web and read pages (run on the server, not the device).
- ask: when the user's request is ambiguous or a decision is needed mid-task, ask a structured question with options instead of guessing.
- todo_write: plan multi-step tasks first and keep the checklist updated as you make progress.

Guidelines:
- Work autonomously: gather what you need with tools before answering.
- Before destructive commands (rm -rf, dd, mkfs, dropping data, killing services), briefly state the risk, then still execute what the user asked.
- Quote paths carefully; the device may be any OS/arch.
- Answer in the user's language, concisely. Report what you did and the outcome.`
	prompt += agentDesktopGuidance
	prompt += fmt.Sprintf("\n\nDevice context: shell and file tools operate on device %q.", deviceID)
	if cwd != "" {
		prompt += fmt.Sprintf("\n\nCurrent project directory: %s (relative paths in tool calls are resolved against it).", cwd)
	}
	return prompt
}

// agentEffectiveCwdLocked returns a thread's working directory: its own cwd
// when set, else the device default. Caller must hold agentSessionsMu.
func agentEffectiveCwdLocked(entry *agentSessionEntry, th *agentThread) string {
	if th.cwd != "" {
		return th.cwd
	}
	return entry.defaultCwd
}

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"rdev/internal/agent"

	"github.com/lxzan/gws"
)

// agentThread is one conversation thread of a device: its own LLM session,
// history, todo list, message queue and (at most one) in-flight turn.
// Guarded by Server.agentSessionsMu.
type agentThread struct {
	scheduled        *scheduledAgentTurn
	goal             *agentGoal
	loop             *agentLoop
	goalActivity     string
	autonomyRevision uint64
	autonomyPending  bool
	autonomyCancel   context.CancelFunc
	turnAutonomy     string
	deviceScope      *agentDeviceScope

	automation       *agentAutomation
	pinnedAt         int64
	pendingReasoning bool // rebuild at the next turn boundary after an effort change
	turnDisplay      json.RawMessage
	turnCommand      bool // compact/tool commands do not consume model-boundary steering
	// Acquire before agentSessionsMu; disk I/O never holds the global lock.
	persistMu     sync.Mutex
	jobs          *agentJobs
	id            string
	session       *agent.Session
	sessionClient *ClientConn // device connection session's tools are bound to
	model         string      // this thread's canonical "provider/model"
	running       bool        // a chat turn is in flight
	cancel        context.CancelFunc
	turnCtx       context.Context
	steeringWake  chan struct{} // guarded by agentSessionsMu; wakes only passive job waits
	// Freeze the pre-turn transcript: semantic compaction may replace session
	// history while live frames still describe the original running turn.
	turnHistory   []agent.Message
	turnMessageID string
	turnPage      agentHistoryPage
	archive       []agent.Message // only used when durable persistence is disabled
	turnHistIdx   int
	// epoch is bumped by reset; a turn that started before the bump skips
	// its save and terminal broadcast (its history belongs to the pre-reset
	// conversation).
	epoch        int
	queue        []agentQueuedChat // chat messages received while running
	todos        []todoItem        // latest todo_write list
	todoPhases   []rpcTodoPhase    // OMP phase-shaped view, when set through RPC
	alwaysAllow  map[string]bool   // tool names approved "always" in this thread
	aprSeq       int               // approval/question id counter
	approvals    map[string]chan approvalDecision
	questions    map[string]chan string // pending ask tool questions (id -> answer)
	toolPaths    map[string]string      // in-flight tool call id -> path arg (edit/write_file)
	turnFiles    []string               // files changed by edit/write_file this turn
	workingSince time.Time              // current turn start
	outcomeAt    string
	lastOutcome  string // latest foreground turn: done, error, stopped
	diagnostic   *agent.FailureDiagnostic
	workingTool  string // currently executing tool name
	updatedAt    time.Time
	// lastUsage is the token accounting of the most recent completed turn,
	// replayed in auth_ok/thread_switched for the context-usage indicator.
	lastUsage    *agentUsage
	cwd          string // working directory for tools ("" = device default)
	branch       string // last probed git branch of cwd ("" = unknown/not a repo)
	metaProbeSeq uint64 // rejects stale Git probe results after cwd changes
	title        string // explicit thread title ("" = derive from first user message)
	// live holds the marshaled frames of the in-flight turn (turn_start …
	// terminal frame) so a browser switching in mid-turn can replay the
	// exact UI state; cleared at the next turn start, read only while
	// running (idle attach replays full history instead).
	live agentReplayBuffer
}

// Permission modes: how much of the agent's tool use needs browser
// confirmation.
const (
	agentPermFull  = "full"  // auto-approve everything, never ask
	agentPermWrite = "write" // default: only dangerous tools ask
	agentPermAsk   = "ask"   // every tool call asks, including read-only
)

// agentPermNeedsApproval reports whether a tool call must be confirmed by
// the browser under the given permission mode (before always-allow memory).
func agentPermNeedsApproval(mode string, dangerous bool) bool {
	switch mode {
	case agentPermFull:
		return false
	case agentPermAsk:
		return true
	default: // write
		return dangerous
	}
}

// validPermMode reports whether mode is a known permission mode.
func validPermMode(mode string) bool {
	return mode == agentPermFull || mode == agentPermWrite || mode == agentPermAsk
}

// validReasoningLevel reports whether level is a known reasoning level.
func validReasoningLevel(level string) bool {
	switch level {
	case "off", "low", "medium", "high":
		return true
	}
	return false
}

// agentSessionEntry is the server-owned per-device agent state: threads (LLM
// sessions) and in-flight turns live here, decoupled from browser WS
// lifetimes. Browsers attach as subscribers; events are broadcast to all.
// Guarded by Server.agentSessionsMu.
type agentSessionEntry struct {
	prefsMu         sync.Mutex // serializes disk updates for this device, outside agentSessionsMu
	deviceID        string
	client          *ClientConn // latest known live device connection
	os              string      // device OS (client registration or desktop platform)
	model           string      // canonical "provider/model"
	permMode        string      // agentPerm*; default agentPermWrite
	reasoningLevel  string      // off|low|medium|high; default "medium"
	subs            map[*gws.Conn]bool
	lastActive      time.Time
	threads         map[string]*agentThread
	worktreeBusy    bool          // excludes Agent execution while a managed checkout changes
	current         string        // current thread id
	defaultCwd      string        // device default working directory
	defaultCwdProbe chan struct{} // coalesces discovery across concurrent panel requests
}

// buildAgentSession creates the LLM session for one provider/model pair,
// bound to the device tools and the thread's approval flow.
func (s *Server) buildAgentSession(entry *agentSessionEntry, th *agentThread, client *ClientConn, provider *AgentProvider, model *AgentModelConfig) *agent.Session {
	if th.jobs == nil {
		jobs := newAgentJobs(nil)
		jobs.persistenceEnabled = s.DataDir != "" && s.AgentSessionTTL > 0
		if store := s.agentStore(); store != nil {
			jobs.childTranscriptDir = store.childTranscriptsPath(entry.deviceID, th.id)
		}
		updates := &agentJobUpdates{launch: s.goAgent, publish: func() {
			if s.broadcastAgentJobsChanged(entry, th, jobs) {
				s.broadcastThreadList(entry)
			}
		}}
		checkpoints := &agentJobUpdates{launch: s.goAgent, publish: func() { s.persistAgentJobs(entry, th, jobs) }}
		jobs.onChange = func() { checkpoints.Notify(); updates.Notify() }
		jobs.onComplete = func(job agentJobSnapshot) { s.deliverAgentJob(entry, th, jobs, job) }
		th.jobs = jobs
	}
	if entry.deviceID != agentServerDeviceID && th.deviceScope == nil {
		th.deviceScope = newAgentDeviceScope(s, entry.deviceID, th.cwd)
	}
	tools := s.agentBaseTools(entry, client, th.cwd)
	if th.deviceScope != nil {
		tools = append(tools, &connectedDevicesTool{srv: s, scope: th.deviceScope}, &remoteDeviceTool{srv: s, scope: th.deviceScope})
	}
	if entry.deviceID == agentServerDeviceID {
		tools = append(tools, &agentSessionsTool{srv: s, owner: entry, thread: th.id}, &agentSessionsTool{srv: s, owner: entry, thread: th.id, mutate: true})
	}
	tools = append(tools, s.automationTools(entry, th)...)
	if entry.deviceID != agentServerDeviceID && client != nil {
		tools = append(tools, &backgroundTool{jobs: th.jobs, scope: th.deviceScope, shell: &shellTool{srv: s, client: client, Cwd: th.cwd}})
	} else if entry.deviceID == agentServerDeviceID {
		tools = append(tools, &backgroundTool{jobs: th.jobs, resolveShell: func(device string) (*shellTool, error) {
			tool, err := s.resolveDeviceTool(device, "shell")
			if err != nil {
				return nil, err
			}
			return tool.(*shellTool), nil
		}})
	}
	tools = append(tools, &jobsTool{jobs: th.jobs, steering: func() <-chan struct{} { return s.agentSteeringSignal(th) }}, &canvasTool{})
	tools = append(tools, &askTool{ask: func(_ context.Context, question string, options []askOption) string {
		return s.requestQuestion(entry, th, question, marshalAskOptions(options))
	}})
	tools = append(tools, s.scheduleAgentTools(entry, th)...)
	cfg := agent.Config{
		API:             provider.modelAPI(model),
		ProviderID:      provider.ID,
		MaxOutputTokens: model.MaxTokens,
		Thinking:        model.Thinking,
		BaseURL:         provider.BaseURL,
		APIKey:          provider.APIKey,
		Model:           model.ID,
		SystemPrompt:    buildAgentSystemPrompt(entry.deviceID, agentEffectiveCwdLocked(entry, th)),
		Tools:           tools,
		MaxTurns:        s.AgentMaxTurns,
		ReasoningEffort: entry.reasoningLevel,
		Approve: func(tc agent.ToolCall, args json.RawMessage) (bool, error) {
			return s.requestApproval(entry, th, tc, args)
		},
		// Read at call time so set_permission_mode applies to existing
		// sessions without a rebuild.
		ApproveAll: func() bool {
			s.agentSessionsMu.Lock()
			defer s.agentSessionsMu.Unlock()
			return agentPermNeedsApproval(entry.permMode, false)
		},
	}
	if th.deviceScope != nil {
		cfg.SystemPrompt += "\nThis conversation supports joined devices. Use list_devices to see authorized participants and device_tool with an explicit device ID for cross-device work. Native tools default to the original device. Each participant has its own working directory. Never assume a path or operating system from another device. Subagents may target an explicitly joined device using device. Only the user can add participants."
	}
	cwd := th.cwd
	cfg.Tools = append(cfg.Tools, &subagentTool{
		jobs: th.jobs, config: cfg,
		tools: func() []agent.Tool {
			if client == nil && entry.deviceID != agentServerDeviceID {
				return nil
			}
			return s.agentBaseTools(entry, client, cwd)
		},
		deviceContext: func(ctx context.Context, device string) (context.Context, func(), error) {
			if th.deviceScope == nil {
				return nil, nil, fmt.Errorf("device targeting unavailable")
			}
			return th.deviceScope.begin(ctx, device)
		},
		deviceTools: func(device string) ([]agent.Tool, error) {
			if th.deviceScope == nil {
				return nil, fmt.Errorf("use server device_tool for remote routing")
			}
			return th.deviceScope.tools(device)
		},
	})
	if entry.deviceID != agentServerDeviceID && client == nil {
		availableTools := cfg.Tools
		cfg.Tools = nil
		cfg.SystemPrompt = "The original device is offline. Historical observations may be outdated."
		if th.deviceScope != nil && len(th.deviceScope.snapshot()) > 1 {
			for _, tool := range availableTools {
				switch tool.Name() {
				case "list_devices", "device_tool", "computer", "cua", "skill", "browser", "autojs", "jobs", "subagent", "todo_write", "web_fetch", "web_search":
					cfg.Tools = append(cfg.Tools, tool)
				}
			}
			cfg.Tools = append(cfg.Tools, &backgroundTool{jobs: th.jobs, scope: th.deviceScope})
			cfg.SystemPrompt += " Use list_devices and explicitly target an authorized online participant with device_tool. Never execute on the offline original device."
		} else {
			cfg.SystemPrompt += " Discuss existing context only; no tools are available."
		}
	}
	if len(cfg.Tools) != 0 {
		availableTools := append([]agent.Tool(nil), cfg.Tools...)
		cfg.Tools = append(cfg.Tools, &agentJSTool{tools: availableTools})
	}
	if th.goal != nil && th.goal.Status == "active" {
		goal, epoch := th.goal, th.epoch
		cfg.Tools = append(cfg.Tools, &goalTool{srv: s, entry: entry, thread: th, goal: goal, epoch: epoch})
		cfg.SystemPrompt += "\nGoal mode is explicitly enabled. Objective: " + goal.Objective + ". Continue making concrete progress; use goal complete only after achieving it. Do not claim completion while blocked."
		cfg.OnUsage = func(usage agent.Usage) { s.accountGoalUsage(entry, th, goal, epoch, usage) }
	}
	if th.scheduled != nil {
		s.configureScheduledAgent(entry, th, &cfg)
	}
	epoch := th.epoch
	cfg.OnHistory = func(messages []agent.Message) error {
		th.persistMu.Lock()
		defer th.persistMu.Unlock()
		s.agentSessionsMu.Lock()
		valid := th.epoch == epoch && entry.threads[th.id] == th
		var archiveErr error
		if valid && s.agentStore() == nil {
			next := mergeAgentTranscript(th.archive, messages)
			var bytes int
			for _, m := range next {
				bytes += len(marshalAgentHistoryMsg(m))
			}
			if len(next) > agentTranscriptMaxEntries || bytes > agentTranscriptMaxBytes {
				archiveErr = errors.New("in-memory transcript quota reached; start a new thread")
			} else {
				th.archive = next
			}
		}
		s.agentSessionsMu.Unlock()
		if !valid {
			return context.Canceled
		}
		if archiveErr != nil {
			return archiveErr
		}
		if store := s.agentStore(); store != nil {
			return store.captureTranscript(entry.deviceID, th.id, messages)
		}
		return nil
	}
	th.pendingReasoning = false
	return agent.New(cfg)
}

// newAgentThreadLocked creates a thread (new session for entry's model) with
// the given history, working directory and explicit title. Caller must hold
// agentSessionsMu.
func (s *Server) newAgentThreadLocked(entry *agentSessionEntry, threadID string, history []agent.Message, cwd, title string) *agentThread {
	provider, model, canonical, err := s.Agent.Resolve(entry.model)
	if err != nil {
		provider, model, canonical, _ = s.Agent.Resolve("")
	}
	th := &agentThread{
		id:            threadID,
		model:         canonical,
		sessionClient: entry.client,
		alwaysAllow:   make(map[string]bool),
		approvals:     make(map[string]chan approvalDecision),
		questions:     make(map[string]chan string),
		toolPaths:     make(map[string]string),
		updatedAt:     time.Now(),
		cwd:           cwd,
		title:         title,
	}
	th.session = s.buildAgentSession(entry, th, entry.client, provider, model)
	if len(history) > 0 {
		th.session.SetHistory(history)
	}
	return th
}

// agentSessionFor returns the per-device agent entry, creating it on first
// use: persisted threads are restored from JSONL history (migrating the
// legacy single-file layout to t_1) and the most recently updated thread
// becomes current (cold start; after that the in-memory entry is the source
// of truth).
func (s *Server) agentSessionFor(deviceID string, client *ClientConn) (*agentSessionEntry, error) {
	s.agentEvictOnce.Do(func() { go s.agentEvictionLoop() })
	s.agentSessionsMu.Lock()
	defer s.agentSessionsMu.Unlock()
	if s.agentSessions == nil {
		s.agentSessions = make(map[string]*agentSessionEntry)
	}
	if entry, ok := s.agentSessions[deviceID]; ok {
		return entry, nil
	}
	_, _, canonical, err := s.Agent.Resolve("")
	if err != nil {
		return nil, err
	}
	entry := &agentSessionEntry{
		deviceID:       deviceID,
		client:         client,
		model:          canonical,
		permMode:       agentPermWrite,
		reasoningLevel: "medium",
		subs:           make(map[*gws.Conn]bool),
		lastActive:     time.Now(),
		threads:        make(map[string]*agentThread),
	}
	prefs := s.loadAgentPrefs(deviceID)
	if validPermMode(prefs.PermissionMode) {
		entry.permMode = prefs.PermissionMode
	}
	if validReasoningLevel(prefs.ReasoningLevel) {
		entry.reasoningLevel = prefs.ReasoningLevel
	}
	entry.os = prefs.OS
	if client != nil {
		if os := clientOS(client); os != "" {
			entry.os = os
		}
	}
	recentThread, recentModel := "", ""
	if store := s.agentStore(); store != nil {
		store.migrate(deviceID)
		for _, meta := range store.listThreads(deviceID) {
			msgs, savedModel, cwd, title := store.load(deviceID, meta.ID)
			th := s.newAgentThreadLocked(entry, meta.ID, msgs, cwd, title)
			th.pinnedAt = prefs.Pinned[meta.ID]
			th.goal, th.loop = store.loadAutonomy(deviceID, meta.ID)
			if th.deviceScope != nil {
				for _, member := range prefs.Participants[meta.ID] {
					if member.Device != "" && member.Device != agentServerDeviceID && member.Device != deviceID {
						th.deviceScope.members[member.Device] = member.Cwd
					}
				}
			}
			th.updatedAt = meta.UpdatedAt
			th.lastOutcome = restoredAgentOutcome(meta.Outcome)
			th.outcomeAt = meta.OutcomeAt
			th.diagnostic = meta.Diagnostic
			// Per-thread model: a thread restored with a different model
			// keeps it instead of being retargeted to the entry default.
			if savedModel != "" && savedModel != th.model {
				if provider, model, canonical, rerr := s.Agent.Resolve(savedModel); rerr == nil && canonical != th.model {
					newSession := s.buildAgentSession(entry, th, entry.client, provider, model)
					newSession.SetHistory(th.session.History())
					th.session = newSession
					th.model = canonical
				}
			}
			entry.threads[meta.ID] = th
			if th.jobs.restore(store.loadJobs(deviceID, meta.ID)) {
				// Rewrite interrupted states once after initialization, outside
				// the session lock, without re-delivering completion messages.
				owner := th.jobs
				s.goAgent(func() { s.persistAgentJobs(entry, th, owner) })
			}
			// The most recently updated thread is listed first.
			if recentThread == "" {
				recentThread, recentModel = meta.ID, savedModel
			}
		}
	}
	// Model priority: persisted prefs (when resolvable) > most recent
	// thread's meta model > server default.
	for _, cand := range []string{prefs.Model, recentModel} {
		if cand == "" {
			continue
		}
		if _, _, c, rerr := s.Agent.Resolve(cand); rerr == nil {
			entry.model = c
			break
		}
	}
	// Current thread: the persisted one when it still exists, else the most
	// recently updated thread.
	if prefs.CurrentThread != "" {
		if _, ok := entry.threads[prefs.CurrentThread]; ok {
			entry.current = prefs.CurrentThread
		}
	}
	if entry.current == "" {
		entry.current = recentThread
	}
	if entry.current == "" {
		th := s.newAgentThreadLocked(entry, newAgentThreadID(), nil, "", "")
		entry.threads[th.id] = th
		entry.current = th.id
	}
	s.agentSessions[deviceID] = entry
	// Probe the device's default working directory once (off-lock); the
	// result backfills thread_meta for the current thread.
	if client != nil && deviceID != agentServerDeviceID {
		s.goAgent(func() { s.probeAgentDefaultCwd(entry) })
	}
	return entry, nil
}

// newAgentThreadID mints a thread id from the current time.
func newAgentThreadID() string {
	return fmt.Sprintf("t_%d", time.Now().UnixNano())
}

// rebindAgentSessionLocked rebuilds idle threads' sessions against the
// latest live device connection (after a device reconnect), keeping model
// and history. Running threads are skipped — rebinding mid-run would strand
// the running turn's history tail; they rebind when the next turn starts.
func (s *Server) rebindAgentSessionLocked(entry *agentSessionEntry) {
	for _, th := range entry.threads {
		if th.sessionClient == entry.client || th.running {
			continue
		}
		provider, model, err := s.resolveAgentModel(th.model)
		if err != nil {
			return
		}
		newSession := s.buildAgentSession(entry, th, entry.client, provider, model)
		newSession.SetHistory(th.session.History())
		th.session = newSession
		th.sessionClient = entry.client
	}
}

// resolveAgentModel resolves a "provider/model" reference, falling back to
// the configured default when the reference is empty or unknown.
func (s *Server) resolveAgentModel(modelRef string) (*AgentProvider, *AgentModelConfig, error) {
	provider, model, _, err := s.Agent.Resolve(modelRef)
	if err != nil && modelRef != "" {
		provider, model, _, err = s.Agent.Resolve("")
	}
	return provider, model, err
}

// agentBroadcast sends msg to every socket attached to entry, pruning
// subscribers whose write fails.
func (s *Server) agentBroadcast(entry *agentSessionEntry, msg agentMsg) {
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("agent marshal error: %v", err)
		return
	}
	s.agentBroadcastData(entry, data)
}

// agentBroadcastTurn broadcasts one frame of a thread's in-flight turn. The
// frame is tagged with the thread id (browsers viewing another thread drop
// it) and appended to th.live so a browser switching in mid-turn replays the
// exact UI state. Lock must NOT be held.
func (s *Server) agentBroadcastTurn(entry *agentSessionEntry, th *agentThread, msg agentMsg) {
	msg.Thread = th.id
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("agent marshal error: %v", err)
		return
	}
	s.agentSessionsMu.Lock()
	th.live.Append(msg, data)
	s.agentSessionsMu.Unlock()
	s.agentBroadcastData(entry, data)
	if msg.Op == "turn_start" {
		// Cross-device snapshots include disk history; never delay turn admission.
		s.goAgent(func() { s.broadcastThreadList(entry) })
	}
}

// agentBroadcastData broadcasts a pre-marshaled frame; used by the
// changes/diff replies which are not agentMsg-shaped.
func (s *Server) agentBroadcastData(entry *agentSessionEntry, data []byte) {
	s.agentSessionsMu.Lock()
	subs := make([]*gws.Conn, 0, len(entry.subs))
	for c := range entry.subs {
		subs = append(subs, c)
	}
	s.agentSessionsMu.Unlock()

	var dead []*gws.Conn
	for _, c := range subs {
		acRaw, _ := c.Session().Load("agentConn")
		if acRaw == nil {
			dead = append(dead, c)
			continue
		}
		if !acRaw.(*agentConn).writeData(data) {
			dead = append(dead, c)
		}
	}
	if len(dead) > 0 {
		s.agentSessionsMu.Lock()
		for _, c := range dead {
			delete(entry.subs, c)
		}
		s.agentSessionsMu.Unlock()
	}
}

// agentEvictionLoop hourly drops idle entries (no running turn, no attached
// browser); the next connect cold-starts again from the JSONL history.
func (s *Server) agentEvictionLoop() {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		s.agentSessionsMu.Lock()
		for id, entry := range s.agentSessions {
			busy := entry.worktreeBusy
			for _, th := range entry.threads {
				if th.running || th.autonomyPending || th.turnCtx != nil || len(th.queue) > 0 || (th.jobs != nil && (th.jobs.Running() || th.jobs.pendingPersistence())) {
					busy = true
					break
				}
			}
			// Recently active entries stay: a browser may reconnect and
			// expect the warm session (30 min grace).
			if busy || len(entry.subs) > 0 || time.Since(entry.lastActive) < 30*time.Minute {
				continue
			}
			for _, th := range entry.threads {
				if th.jobs != nil {
					th.jobs.Close()
					th.jobs = nil
				}
			}
			delete(s.agentSessions, id)
		}
		s.agentSessionsMu.Unlock()
	}
}

// agentThreadListLocked snapshots entry's threads, most recently updated
// first. Caller must hold agentSessionsMu.
func agentThreadListLocked(entry *agentSessionEntry) []agentThreadInfo {
	list := make([]agentThreadInfo, 0, len(entry.threads))
	for _, th := range entry.threads {
		title := th.title
		if title == "" {
			title = threadTitle(th.session.History())
		}
		info := agentThreadInfo{
			ID:         th.id,
			PinnedAt:   th.pinnedAt,
			Title:      title,
			UpdatedAt:  th.updatedAt.UTC().Format(time.RFC3339),
			Running:    th.running,
			Outcome:    th.lastOutcome,
			OutcomeAt:  th.outcomeAt,
			Diagnostic: th.diagnostic,
			Model:      th.model,
		}
		if th.jobs != nil {
			info.BackgroundJobs = th.jobs.RunningCount()
		}
		if th.running {
			info.Tool = th.workingTool
			if len(th.approvals) > 0 {
				info.Waiting = "approval"
			} else if len(th.questions) > 0 {
				info.Waiting = "answer"
			}
		}
		list = append(list, info)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].UpdatedAt > list[j].UpdatedAt })
	return list
}

// agentDeviceThreads is one device's slice of the cross-device thread list.
type agentDeviceThreads struct {
	Device  string            `json:"device"`
	OS      string            `json:"os,omitempty"`
	Online  bool              `json:"online"`
	Threads []agentThreadInfo `json:"threads"`
}

// collectAllThreads builds the cross-device thread list: live entries come
// from memory (running flags included); devices that only exist on disk are
// listed from the session store with running=false. Directory names are the
// device ids (sanitizeAgentHistoryName is identity for realistic ids).
func (s *Server) collectAllThreads() []agentDeviceThreads {
	s.agentSessionsMu.Lock()
	live := make(map[string]agentDeviceThreads, len(s.agentSessions))
	for id, entry := range s.agentSessions {
		live[sanitizeAgentHistoryName(id)] = agentDeviceThreads{
			Device:  id,
			OS:      entry.os,
			Online:  entry.client != nil,
			Threads: agentThreadListLocked(entry),
		}
	}
	s.agentSessionsMu.Unlock()

	store := s.agentStore()
	if store == nil {
		out := make([]agentDeviceThreads, 0, len(live))
		for _, d := range live {
			out = append(out, d)
		}
		return out
	}
	out := make([]agentDeviceThreads, 0, len(live)+2)
	seen := make(map[string]bool, len(live))
	for _, d := range live {
		out = append(out, d)
		seen[sanitizeAgentHistoryName(d.Device)] = true
	}
	for _, dirID := range store.listDevices() {
		if seen[dirID] {
			continue
		}
		prefs := s.loadAgentPrefs(dirID)
		os := prefs.OS
		metas := store.listThreads(dirID)
		threads := make([]agentThreadInfo, 0, len(metas))
		for _, m := range metas {
			threads = append(threads, agentThreadInfo{
				ID:         m.ID,
				PinnedAt:   prefs.Pinned[m.ID],
				Outcome:    restoredAgentOutcome(m.Outcome),
				OutcomeAt:  m.OutcomeAt,
				Diagnostic: m.Diagnostic,
				Title:      m.Title,
				UpdatedAt:  m.UpdatedAt.UTC().Format(time.RFC3339),
			})
		}
		out = append(out, agentDeviceThreads{Device: dirID, OS: os, Threads: threads})
	}
	return out
}

// broadcastAllThreads pushes the cross-device list to every attached
// browser of every device. Lock must NOT be held.
func (s *Server) broadcastAllThreads() {
	devices := s.collectAllThreads()
	s.agentSessionsMu.Lock()
	entries := make([]*agentSessionEntry, 0, len(s.agentSessions))
	for _, entry := range s.agentSessions {
		if len(entry.subs) > 0 {
			entries = append(entries, entry)
		}
	}
	s.agentSessionsMu.Unlock()
	for _, entry := range entries {
		s.agentBroadcast(entry, agentMsg{Op: "all_threads", Devices: devices})
	}
}

// broadcastThreadList sends the thread_list snapshot to all attached
// browsers. Lock must NOT be held (broadcast takes it internally).
func (s *Server) broadcastThreadList(entry *agentSessionEntry) {
	s.agentSessionsMu.Lock()
	list := agentThreadListLocked(entry)
	s.agentSessionsMu.Unlock()
	s.agentBroadcast(entry, agentMsg{Op: "thread_list", Threads: list})
	// Any thread-list change (title, running flag, new/deleted thread) also
	// changes the cross-device sidebar, so refresh it everywhere.
	s.broadcastAllThreads()
}

// broadcastQueue sends the queue snapshot for one thread.
func (s *Server) broadcastQueue(entry *agentSessionEntry, th *agentThread) {
	s.agentSessionsMu.Lock()
	if entry.threads[th.id] != th {
		s.agentSessionsMu.Unlock()
		return
	}
	items := queueTexts(th.queue)
	ids := queueIDs(th.queue)
	modes := queueModes(th)
	s.agentSessionsMu.Unlock()
	raw, err := json.Marshal(items)
	if err != nil {
		return
	}
	s.agentBroadcast(entry, agentMsg{Op: "queue", Thread: th.id, Items: raw, QueueIDs: ids, QueueModes: modes})
}

// requestQuestion implements the ask tool's question flow: broadcasts
// ask_request to the turn's viewers and waits for the browser's answer
// (dismissed on timeout, turn cancel, or turn end).
func (s *Server) requestQuestion(entry *agentSessionEntry, th *agentThread, question string, options json.RawMessage) string {
	s.agentSessionsMu.Lock()
	th.aprSeq++
	id := fmt.Sprintf("ask_%s_%d", th.id, th.aprSeq)
	ch := make(chan string, 1)
	th.questions[id] = ch
	turnCtx := th.turnCtx
	s.agentSessionsMu.Unlock()

	s.agentBroadcastTurn(entry, th, agentMsg{Op: "ask_request", ID: id, Text: question, Options: options})
	s.broadcastThreadList(entry)

	if turnCtx == nil {
		turnCtx = context.Background()
	}
	timer := time.NewTimer(agentApprovalTimeout)
	defer timer.Stop()
	resolve := func() {
		s.agentSessionsMu.Lock()
		delete(th.questions, id)
		s.agentSessionsMu.Unlock()
		s.broadcastThreadList(entry)
	}
	var answer string
	select {
	case answer = <-ch:
	case <-timer.C:
	case <-turnCtx.Done():
	}
	resolve()
	// Close the card on every attached browser, whatever the outcome.
	// Turn-scoped so mid-turn replays (auth_ok live frames) include it.
	s.agentBroadcastTurn(entry, th, agentMsg{Op: "ask_resolved", ID: id, Answer: answer})
	return answer
}

// requestApproval implements agent.Config.Approve: short-circuits on full
// permission mode / always-allow, otherwise broadcasts approval_request and
// waits for the browser's decision (deny on timeout or turn cancellation).
// A disconnected browser may reattach and answer the replayed request.
func (s *Server) requestApproval(entry *agentSessionEntry, th *agentThread, tc agent.ToolCall, args json.RawMessage, contexts ...context.Context) (bool, error) {
	s.agentSessionsMu.Lock()
	if entry.permMode == agentPermFull || th.alwaysAllow[tc.Name] {
		s.agentSessionsMu.Unlock()
		return true, nil
	}
	th.aprSeq++
	id := fmt.Sprintf("apr_%s_%d", th.id, th.aprSeq)
	ch := make(chan approvalDecision, 1)
	th.approvals[id] = ch
	turnCtx := th.turnCtx
	if len(contexts) > 0 {
		turnCtx = contexts[0]
	}
	s.agentSessionsMu.Unlock()

	s.agentBroadcastTurn(entry, th, agentMsg{
		Op:      "approval_request",
		ID:      id,
		Name:    tc.Name,
		Args:    string(args),
		Summary: tc.Summary,
	})
	s.broadcastThreadList(entry)

	if turnCtx == nil {
		turnCtx = context.Background()
	}
	timer := time.NewTimer(agentApprovalTimeout)
	defer timer.Stop()
	resolve := func() {
		s.agentSessionsMu.Lock()
		delete(th.approvals, id)
		s.agentSessionsMu.Unlock()
		s.broadcastThreadList(entry)
	}
	select {
	case d := <-ch:
		resolve()
		if d.allow && d.always {
			s.agentSessionsMu.Lock()
			th.alwaysAllow[tc.Name] = true
			s.agentSessionsMu.Unlock()
		}
		return d.allow, nil
	case <-timer.C:
		resolve()
		return false, nil
	case <-turnCtx.Done():
		resolve()
		return false, nil
	}
}

// agentThreadSnapshot is the replay state of one thread for auth_ok,
// thread_switched, thread_peeked and delete-thread remaps.
type agentThreadSnapshot struct {
	goal         *agentGoal
	loop         *agentLoop
	participants []agentParticipant

	automation     *automationView
	history        []agentHistoryEntry
	historyHasMore bool
	historyBefore  string
	historyError   string
	diagnostic     *agent.FailureDiagnostic
	todos          []todoItem
	queue          []string
	queueIDs       []string
	queueModes     []string
	running        bool
	working        *agentWorking
	usage          *agentUsage
	live           []json.RawMessage
}

// threadSnapshotLocked snapshots a thread for an attacher. History and live
// are disjoint: a running turn replays its frozen pre-turn history plus live
// frames, even if context recovery replaces the session history. Idle threads
// use the complete current history. Caller must hold agentSessionsMu.
func (s *Server) threadSnapshotLocked(entry *agentSessionEntry, th *agentThread) agentThreadSnapshot {
	snap := agentThreadSnapshot{
		goal: cloneGoal(th.goal), loop: cloneLoop(th.loop),
		todos:      th.todos,
		queue:      queueTexts(th.queue),
		queueIDs:   queueIDs(th.queue),
		queueModes: queueModes(th),
		running:    th.running,
		usage:      th.lastUsage,
		diagnostic: th.diagnostic,
	}
	if th.deviceScope != nil {
		snap.participants = th.deviceScope.snapshot()
	}
	if th.automation != nil {
		th.automation.mu.Lock()
		view := th.automation.view
		snap.automation = &view
		th.automation.mu.Unlock()
	}
	if snap.todos == nil {
		snap.todos = []todoItem{}
	}
	if snap.queue == nil {
		snap.queue = []string{}
	}
	if th.running {
		snap.working = &agentWorking{Since: th.workingSince.UTC().Format(time.RFC3339Nano), Tool: th.workingTool}
		snap.live = th.live.Snapshot()
	}
	if th.autonomyPending {
		snap.live = th.live.Snapshot()
	}
	page := s.recentTranscriptPageLocked(entry, th)
	snap.history, snap.historyHasMore, snap.historyBefore = page.History, page.HasMore, page.Before
	s.agentStoreMu.RLock()
	store := s.agentStoreInst
	s.agentStoreMu.RUnlock()
	if store != nil {
		if fault := store.historyFault(entry.deviceID, th.id); fault != "" {
			snap.historyError = fault
		}
	}
	if snap.history == nil {
		snap.history = []agentHistoryEntry{}
	}
	return snap
}

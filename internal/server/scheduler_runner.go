package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"rdev/internal/agent"
)

// A scheduled observer belongs to one immutable turn snapshot, not to the
// thread's latest outcome (which may already describe a queued user turn).
// Only the turn goroutine accesses usage, result and denial after admission.
type scheduledAgentTurn struct {
	scheduleID     string
	spec           ScheduleSpec
	report         func(ScheduledUpdate)
	cancel         context.CancelFunc
	usage          agent.Usage
	result         ScheduledResult
	budgetExceeded bool
	denial         string
}

func (turn *scheduledAgentTurn) emit(event agent.Event) {
	switch event.Type {
	case agent.EventTextDelta, agent.EventReasoning:
		turn.report(ScheduledUpdate{Output: event.Text})
	case agent.EventToolEnd:
		turn.report(ScheduledUpdate{Output: "\n[" + event.ToolName + "]\n" + event.ToolResult + "\n"})
	}
}

func (turn *scheduledAgentTurn) finish(ctx context.Context, err error) {
	turn.result = ScheduledResult{State: "succeeded", Usage: turn.usage}
	switch {
	case turn.budgetExceeded:
		turn.result.State, turn.result.Reason = "failed", "token budget exhausted"
	case ctx.Err() == context.DeadlineExceeded:
		turn.result.State, turn.result.Reason = "failed", "execution timeout"
	case ctx.Err() != nil:
		turn.result.State, turn.result.Reason = "canceled", ctx.Err().Error()
	case err != nil:
		turn.result.State, turn.result.Reason = "failed", err.Error()
	case turn.denial != "":
		turn.result.State, turn.result.Reason = "failed", turn.denial
	}
}

// Deliberately exclude delegation, interactive tools, cross-device routers,
// session management and scheduler mutations even when explicitly requested.
func scheduledToolAllowed(name string) bool {
	switch name {
	case "shell", "list_dir", "read_file", "write_file", "edit", "grep", "glob", "todo_write", "web_fetch", "web_search", "canvas", "list_devices", "schedules", "schedule_runs":
		return true
	default:
		return false
	}
}

func (s *Server) configureScheduledAgent(entry *agentSessionEntry, th *agentThread, cfg *agent.Config) {
	turn := th.scheduled
	allowed := make(map[string]bool, len(turn.spec.AllowedTools))
	for _, name := range turn.spec.AllowedTools {
		allowed[name] = true
	}
	tools := cfg.Tools[:0]
	dangerous := make(map[string]bool)
	for _, tool := range cfg.Tools {
		if !allowed[tool.Name()] || !scheduledToolAllowed(tool.Name()) {
			continue
		}
		tools = append(tools, tool)
		if d, ok := tool.(agent.DangerousTool); ok {
			dangerous[tool.Name()] = d.Dangerous()
		}
	}
	cfg.Tools = tools
	cfg.SystemPrompt += "\nThis is an unattended scheduled turn. Execute only the requested task using the explicitly granted tools. Do not ask questions, create schedules, delegate, or start background work. Report denied capabilities honestly."
	cfg.ReasoningEffort = turn.spec.Reasoning
	cfg.ApproveAll = func() bool { return true }
	cfg.Approve = func(call agent.ToolCall, _ json.RawMessage) (bool, error) {
		ok := turn.spec.Authorized && allowed[call.Name] && scheduledToolAllowed(call.Name)
		if ok && turn.spec.SessionMode == "existing" {
			s.agentSessionsMu.Lock()
			ok = !agentPermNeedsApproval(entry.permMode, dangerous[call.Name]) || th.alwaysAllow[call.Name]
			s.agentSessionsMu.Unlock()
		}
		if !ok {
			turn.denial = "scheduled tool denied by unattended permissions: " + call.Name
		}
		return ok, nil
	}
	cfg.OnUsage = func(usage agent.Usage) {
		turn.usage.InputTokens += usage.InputTokens
		turn.usage.OutputTokens += usage.OutputTokens
		turn.usage.CacheReadTokens += usage.CacheReadTokens
		turn.report(ScheduledUpdate{Usage: &turn.usage})
		if turn.spec.TokenBudget > 0 && turn.usage.InputTokens+turn.usage.OutputTokens >= turn.spec.TokenBudget {
			turn.budgetExceeded = true
			turn.cancel()
		}
	}
}

// Called before queued users are admitted. Rebuild the normal capability set
// while retaining the scheduled turn's real history and any live preference edits.
func (s *Server) restoreScheduledSessionLocked(entry *agentSessionEntry, th *agentThread, turn agentTurnSnapshot) {
	if turn.scheduled == nil || th.scheduled != turn.scheduled {
		return
	}
	th.scheduled = nil
	provider, model, err := s.resolveAgentModel(th.model)
	if err != nil {
		return
	}
	session := s.buildAgentSession(entry, th, entry.client, provider, model)
	session.SetHistory(turn.session.History())
	th.session, th.sessionClient = session, entry.client
}

func (s *Server) invalidateScheduledThread(device, thread, reason string) {
	s.schedulerMu.Lock()
	manager := s.scheduler
	s.schedulerMu.Unlock()
	if manager != nil {
		manager.InvalidateThread(device, thread, reason)
	}
}

func (s *Server) validateScheduledTarget(spec ScheduleSpec) error {
	if !spec.Authorized {
		return fmt.Errorf("explicit authorization is required for unattended execution")
	}
	if strings.TrimSpace(spec.Device) == "" {
		return fmt.Errorf("device is required")
	}
	if spec.Kind == "shell" {
		if spec.Device == agentServerDeviceID {
			return fmt.Errorf("shell schedules require a remote device; server host execution is unavailable")
		}
		if strings.TrimSpace(spec.Command) == "" {
			return fmt.Errorf("command is required")
		}
		// Device IDs can be offline at creation and are resolved at dispatch.
		return nil
	}
	if spec.Kind != "agent" {
		return fmt.Errorf("kind must be shell or agent")
	}
	if s.Agent == nil {
		return fmt.Errorf("agent is not configured")
	}
	if strings.TrimSpace(spec.Prompt) == "" {
		return fmt.Errorf("prompt is required")
	}
	if command, _ := splitAgentCommand(spec.Prompt); isAgentSessionCommand(command) || command == "/goal" || command == "/loop" {
		return fmt.Errorf("scheduled prompts cannot invoke interactive session commands")
	}
	if spec.Reasoning != "" && !validReasoningLevel(spec.Reasoning) {
		return fmt.Errorf("invalid reasoning level")
	}
	for _, name := range spec.AllowedTools {
		if !scheduledToolAllowed(name) {
			return fmt.Errorf("tool %q is not available for unattended execution", name)
		}
	}
	if spec.SessionMode == "new" {
		_, _, _, err := s.Agent.Resolve(spec.Model)
		return err
	}
	if spec.SessionMode != "existing" || spec.Thread == "" {
		return fmt.Errorf("agent session mode must be new or existing with a thread")
	}
	s.agentSessionsMu.Lock()
	entry := s.agentSessions[spec.Device]
	if entry != nil {
		th := entry.threads[spec.Thread]
		if th == nil {
			s.agentSessionsMu.Unlock()
			return fmt.Errorf("target thread does not exist")
		}
		model := th.model
		s.agentSessionsMu.Unlock()
		_, _, _, err := s.Agent.Resolve(model)
		return err
	}
	s.agentSessionsMu.Unlock()
	if store := s.agentStore(); store != nil {
		for _, meta := range store.listThreads(spec.Device) {
			if meta.ID == spec.Thread {
				_, model, _, _ := store.load(spec.Device, spec.Thread)
				_, _, _, err := s.Agent.Resolve(model)
				return err
			}
		}
	}
	return fmt.Errorf("target thread does not exist")
}

func scheduledThreadBusy(th *agentThread) bool {
	return th.running || len(th.queue) > 0 || th.autonomyPending ||
		(th.goal != nil && th.goal.Status == "active") ||
		(th.loop != nil && th.loop.Status == "active") ||
		(th.jobs != nil && th.jobs.Running())
}

func (s *Server) scheduledTaskReady(spec ScheduleSpec) (bool, string) {
	if spec.Device != agentServerDeviceID {
		if _, ok := s.GetClient(spec.Device); !ok {
			return false, "device_offline"
		}
	}
	if spec.Kind == "agent" && spec.SessionMode == "existing" {
		s.agentSessionsMu.Lock()
		defer s.agentSessionsMu.Unlock()
		if entry := s.agentSessions[spec.Device]; entry != nil {
			th := entry.threads[spec.Thread]
			if th == nil {
				return false, "target thread does not exist"
			}
			if scheduledThreadBusy(th) {
				return false, "thread_busy"
			}
		}
	}
	return true, ""
}

func (s *Server) runScheduledTask(ctx context.Context, run ScheduleRun, report func(ScheduledUpdate)) ScheduledResult {
	if report == nil {
		report = func(ScheduledUpdate) {}
	}
	spec := run.Snapshot
	if err := s.validateScheduledTarget(spec); err != nil {
		return ScheduledResult{State: "failed", Reason: err.Error()}
	}
	if spec.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(spec.TimeoutSeconds)*time.Second)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return ScheduledResult{State: "canceled", Reason: err.Error()}
	}
	if ready, reason := s.scheduledTaskReady(spec); !ready {
		return ScheduledResult{State: "skipped", Reason: reason}
	}
	client, _ := s.GetClient(spec.Device)
	if spec.Kind == "shell" {
		if client == nil {
			return ScheduledResult{State: "skipped", Reason: "device offline"}
		}
		tool := &shellTool{srv: s, client: client, Cwd: spec.Cwd, Env: []string{"RDEV_RUN_ID=" + run.ID, "RDEV_SCHEDULE_ID=" + run.ScheduleID}}
		result, err := tool.runShellReporting(ctx, spec.Command, func(text string) { report(ScheduledUpdate{Output: text}) })
		if err != nil {
			state := "failed"
			if ctx.Err() != nil {
				state = "unknown"
			}
			return ScheduledResult{State: state, Reason: err.Error()}
		}
		if result.ExitCode < 0 {
			return ScheduledResult{State: "unknown", Reason: "device did not report an exit code"}
		}
		state, reason := "succeeded", ""
		if result.ExitCode != 0 {
			state, reason = "failed", fmt.Sprintf("exit code %d", result.ExitCode)
		}
		return ScheduledResult{State: state, Reason: reason, ExitCode: &result.ExitCode}
	}
	entry, err := s.agentSessionFor(spec.Device, client)
	if err != nil {
		return ScheduledResult{State: "failed", Reason: err.Error()}
	}
	s.agentSessionsMu.Lock()
	th := entry.threads[spec.Thread]
	if spec.SessionMode == "existing" {
		if th == nil || scheduledThreadBusy(th) {
			s.agentSessionsMu.Unlock()
			return ScheduledResult{State: "skipped", Reason: "target thread missing or busy (user work has priority)"}
		}
		spec.Model, spec.Cwd, spec.Reasoning = th.model, agentEffectiveCwdLocked(entry, th), entry.reasoningLevel
	} else {
		th = s.newAgentThreadLocked(entry, newAgentThreadID(), nil, spec.Cwd, spec.Name+" · "+time.Now().Format(time.RFC3339))
		if spec.Reasoning == "" {
			spec.Reasoning = entry.reasoningLevel
		}
	}
	provider, model, canonical, err := s.Agent.Resolve(spec.Model)
	if err != nil {
		s.agentSessionsMu.Unlock()
		return ScheduledResult{State: "failed", Reason: err.Error()}
	}
	turnCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	observer := &scheduledAgentTurn{scheduleID: run.ScheduleID, spec: spec, report: report, cancel: cancel}
	th.scheduled = observer
	session := s.buildAgentSession(entry, th, client, provider, model)
	session.SetHistory(th.session.History())
	th.session, th.sessionClient, th.model = session, client, canonical
	entry.threads[th.id] = th
	display, _ := json.Marshal(map[string]string{"scheduleId": run.ScheduleID, "scheduleRunId": run.ID, "turnId": run.ID})
	start := s.startAgentTurnLocked(entry, th, spec.Prompt, nil, display)
	th.cancel()
	th.turnCtx, th.cancel = turnCtx, cancel
	turn := snapshotAgentTurnLocked(th)
	spec.Model, spec.Thread = canonical, th.id
	title := th.title
	s.agentSessionsMu.Unlock()
	report(ScheduledUpdate{Thread: th.id, Turn: run.ID, Actual: &spec})
	if spec.SessionMode == "new" {
		if store := s.agentStore(); store != nil {
			th.persistMu.Lock()
			s.agentSessionsMu.Lock()
			current := entry.threads[th.id] == th && th.epoch == turn.epoch
			s.agentSessionsMu.Unlock()
			if current {
				store.replace(entry.deviceID, th.id, canonical, nil)
				store.setCwd(entry.deviceID, th.id, spec.Cwd, nil)
				store.setTitle(entry.deviceID, th.id, title, nil)
			}
			th.persistMu.Unlock()
		}
	}
	s.agentBroadcastTurn(entry, th, start)
	s.broadcastThreadList(entry)
	s.runAgentTurn(entry, th, turn, spec.Prompt, nil)
	return observer.result
}

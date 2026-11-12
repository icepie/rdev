package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"rdev/internal/agent"
)

// Goal and loop state is thread-owned. Revisions invalidate delayed admissions;
// epoch separately invalidates turns and persistence after reset/deletion.
type agentGoal struct {
	Objective   string `json:"objective"`
	Status      string `json:"status"`
	TokenBudget *int64 `json:"tokenBudget,omitempty"`
	TokensUsed  int64  `json:"tokensUsed"`
}

type agentLoop struct {
	Prompt        string `json:"prompt"`
	Status        string `json:"status"`
	Iteration     int64  `json:"iteration"`
	MaxIterations int64  `json:"maxIterations,omitempty"`
	DurationMs    int64  `json:"durationMs,omitempty"`
	Condition     string `json:"condition,omitempty"`
	Until         bool   `json:"until,omitempty"`
	deadline      time.Time
	remaining     time.Duration
}

func cloneGoal(g *agentGoal) *agentGoal {
	if g == nil {
		return nil
	}
	v := *g
	if g.TokenBudget != nil {
		budget := *g.TokenBudget
		v.TokenBudget = &budget
	}
	return &v
}
func cloneLoop(l *agentLoop) *agentLoop {
	if l == nil {
		return nil
	}
	v := *l
	return &v
}
func autonomyFrameLocked(th *agentThread, message string) agentMsg {
	return agentMsg{Op: "autonomy", Thread: th.id, Goal: cloneGoal(th.goal), Loop: cloneLoop(th.loop), Message: message}
}
func cancelAutonomyPendingLocked(th *agentThread) {
	th.autonomyRevision++
	if th.autonomyCancel != nil {
		th.autonomyCancel()
		th.autonomyCancel = nil
	}
	th.autonomyPending = false
}
func pauseAutonomyLocked(th *agentThread) {
	cancelAutonomyPendingLocked(th)
	if th.goal != nil && th.goal.Status == "active" {
		th.goal.Status = "paused"
		th.pendingReasoning = true
	}
	if th.loop != nil && th.loop.Status == "active" {
		th.loop.Status = "paused"
		if th.loop.DurationMs > 0 {
			th.loop.remaining = time.Until(th.loop.deadline)
			if th.loop.remaining < 0 {
				th.loop.remaining = 0
			}
		}
	}
}
func autonomyWantedLocked(th *agentThread) bool {
	return th.loop != nil && th.loop.Status == "active" && th.loop.Prompt != "" || th.goal != nil && th.goal.Status == "active"
}

func (s *Server) publishAutonomy(entry *agentSessionEntry, th *agentThread, message string) {
	s.agentSessionsMu.Lock()
	if entry.threads[th.id] != th {
		s.agentSessionsMu.Unlock()
		return
	}
	frame := autonomyFrameLocked(th, message)
	s.agentSessionsMu.Unlock()
	s.agentBroadcast(entry, frame)
}

func (h *agentWSHandler) handleAutonomy(ac *agentConn, request agentMsg) {
	threadID := request.Thread
	if threadID == "" {
		threadID = ac.current
	}
	s := h.srv
	if ac.entry == nil {
		if request.RPC {
			h.writeRPCError(ac, request, "invalid_request", "agent session not ready")
		} else {
			ac.writeJSON(agentMsg{Op: "autonomy", Thread: threadID, Message: "agent session not ready"})
		}
		return
	}
	s.agentSessionsMu.Lock()
	th := ac.entry.threads[threadID]
	if th == nil {
		s.agentSessionsMu.Unlock()
		if request.RPC {
			h.writeRPCError(ac, request, "invalid_request", "unknown thread")
		} else {
			ac.writeJSON(agentMsg{Op: "autonomy", Thread: threadID, Message: "unknown thread"})
		}
		return
	}
	err := mutateAutonomyLocked(th, request)
	frame := autonomyFrameLocked(th, "")
	if err != nil {
		frame.Message = err.Error()
	}
	s.agentSessionsMu.Unlock()
	if request.RPC {
		if err != nil {
			h.writeRPCError(ac, request, "invalid_request", err.Error())
		} else {
			data, _ := json.Marshal(map[string]any{"goal": frame.Goal, "loop": frame.Loop})
			ok := true
			ac.writeJSON(agentMsg{Type: "response", Command: request.Command, ID: request.ID, Success: &ok, Data: data})
		}
	}
	if err != nil {
		ac.writeJSON(frame)
		return
	}
	if request.Action == "get" || request.Action == "status" {
		ac.writeJSON(frame)
		return
	}
	s.agentBroadcast(ac.entry, frame)
	s.persistAutonomy(ac.entry, th)
	s.scheduleAutonomy(ac.entry, th)
}

func mutateAutonomyLocked(th *agentThread, request agentMsg) error {
	action := request.Action
	if action == "get" || action == "status" {
		return nil
	}
	switch request.Op {
	case "goal":
		switch action {
		case "create":
			objective := strings.TrimSpace(request.Objective)
			if objective == "" {
				return fmt.Errorf("goal objective is required")
			}
			if request.TokenBudget != nil && *request.TokenBudget <= 0 {
				return fmt.Errorf("tokenBudget must be a positive integer")
			}
			if th.goal != nil && th.goal.Status != "complete" {
				return fmt.Errorf("drop the existing goal before creating another")
			}
			th.goal = cloneGoal(&agentGoal{Objective: objective, Status: "active", TokenBudget: request.TokenBudget})
			if th.loop != nil && th.loop.Status == "active" {
				pauseAutonomyLocked(th)
				th.goal.Status = "active"
			}
		case "pause":
			if th.goal != nil && th.goal.Status == "active" {
				th.goal.Status = "paused"
			}
		case "resume":
			if th.goal == nil || th.goal.Status == "complete" {
				return fmt.Errorf("no paused goal")
			}
			if th.goal.TokenBudget != nil && th.goal.TokensUsed >= *th.goal.TokenBudget {
				return fmt.Errorf("goal token budget exhausted; create a new goal to authorize more work")
			}
			if th.loop != nil && th.loop.Status == "active" {
				return fmt.Errorf("pause the loop before resuming the goal")
			}
			th.goal.Status = "active"
		case "drop":
			th.goal = nil
		default:
			return fmt.Errorf("unknown goal action %q", action)
		}
		th.goalActivity = ""
		th.pendingReasoning = true
	case "loop":
		switch action {
		case "start":
			loop, err := parseAgentLoop(request.Text)
			if err != nil {
				return err
			}
			pauseAutonomyLocked(th)
			th.loop = loop
		case "pause":
			if th.loop != nil && th.loop.Status == "active" {
				th.loop.Status = "paused"
				th.loop.remaining = time.Until(th.loop.deadline)
			}
		case "resume":
			if th.loop == nil || th.loop.Status != "paused" {
				return fmt.Errorf("no paused loop")
			}
			if th.goal != nil && th.goal.Status == "active" {
				return fmt.Errorf("pause the goal before resuming the loop")
			}
			th.loop.Status = "active"
			if th.loop.DurationMs > 0 {
				th.loop.deadline = time.Now().Add(th.loop.remaining)
			}
		case "off":
			th.loop = nil
		default:
			return fmt.Errorf("unknown loop action %q", action)
		}
	default:
		return fmt.Errorf("unknown autonomy operation")
	}
	cancelAutonomyPendingLocked(th)
	return nil
}

func autonomySlash(text string) (agentMsg, bool) {
	command, argument := splitAgentCommand(text)
	if command != "/goal" && command != "/loop" {
		return agentMsg{}, false
	}
	request := agentMsg{Op: strings.TrimPrefix(command, "/")}
	first, rest, _ := loopWord(argument)
	if command == "/goal" {
		request.Action = first
		if first == "set" {
			request.Action = "create"
			request.Objective = rest
		}
		if first == "" || first == "status" {
			request.Action = "get"
		}
	} else {
		switch first {
		case "pause", "resume", "off", "status":
			request.Action = first
		default:
			request.Action = "start"
			request.Text = argument
		}
	}
	return request, true
}
func (h *agentWSHandler) handleAutonomySlash(ac *agentConn, text, thread string, images json.RawMessage) bool {
	request, ok := autonomySlash(text)
	if !ok {
		return false
	}
	if thread == "" {
		thread = ac.current
	}
	request.Thread = thread
	parsed, err := parseAgentImages(images)
	if err != nil || len(parsed) > 0 {
		message := "autonomy commands do not accept images"
		if err != nil {
			message = err.Error()
		}
		frame := agentMsg{Op: "autonomy", Thread: thread, Message: message}
		h.srv.agentSessionsMu.Lock()
		if ac.entry != nil {
			if th := ac.entry.threads[thread]; th != nil {
				frame = autonomyFrameLocked(th, message)
			}
		}
		h.srv.agentSessionsMu.Unlock()
		ac.writeJSON(frame)
		return true
	}
	h.handleAutonomy(ac, request)
	return true
}

// Admission happens only after the previous turn's history and terminal frame
// settle. Queued user work (including held edits) always takes precedence.
func (s *Server) scheduleAutonomy(entry *agentSessionEntry, th *agentThread) {
	s.agentSessionsMu.Lock()
	if entry.threads[th.id] != th || th.running || len(th.queue) != 0 || th.autonomyPending || !autonomyWantedLocked(th) {
		s.agentSessionsMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	th.autonomyPending, th.autonomyCancel = true, cancel
	revision, epoch := th.autonomyRevision, th.epoch
	loop := cloneLoop(th.loop)
	th.live.Reset()
	s.agentSessionsMu.Unlock()
	s.goAgent(func() {
		defer cancel()
		timer := time.NewTimer(20 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		message := ""
		pass := true
		if loop != nil && loop.Status == "active" {
			if loop.MaxIterations > 0 && loop.Iteration >= loop.MaxIterations || loop.DurationMs > 0 && !time.Now().Before(loop.deadline) {
				pass = false
			} else if loop.Condition != "" {
				var err error
				pass, err = s.evaluateLoopCondition(ctx, entry, th, loop)
				if err != nil {
					message = err.Error()
				}
			}
		}
		s.agentSessionsMu.Lock()
		if ctx.Err() != nil || entry.threads[th.id] != th || th.epoch != epoch || th.autonomyRevision != revision {
			s.agentSessionsMu.Unlock()
			return
		}
		th.autonomyPending, th.autonomyCancel = false, nil
		if th.running || len(th.queue) != 0 {
			s.agentSessionsMu.Unlock()
			return
		}
		if !pass {
			if th.loop != nil {
				th.loop.Status = "complete"
				if message != "" {
					th.loop.Status = "paused"
				}
			}
			s.agentSessionsMu.Unlock()
			s.persistAutonomy(entry, th)
			s.publishAutonomy(entry, th, message)
			return
		}
		if !autonomyWantedLocked(th) {
			s.agentSessionsMu.Unlock()
			return
		}
		text, kind := "", "goal"
		if th.loop != nil && th.loop.Status == "active" {
			if th.loop.DurationMs > 0 && !time.Now().Before(th.loop.deadline) {
				th.loop.Status = "complete"
				s.agentSessionsMu.Unlock()
				s.persistAutonomy(entry, th)
				s.publishAutonomy(entry, th, "")
				return
			}
			text, kind = th.loop.Prompt, "loop"
			th.loop.Iteration++
		} else {
			text = "Continue working toward the goal: " + th.goal.Objective + ". Use goal complete only when the objective is achieved. If blocked, explain the blocker rather than repeating the same answer."
		}
		frame := s.startAgentTurnLocked(entry, th, text, nil)
		th.turnAutonomy = kind
		turn := snapshotAgentTurnLocked(th)
		s.agentSessionsMu.Unlock()
		s.persistAutonomy(entry, th)
		s.publishAutonomy(entry, th, "")
		s.agentBroadcastTurn(entry, th, frame)
		s.runAgentTurn(entry, th, turn, text, nil)
	})
}

func (s *Server) evaluateLoopCondition(ctx context.Context, entry *agentSessionEntry, th *agentThread, loop *agentLoop) (bool, error) {
	// Conditions are never local server shell escapes: they use the original,
	// authenticated device and the same approval policy as model shell calls.
	if entry.deviceID == agentServerDeviceID {
		return false, fmt.Errorf("loop shell conditions require a device thread")
	}
	client, ok := s.GetClient(entry.deviceID)
	if !ok {
		return false, fmt.Errorf("loop condition device is offline")
	}
	s.agentSessionsMu.Lock()
	cwd := th.cwd
	s.agentSessionsMu.Unlock()
	args, _ := json.Marshal(map[string]string{"command": loop.Condition})
	allowed, err := s.requestApproval(entry, th, agent.ToolCall{Name: "shell", Summary: "Loop condition: " + loop.Condition}, args, ctx)
	if err != nil {
		return false, err
	}
	if !allowed {
		return false, fmt.Errorf("loop condition approval denied")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result, err := (&shellTool{srv: s, client: client, Cwd: cwd}).runShellReporting(ctx, loop.Condition, nil)
	if err != nil {
		return false, err
	}
	return loopConditionContinues(result.ExitCode, loop.Until)
}

func goalTurnActivity(messages []agent.Message) string {
	var b strings.Builder
	var bookkeeping map[string]bool
	for _, m := range messages {
		if m.Role == "assistant" {
			b.WriteString(m.Content)
			for _, tc := range m.ToolCalls {
				if tc.Name == "goal" {
					if bookkeeping == nil {
						bookkeeping = make(map[string]bool)
					}
					bookkeeping[tc.ID] = true
				} else {
					b.WriteString(tc.Name)
					b.WriteString(tc.Arguments)
				}
			}
		}
		if m.Role == "tool" && !bookkeeping[m.ToolCallID] {
			b.WriteString(m.Content)
		}
	}
	return strings.TrimSpace(b.String())
}

func finishAutonomyLocked(th *agentThread, turn agentTurnSnapshot, outcome, activity string) {
	if outcome != "done" {
		pauseAutonomyLocked(th)
		return
	}
	if turn.autonomy == "" {
		th.goalActivity = ""
	}
	if turn.autonomy == "goal" && th.goal == turn.goal && th.goal != nil && th.goal.Status == "active" {
		if activity == "" || activity == th.goalActivity {
			th.goal.Status = "paused"
			th.pendingReasoning = true
		}
		th.goalActivity = activity
	}
	if turn.autonomy == "loop" && th.loop != nil && th.loop.Status == "active" && (th.loop.MaxIterations > 0 && th.loop.Iteration >= th.loop.MaxIterations || th.loop.DurationMs > 0 && !time.Now().Before(th.loop.deadline)) {
		th.loop.Status = "complete"
	}
}

type goalTool struct {
	srv    *Server
	entry  *agentSessionEntry
	thread *agentThread
	goal   *agentGoal
	epoch  int
}

func (*goalTool) Name() string { return "goal" }
func (*goalTool) Description() string {
	return "Inspect the explicitly authorized thread goal, or mark it complete only when its objective is achieved. Report final token usage."
}
func (*goalTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"op":{"type":"string","enum":["get","complete"]}},"required":["op"],"additionalProperties":false}`)
}
func (t *goalTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input struct {
		Op string `json:"op"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", err
	}
	t.srv.agentSessionsMu.Lock()
	th := t.thread
	if ctx.Err() != nil || t.entry.threads[th.id] != th || th.epoch != t.epoch || th.goal != t.goal || th.goal == nil || th.goal.Status != "active" {
		t.srv.agentSessionsMu.Unlock()
		return "", fmt.Errorf("goal mode is not active")
	}
	switch input.Op {
	case "get":
	case "complete":
		th.goal.Status = "complete"
		th.pendingReasoning = true
		cancelAutonomyPendingLocked(th)
	default:
		t.srv.agentSessionsMu.Unlock()
		return "", fmt.Errorf("unsupported goal operation")
	}
	result, _ := json.Marshal(cloneGoal(th.goal))
	t.srv.agentSessionsMu.Unlock()
	t.srv.persistAutonomy(t.entry, th)
	t.srv.publishAutonomy(t.entry, th, "")
	return string(result), nil
}

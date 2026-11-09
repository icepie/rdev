package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"rdev/internal/agent"
)

// OMP ImageContent uses mimeType; browser attachments use mime/name.
func parseRPCImages(raw json.RawMessage) ([]agent.MessageImage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if string(raw) == "null" {
		return nil, fmt.Errorf("images must be an array")
	}
	var input []struct {
		Type     string `json:"type"`
		MimeType string `json:"mimeType"`
		Data     string `json:"data"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, fmt.Errorf("images must be an array of ImageContent")
	}
	if len(input) > agentMaxImages {
		return nil, fmt.Errorf("too many images: max %d", agentMaxImages)
	}
	images := make([]agentImage, 0, len(input))
	for i, image := range input {
		if image.Type != "image" || !strings.HasPrefix(image.MimeType, "image/") || image.Data == "" {
			return nil, fmt.Errorf("image %d requires type=image, mimeType and data", i+1)
		}
		images = append(images, agentImage{Name: fmt.Sprintf("image-%d", i+1), Mime: image.MimeType, Data: image.Data})
	}
	encoded, _ := json.Marshal(images)
	return parseAgentImages(encoded)
}

// rpcTurnCompletion keeps a native RPC request correlated with the one turn
// that fulfills it. It is intentionally connection-scoped: turn frames are
// broadcast, but the RPC response must never leak to another browser.
type rpcTurnCompletion struct {
	conn    *agentConn
	request agentMsg
}

func (r *rpcTurnCompletion) write(ok bool, data any, code, message string) {
	if r == nil || r.conn == nil {
		return
	}
	var success = ok
	frame := agentMsg{Type: "response", Command: r.request.Command, ID: r.request.ID, Success: &success}
	if ok {
		if raw, err := json.Marshal(data); err == nil {
			frame.Data = raw
		}
	} else {
		frame.Code, frame.Error = code, message
	}
	r.conn.writeJSON(frame)
}

// Keep RPC commands separate from browser operations. In particular, an
// unsupported RPC name must not fall through to a mutating browser operation
// that neither validates OMP fields nor returns a correlated RPC response.
func (h *agentWSHandler) handleAgentRPCFrame(ac *agentConn, raw []byte) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return false
	}
	kind, present := fields["type"]
	if !present {
		return false
	}
	// RPC uses the same authentication gate as native browser commands.
	if !ac.authed {
		return true
	}
	request := agentMsg{RPC: true}
	// Preserve a usable correlation ID even when another field is malformed.
	_ = json.Unmarshal(fields["id"], &request.ID)
	fail := func(code, message string) {
		success := false
		ac.writeJSON(agentMsg{Type: "response", Command: request.Command, ID: request.ID, Success: &success, Code: code, Error: message})
	}
	var name string
	if len(kind) == 0 || kind[0] != '"' || json.Unmarshal(kind, &name) != nil || name == "" {
		fail("invalid_request", "type must be a non-empty string")
		return true
	}
	request.Command = name
	if name == "request" { // Previously shipped rdev wrapper, not OMP's native frame.
		if err := rpcStringField(fields, "command", &request.Command, true); err != nil {
			fail("invalid_request", err.Error())
			return true
		}
	}
	if err := rpcStringField(fields, "id", &request.ID, false); err != nil {
		request.ID = ""
		fail("invalid_request", err.Error())
		return true
	}
	if err := rpcStringField(fields, "thread", &request.Thread, false); err != nil {
		fail("invalid_request", err.Error())
		return true
	}
	// Reject mixed envelopes, rather than silently choosing between operations.
	if _, ok := fields["op"]; ok {
		fail("invalid_request", "RPC frames must not contain op")
		return true
	}
	if ac.deviceID != "" && ac.deviceID != agentServerDeviceID {
		if _, online := h.srv.GetClient(ac.deviceID); !online {
			switch request.Command {
			case "get_state", "get_messages", "get_models", "get_available_models", "get_commands", "get_session_stats":
			default:
				fail("device_offline", "Offline execution requires confirmation in the conversation UI")
				return true
			}
		}
	}
	switch request.Command {
	case "negotiate_protocol":
		h.handleRPCProtocol(ac, request)
	case "get_available_commands":
		h.handleRPCAvailableCommands(ac, request)
	case "get_state":
		h.handleRPCState(ac, request)
	case "get_jobs", "get_job", "cancel_job":
		if request.Command != "get_jobs" {
			if err := rpcStringField(fields, "jobId", &request.JobID, true); err != nil {
				fail("invalid_request", err.Error())
				return true
			}
		}
		h.handleRPCJobs(ac, request)
	case "get_available_models":
		h.handleRPCAvailableModels(ac, request)
	case "set_model":
		if err := rpcModelReference(fields, &request.Model); err != nil {
			fail("invalid_request", err.Error())
			return true
		}
		_, ompSelector := fields["provider"]
		h.handleRPCSetModel(ac, request, ompSelector)
	case "get_available_thinking_levels":
		h.handleRPCThinkingLevels(ac, request)
	case "cycle_thinking_level":
		h.handleRPCCycleThinkingLevel(ac, request)
	case "set_thinking_level":
		if err := rpcStringField(fields, "level", &request.Level, true); err != nil {
			fail("invalid_request", err.Error())
			return true
		}
		h.handleRPCSetThinkingLevel(ac, request)
	case "set_todos":
		h.handleRPCSetTodos(ac, request, fields["phases"])
	case "abort":
		h.handleRPCAbort(ac, request)
	case "abort_and_restore_queue":
		h.handleRPCAbortAndRestoreQueue(ac, request)
	case "prompt":
		if err := rpcStringField(fields, "message", &request.Message, false); err != nil {
			fail("invalid_request", err.Error())
			return true
		}
		request.Images = fields["images"]
		h.handleRPCPrompt(ac, request)
	case "steer", "follow_up":
		if err := rpcStringField(fields, "message", &request.Message, false); err != nil {
			fail("invalid_request", err.Error())
			return true
		}
		request.Images = fields["images"]
		h.handleRPCQueuedInput(ac, request)
	case "remove_queued_message":
		var queue string
		if err := rpcStringField(fields, "queue", &queue, true); err != nil {
			fail("invalid_request", err.Error())
			return true
		}
		if queue != "steering" && queue != "followUp" {
			fail("invalid_request", `queue must be "steering" or "followUp"`)
			return true
		}
		if _, exists := fields["message"]; !exists {
			fail("invalid_request", "message must be a string")
			return true
		}
		if err := rpcStringField(fields, "message", &request.Message, false); err != nil {
			fail("invalid_request", err.Error())
			return true
		}
		if _, exists := fields["queueId"]; exists {
			if err := rpcStringField(fields, "queueId", &request.QueueID, true); err != nil {
				fail("invalid_request", err.Error())
				return true
			}
		}
		h.handleRPCRemoveQueued(ac, request, queue)
	case "promote_queued_message":
		if err := rpcStringField(fields, "message", &request.Message, true); err != nil {
			fail("invalid_request", err.Error())
			return true
		}
		if _, exists := fields["queueId"]; exists {
			if err := rpcStringField(fields, "queueId", &request.QueueID, true); err != nil {
				fail("invalid_request", err.Error())
				return true
			}
		}
		h.handleRPCPromoteQueued(ac, request)
	case "get_subagents":
		h.handleGetSubagents(ac, request)
	case "get_subagent_messages":
		if err := rpcStringField(fields, "subagentId", &request.SubagentID, false); err != nil {
			fail("invalid_request", err.Error())
			return true
		}
		if err := rpcStringField(fields, "sessionFile", &request.SessionFile, false); err != nil {
			fail("invalid_request", err.Error())
			return true
		}
		if raw, ok := fields["fromByte"]; ok {
			var value int64
			if string(raw) == "null" || json.Unmarshal(raw, &value) != nil || value < 0 {
				fail("invalid_request", "fromByte must be a non-negative integer")
				return true
			}
			request.FromByte = &value
		}
		h.handleGetSubagentMessages(ac, request)
	case "steer_subagent", "cancel_subagent":
		if err := rpcStringField(fields, "subagentId", &request.SubagentID, true); err != nil {
			fail("invalid_request", err.Error())
			return true
		}
		if request.Command == "steer_subagent" {
			if err := rpcStringField(fields, "message", &request.Message, true); err != nil {
				fail("invalid_request", err.Error())
				return true
			}
			h.handleJobGuidance(ac, request)
		} else {
			h.handleCancelSubagent(ac, request)
		}
	case "compact":
		if err := rpcStringField(fields, "customInstructions", &request.CustomInstructions, false); err != nil {
			fail("invalid_request", err.Error())
			return true
		}
		h.handleRPCCompact(ac, request)
	default:
		fail("unsupported_command", fmt.Sprintf("unsupported RPC command %q", request.Command))
	}
	return true
}

type rpcTodoPhase struct {
	Name  string        `json:"name"`
	Tasks []rpcTodoTask `json:"tasks"`
}
type rpcTodoTask struct {
	Content string   `json:"content"`
	Status  string   `json:"status"`
	Blocker string   `json:"blocker,omitempty"`
	Details string   `json:"details,omitempty"`
	Notes   []string `json:"notes,omitempty"`
}

func (h *agentWSHandler) handleRPCSetTodos(ac *agentConn, request agentMsg, raw json.RawMessage) {
	if ac.entry == nil {
		h.writeRPCError(ac, request, "invalid_request", "unknown thread")
		return
	}
	var phases []rpcTodoPhase
	if len(raw) == 0 || json.Unmarshal(raw, &phases) != nil || phases == nil {
		h.writeRPCError(ac, request, "invalid_request", "phases must be an array")
		return
	}
	flat := make([]todoItem, 0)
	for pi, phase := range phases {
		if phase.Tasks == nil {
			h.writeRPCError(ac, request, "invalid_request", fmt.Sprintf("phase %d tasks must be an array", pi))
			return
		}
		if strings.TrimSpace(phase.Name) == "" {
			h.writeRPCError(ac, request, "invalid_request", fmt.Sprintf("phase %d name is required", pi))
			return
		}
		for ti, task := range phase.Tasks {
			if strings.TrimSpace(task.Content) == "" {
				h.writeRPCError(ac, request, "invalid_request", fmt.Sprintf("phase %d task %d content is required", pi, ti))
				return
			}
			switch task.Status {
			case "pending", "in_progress", "completed", "abandoned", "blocked":
			default:
				h.writeRPCError(ac, request, "invalid_request", fmt.Sprintf("phase %d task %d has invalid status", pi, ti))
				return
			}
			flat = append(flat, todoItem{Content: phase.Name + ": " + task.Content, Status: task.Status, Blocker: task.Blocker, Details: task.Details, Notes: task.Notes})
		}
	}
	threadID := request.Thread
	if threadID == "" {
		threadID = ac.current
	}
	s := h.srv
	s.agentSessionsMu.Lock()
	th := ac.entry.threads[threadID]
	if th == nil {
		s.agentSessionsMu.Unlock()
		h.writeRPCError(ac, request, "invalid_request", "unknown thread")
		return
	}
	th.todos = flat
	th.todoPhases = phases
	s.agentSessionsMu.Unlock()
	rawItems, _ := json.Marshal(flat)
	s.agentBroadcast(ac.entry, agentMsg{Op: "todo", Thread: threadID, Items: rawItems})
	success := true
	data, _ := json.Marshal(map[string]any{"todoPhases": phases})
	ac.writeJSON(agentMsg{Type: "response", Command: request.Command, ID: request.ID, Success: &success, Data: data})
}

func (h *agentWSHandler) handleRPCPrompt(ac *agentConn, request agentMsg) {
	if ac.entry == nil {
		h.writeRPCError(ac, request, "invalid_request", "unknown thread")
		return
	}
	s := h.srv
	images, err := parseRPCImages(request.Images)
	if err != nil {
		h.writeRPCError(ac, request, "invalid_request", err.Error())
		return
	}
	if strings.TrimSpace(request.Message) == "" && len(images) == 0 {
		h.writeRPCError(ac, request, "invalid_request", "message must not be blank")
		return
	}
	threadID := request.Thread
	if threadID == "" {
		threadID = ac.current
	}
	s.agentSessionsMu.Lock()
	th := ac.entry.threads[threadID]
	if th == nil {
		s.agentSessionsMu.Unlock()
		h.writeRPCError(ac, request, "invalid_request", "unknown thread")
		return
	}
	if th.running {
		s.agentSessionsMu.Unlock()
		h.writeRPCError(ac, request, "busy", "conversation is busy")
		return
	}
	start := s.startAgentTurnLocked(ac.entry, th, request.Message, images)
	turn := snapshotAgentTurnLocked(th)
	turn.rpc = &rpcTurnCompletion{conn: ac, request: request}
	s.agentSessionsMu.Unlock()
	s.agentBroadcastTurn(ac.entry, th, start)
	s.goAgent(func() { s.runAgentTurn(ac.entry, th, turn, request.Message, images) })
}

func (h *agentWSHandler) handleRPCQueuedInput(ac *agentConn, request agentMsg) {
	if ac.entry == nil {
		h.writeRPCError(ac, request, "invalid_request", "unknown thread")
		return
	}
	threadID := request.Thread
	if threadID == "" {
		threadID = ac.current
	}
	images, err := parseRPCImages(request.Images)
	if err != nil {
		h.writeRPCError(ac, request, "invalid_request", err.Error())
		return
	}
	text := strings.TrimSpace(request.Message)
	if text == "" && len(images) == 0 {
		h.writeRPCError(ac, request, "invalid_request", "message must not be blank")
		return
	}
	s := h.srv
	s.agentSessionsMu.Lock()
	th := ac.entry.threads[threadID]
	if th == nil {
		s.agentSessionsMu.Unlock()
		h.writeRPCError(ac, request, "invalid_request", "unknown thread")
		return
	}
	queued := th.running || len(th.queue) > 0
	var start agentMsg
	var turn agentTurnSnapshot
	if queued {
		item := agentQueuedChat{text: text, images: images}
		if request.Command == "steer" {
			queueAgentSteeringLocked(th, item)
		} else {
			th.queue = append(th.queue, item)
		}
		ac.entry.lastActive = time.Now()
	} else {
		s.rebindAgentSessionLocked(ac.entry)
		start = s.startAgentTurnLocked(ac.entry, th, text, images)
		turn = snapshotAgentTurnLocked(th)
	}
	s.agentSessionsMu.Unlock()
	// Acknowledge admission, as OMP does for steer/follow_up. Validation and
	// insertion share one lock hold, so deletion cannot produce a false ack.
	success := true
	ac.writeJSON(agentMsg{Type: "response", Command: request.Command, ID: request.ID, Success: &success})
	if queued {
		s.broadcastQueue(ac.entry, th)
		s.resumeAgentQueue(ac.entry, th)
	} else {
		s.agentBroadcastTurn(ac.entry, th, start)
		s.goAgent(func() { s.runAgentTurn(ac.entry, th, turn, text, images) })
	}
}

func (h *agentWSHandler) handleRPCAbort(ac *agentConn, request agentMsg) {
	if ac.entry == nil {
		h.writeRPCError(ac, request, "invalid_request", "unknown thread")
		return
	}
	threadID := request.Thread
	if threadID == "" {
		threadID = ac.current
	}
	s := h.srv
	s.agentSessionsMu.Lock()
	th := ac.entry.threads[threadID]
	if th == nil {
		s.agentSessionsMu.Unlock()
		h.writeRPCError(ac, request, "invalid_request", "unknown thread")
		return
	}
	cancel := th.cancel
	if cancel != nil {
		th.queue = nil
		denyApprovalsLocked(th)
	}
	s.agentSessionsMu.Unlock()
	if cancel == nil {
		// OMP treats abort as idempotent when no turn is active.
		success := true
		ac.writeJSON(agentMsg{Type: "response", Command: request.Command, ID: request.ID, Success: &success, Data: json.RawMessage(`{"aborted":false}`)})
		return
	}
	cancel()
	s.broadcastQueue(ac.entry, th)
	// The browser terminal is authoritative; this response acknowledges the
	// cancellation request while the stopped frame settles the running turn.
	success := true
	ac.writeJSON(agentMsg{Type: "response", Command: request.Command, ID: request.ID, Success: &success, Data: json.RawMessage(`{"aborted":true}`)})
}

type rpcRestoredQueuedMessage struct {
	Text   string            `json:"text"`
	Images []rpcImageContent `json:"images,omitempty"`
}

type rpcAbortRestoreQueueResult struct {
	Steering []rpcRestoredQueuedMessage `json:"steering"`
	FollowUp []rpcRestoredQueuedMessage `json:"followUp"`
}

// OMP's interrupt path withdraws user-authored queued input before aborting.
// System notifications are cleared with the interrupted queue, as for abort,
// but are not returned as editable prompts. Job records remain available.
func (h *agentWSHandler) handleRPCAbortAndRestoreQueue(ac *agentConn, request agentMsg) {
	if ac.entry == nil {
		h.writeRPCError(ac, request, "invalid_request", "unknown thread")
		return
	}
	threadID := request.Thread
	if threadID == "" {
		threadID = ac.current
	}
	s := h.srv
	s.agentSessionsMu.Lock()
	th := ac.entry.threads[threadID]
	if th == nil {
		s.agentSessionsMu.Unlock()
		h.writeRPCError(ac, request, "invalid_request", "unknown thread")
		return
	}
	result := rpcAbortRestoreQueueResult{Steering: []rpcRestoredQueuedMessage{}, FollowUp: []rpcRestoredQueuedMessage{}}
	for _, item := range th.queue {
		if !item.steering && len(item.display) > 0 {
			continue
		}
		message := rpcRestoredQueuedMessage{Text: item.text, Images: rpcImageContents(item.images)}
		if item.steering {
			result.Steering = append(result.Steering, message)
		} else {
			result.FollowUp = append(result.FollowUp, message)
		}
	}
	// Refuse before mutation if the restored payload cannot fit a bounded reply.
	// Unlike a lossy truncation, this leaves every queued text/image recoverable
	// through remove_queued_message before retrying the interruption.
	raw, err := json.Marshal(result)
	if err != nil || len(raw) > 16*1024*1024 {
		s.agentSessionsMu.Unlock()
		h.writeRPCError(ac, request, "response_too_large", "restore queue exceeds 16 MiB; remove individual queued messages first")
		return
	}
	th.queue = nil
	denyApprovalsLocked(th)
	cancel := th.cancel
	if cancel != nil {
		cancel()
	}
	s.agentSessionsMu.Unlock()
	s.broadcastQueue(ac.entry, th)
	success := true
	ac.writeJSON(agentMsg{Type: "response", Command: request.Command, ID: request.ID, Success: &success, Data: raw})
}

func (h *agentWSHandler) handleRPCState(ac *agentConn, request agentMsg) {
	threadID := request.Thread
	if threadID == "" {
		threadID = ac.current
	}
	if ac.entry == nil {
		h.writeRPCError(ac, request, "invalid_request", "unknown thread")
		return
	}
	s := h.srv
	s.agentSessionsMu.Lock()
	th := ac.entry.threads[threadID]
	if th == nil {
		s.agentSessionsMu.Unlock()
		h.writeRPCError(ac, request, "invalid_request", "unknown thread")
		return
	}
	snap := s.threadSnapshotLocked(ac.entry, th)
	steering, followUp := splitRPCQueue(snap.queue, snap.queueModes)
	pendingWork := th.jobs != nil && th.jobs.HasPendingWork()
	var modelInfo *rpcModel
	if provider, model, _, err := s.Agent.Resolve(th.model); err == nil {
		descriptor := describeRPCModel(provider, model)
		modelInfo = &descriptor
	}
	state := map[string]any{
		"thread": threadID, "model": modelInfo, "modelRef": th.model, "isStreaming": snap.running,
		"thinkingLevel": ac.entry.reasoningLevel,
		"isCompacting":  snap.running && th.workingTool == "/compact",
		"messageCount":  len(snap.history), "queuedMessageCount": len(snap.queue),
		"hasPendingAsyncWork": pendingWork,
		"isSettled":           !snap.running && len(snap.queue) == 0 && !pendingWork,
		"queuedMessages":      map[string]any{"followUp": followUp, "steering": steering, "liveSteered": len(steering)},
		"todoPhases":          th.todoPhases, "todos": snap.todos, "sessionFile": "rdev://thread/" + threadID,
	}
	s.agentSessionsMu.Unlock()
	success := true
	data, _ := json.Marshal(state)
	ac.writeJSON(agentMsg{Type: "response", Command: request.Command, ID: request.ID, Success: &success, Data: data})
}

func splitRPCQueue(items, modes []string) (steering, followUp []string) {
	for i, item := range items {
		if i < len(modes) && modes[i] == "steer" {
			steering = append(steering, item)
		} else {
			followUp = append(followUp, item)
		}
	}
	if steering == nil {
		steering = []string{}
	}
	if followUp == nil {
		followUp = []string{}
	}
	return steering, followUp
}

func (h *agentWSHandler) handleRPCAvailableModels(ac *agentConn, request agentMsg) {
	models := make([]rpcModel, 0)
	for i := range h.srv.Agent.Providers {
		provider := &h.srv.Agent.Providers[i]
		for j := range provider.Models {
			models = append(models, describeRPCModel(provider, &provider.Models[j]))
		}
	}
	success := true
	data, _ := json.Marshal(struct {
		Models []rpcModel `json:"models"`
	}{models})
	ac.writeJSON(agentMsg{Type: "response", Command: request.Command, ID: request.ID, Success: &success, Data: data})
}

func (h *agentWSHandler) handleRPCSetModel(ac *agentConn, request agentMsg, ompSelector bool) {
	if ac.entry == nil {
		h.writeRPCError(ac, request, "invalid_request", "unknown thread")
		return
	}
	s := h.srv
	provider, model, canonical, err := s.Agent.Resolve(request.Model)
	if err != nil {
		h.writeRPCError(ac, request, "invalid_request", err.Error())
		return
	}
	threadID := request.Thread
	if threadID == "" {
		threadID = ac.current
	}
	s.agentSessionsMu.Lock()
	th := ac.entry.threads[threadID]
	if th == nil {
		s.agentSessionsMu.Unlock()
		h.writeRPCError(ac, request, "invalid_request", "unknown thread")
		return
	}
	if th.running {
		s.agentSessionsMu.Unlock()
		h.writeRPCError(ac, request, "invalid_request", "conversation is busy")
		return
	}
	if th.model != canonical {
		newSession := s.buildAgentSession(ac.entry, th, ac.entry.client, provider, model)
		newSession.SetHistory(th.session.History())
		th.session = newSession
		th.sessionClient = ac.entry.client
		th.model = canonical
		ac.entry.model = canonical
		th.updatedAt = time.Now()
	}
	s.agentSessionsMu.Unlock()
	s.writeAgentPrefs(ac.entry)
	s.broadcastThreadList(ac.entry)
	success := true
	var result any = map[string]string{"model": canonical}
	if ompSelector {
		result = describeRPCModel(provider, model)
	}
	data, _ := json.Marshal(result)
	ac.writeJSON(agentMsg{Type: "response", Command: request.Command, ID: request.ID, Success: &success, Data: data})
}

// Caller holds agentSessionsMu; the current thread selects the capability set.
func (h *agentWSHandler) rpcThinkingLevels(ac *agentConn) []string {
	thread := ac.entry.threads[ac.current]
	if thread == nil {
		return nil
	}
	provider, model, _, err := h.srv.Agent.Resolve(thread.model)
	if err != nil {
		return nil
	}
	if provider.modelAPI(model) == "anthropic-messages" && model.Thinking.Mode == "anthropic-adaptive" {
		return []string{"low", "medium", "high"}
	}
	return []string{"off", "low", "medium", "high"}
}

func (h *agentWSHandler) handleRPCThinkingLevels(ac *agentConn, request agentMsg) {
	h.handleRPCThinkingControl(ac, request, false)
}

func (h *agentWSHandler) handleRPCCycleThinkingLevel(ac *agentConn, request agentMsg) {
	h.handleRPCThinkingControl(ac, request, true)
}

func (h *agentWSHandler) handleRPCThinkingControl(ac *agentConn, request agentMsg, cycle bool) {
	if ac.entry == nil || request.Thread != "" {
		h.writeRPCError(ac, request, "invalid_request", "thinking controls require an active device session; omit thread")
		return
	}
	h.srv.agentSessionsMu.Lock()
	levels := h.rpcThinkingLevels(ac)
	if len(levels) == 0 {
		h.srv.agentSessionsMu.Unlock()
		h.writeRPCError(ac, request, "invalid_request", "unknown thread or model")
		return
	}
	var data any = map[string]any{"levels": levels}
	level := ""
	if cycle {
		index := 0
		for i, candidate := range levels {
			if candidate == ac.entry.reasoningLevel {
				index = (i + 1) % len(levels)
				break
			}
		}
		level = levels[index]
		h.srv.setReasoningLevelLocked(ac.entry, level)
		data = map[string]string{"level": level}
	}
	h.srv.agentSessionsMu.Unlock()
	if cycle {
		h.srv.writeAgentPrefs(ac.entry)
		h.srv.agentBroadcast(ac.entry, agentMsg{Op: "reasoning_level", Level: level})
	}
	(&rpcTurnCompletion{conn: ac, request: request}).write(true, data, "", "")
}

func (h *agentWSHandler) handleRPCSetThinkingLevel(ac *agentConn, request agentMsg) {
	if ac.entry == nil {
		h.writeRPCError(ac, request, "invalid_request", "unknown thread")
		return
	}
	if request.Thread != "" {
		h.writeRPCError(ac, request, "invalid_request", "thinking level is device-scoped; omit thread")
		return
	}
	if !validReasoningLevel(request.Level) {
		h.writeRPCError(ac, request, "invalid_request", "unknown thinking level "+request.Level)
		return
	}
	// The existing browser handler updates the shared preference and rebuilds
	// idle sessions; running turns keep their snapshot and apply the level next.
	h.handleSetReasoningLevel(ac, request.Level)
	success := true
	ac.writeJSON(agentMsg{Type: "response", Command: request.Command, ID: request.ID, Success: &success})
}

func (h *agentWSHandler) handleRPCProtocol(ac *agentConn, request agentMsg) {
	success := true
	data, _ := json.Marshal(struct {
		ProtocolVersion int `json:"protocolVersion"`
	}{2})
	ac.writeJSON(agentMsg{Type: "response", Command: request.Command, ID: request.ID, Success: &success, Data: data})
}

func (h *agentWSHandler) handleRPCAvailableCommands(ac *agentConn, request agentMsg) {
	// OMP commands are slash commands accepted by prompt. Keep RPC method
	// discovery in a separate rdev extension, so palettes never advertise
	// method names or browser-only controls as runnable prompt commands.
	commands := []map[string]any{
		{"name": "get_jobs", "source": "rdev", "params": map[string]any{"thread": "string?"}},
		{"name": "get_job", "source": "rdev", "params": map[string]any{"thread": "string?", "jobId": "string"}},
		{"name": "cancel_job", "source": "rdev", "params": map[string]any{"thread": "string?", "jobId": "string|all"}},
		{"name": "negotiate_protocol"}, {"name": "get_available_commands"},
		{"name": "get_state", "params": map[string]any{"thread": "string?"}},
		{"name": "get_available_models"},
		{"name": "set_model", "params": map[string]any{"provider": "string", "modelId": "string", "thread": "string?"}, "legacyParams": map[string]any{"model": "string"}},
		{"name": "get_available_thinking_levels"},
		{"name": "cycle_thinking_level"},
		{"name": "set_thinking_level", "params": map[string]any{"level": "off|low|medium|high"}},
		{"name": "set_todos", "params": map[string]any{"phases": "TodoPhase[]"}},
		{"name": "abort", "params": map[string]any{"thread": "string?"}},
		{"name": "abort_and_restore_queue", "params": map[string]any{"thread": "string?"}},
		{"name": "prompt", "params": map[string]any{"message": "string", "thread": "string?"}},
		{"name": "steer", "params": map[string]any{"message": "string", "thread": "string?"}},
		{"name": "follow_up", "params": map[string]any{"message": "string", "thread": "string?"}},
		{"name": "remove_queued_message", "params": map[string]any{"message": "string", "queue": "steering|followUp", "queueId": "string?"}},
		{"name": "promote_queued_message", "params": map[string]any{"message": "string", "queueId": "string?"}},
		{"name": "compact", "params": map[string]any{"customInstructions": "string?"}},
		{"name": "get_subagents"}, {"name": "get_subagent_messages", "params": map[string]any{"subagentId": "string?", "sessionFile": "string?", "fromByte": "non-negative integer?", "thread": "string?"}},
		{"name": "steer_subagent", "params": map[string]any{"subagentId": "string", "message": "string"}},
		{"name": "cancel_subagent", "params": map[string]any{"subagentId": "string"}},
	}
	success := true
	data, _ := json.Marshal(struct {
		RPCCommands []map[string]any `json:"rpcCommands"`
		Commands    []map[string]any `json:"commands"`
	}{
		RPCCommands: commands,
		Commands: []map[string]any{
			{"name": "jobs", "description": "Inspect or cancel this thread's background jobs.", "input": map[string]any{"hint": "[full|kill <id>|kill all]"}, "source": "builtin"},
			{"name": "compact", "description": "Compact the current conversation context.", "input": map[string]any{"hint": "custom instructions?"}, "source": "builtin"},
			{"name": "bg", "description": "Run a command in the background.", "input": map[string]any{"hint": "[--device <id>] command"}, "source": "builtin"},
			{"name": "subagent", "description": "Start a background subagent task.", "input": map[string]any{"hint": "task"}, "source": "builtin"},
		},
	})
	ac.writeJSON(agentMsg{Type: "response", Command: request.Command, ID: request.ID, Success: &success, Data: data})
}

func (h *agentWSHandler) handleRPCCompact(ac *agentConn, request agentMsg) {
	threadID := request.Thread
	if threadID == "" {
		threadID = ac.current
	}
	if ac.entry == nil {
		h.writeRPCError(ac, request, "invalid_request", "unknown thread")
		return
	}
	s := h.srv
	s.agentSessionsMu.Lock()
	th := ac.entry.threads[threadID]
	if th == nil {
		s.agentSessionsMu.Unlock()
		h.writeRPCError(ac, request, "invalid_request", "unknown thread")
		return
	}
	if th.running {
		s.agentSessionsMu.Unlock()
		h.writeRPCError(ac, request, "invalid_request", "conversation is busy")
		return
	}
	text := "/compact"
	if request.CustomInstructions != "" {
		text += " " + request.CustomInstructions
	}
	start := s.startAgentTurnLocked(ac.entry, th, text, nil)
	turn := snapshotAgentTurnLocked(th)
	turn.rpc = &rpcTurnCompletion{conn: ac, request: request}
	s.agentSessionsMu.Unlock()
	s.agentBroadcastTurn(ac.entry, th, start)
	s.goAgent(func() { s.runAgentTurn(ac.entry, th, turn, text, nil) })
}

func (h *agentWSHandler) writeRPCError(ac *agentConn, request agentMsg, code, message string) {
	success := false
	ac.writeJSON(agentMsg{Type: "response", Command: request.Command, ID: request.ID, Success: &success, Code: code, Error: message})
}

// OMP uses separate provider/modelId fields. Keep the shipped rdev model
// reference as an alternative, but never silently choose between both forms.
func rpcModelReference(fields map[string]json.RawMessage, dest *string) error {
	_, hasProvider := fields["provider"]
	_, hasModelID := fields["modelId"]
	if !hasProvider && !hasModelID {
		return rpcStringField(fields, "model", dest, true)
	}
	if _, mixed := fields["model"]; mixed {
		return fmt.Errorf("use provider/modelId or model, not both")
	}
	var provider, modelID string
	if err := rpcStringField(fields, "provider", &provider, true); err != nil {
		return err
	}
	if err := rpcStringField(fields, "modelId", &modelID, true); err != nil {
		return err
	}
	if strings.Contains(provider, "/") {
		return fmt.Errorf("provider must not contain /")
	}
	*dest = provider + "/" + modelID
	return nil
}

func rpcStringField(fields map[string]json.RawMessage, key string, dest *string, required bool) error {
	raw, exists := fields[key]
	if !exists && !required {
		return nil
	}
	if !exists || len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, dest) != nil || (required && *dest == "") {
		if required {
			return fmt.Errorf("%s must be a non-empty string", key)
		}
		return fmt.Errorf("%s must be a string", key)
	}
	return nil
}

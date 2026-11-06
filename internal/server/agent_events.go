package server

import (
	"encoding/json"
	"slices"

	"rdev/internal/agent"
)

// Apply state and record its replay frame atomically. Late model/tool events
// from a reset conversation must not repopulate the new conversation.
func (s *Server) emitAgentTurnEvent(entry *agentSessionEntry, th *agentThread, epoch int, event agent.Event) bool {
	s.agentSessionsMu.Lock()
	if th.epoch != epoch || entry.threads[th.id] != th {
		s.agentSessionsMu.Unlock()
		return false
	}
	msg := agentMsg{Thread: th.id}
	switch event.Type {
	case agent.EventTextDelta:
		msg.Op, msg.Text = "delta", event.Text
	case agent.EventReasoning:
		msg.Op, msg.Text = "reasoning", event.Reasoning
	case agent.EventToolStart:
		th.workingTool = event.ToolName
		if event.ToolName == "edit" || event.ToolName == "write_file" {
			if p := toolPathArg(event.ToolArgs); p != "" {
				th.toolPaths[event.ToolID] = p
			}
		}
		msg.Op, msg.ID, msg.Name, msg.Args, msg.Summary = "tool_start", event.ToolID, event.ToolName, string(event.ToolArgs), event.Summary
	case agent.EventToolEnd:
		th.workingTool = ""
		if p, ok := th.toolPaths[event.ToolID]; ok {
			delete(th.toolPaths, event.ToolID)
			if !event.IsError && !slices.Contains(th.turnFiles, p) {
				th.turnFiles = append(th.turnFiles, p)
			}
		}
		msg.Op, msg.ID, msg.Name, msg.Result, msg.IsError, msg.Diff = "tool_end", event.ToolID, event.ToolName, event.ToolResult, event.IsError, event.Diff
	case agent.EventTodo:
		var items []todoItem
		if json.Unmarshal([]byte(event.ToolResult), &items) == nil {
			th.todos = items
			msg.Op, msg.Items = "todo", json.RawMessage(event.ToolResult)
		}
	case agent.EventDone:
		th.lastUsage = &agentUsage{InputTokens: event.Usage.InputTokens, OutputTokens: event.Usage.OutputTokens}
	}
	var data []byte
	if msg.Op != "" {
		data, _ = json.Marshal(msg)
		if data != nil {
			th.live = append(th.live, json.RawMessage(data))
		}
	}
	s.agentSessionsMu.Unlock()
	if data != nil {
		s.agentBroadcastData(entry, data)
	}
	return true
}

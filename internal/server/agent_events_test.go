package server

import (
	"encoding/json"
	"rdev/internal/agent"
	"testing"
)

func TestResetRejectsLateTurnEvents(t *testing.T) {
	srv := newPrefsTestServer(t)
	entry, err := srv.agentSessionFor(agentServerDeviceID, &ClientConn{ID: agentServerDeviceID})
	if err != nil {
		t.Fatal(err)
	}
	th := entry.threads[entry.current]
	srv.agentSessionsMu.Lock()
	srv.startAgentTurnLocked(entry, th, "edit file", nil)
	turn := snapshotAgentTurnLocked(th)
	srv.agentSessionsMu.Unlock()
	defer turn.cancel()
	(&agentWSHandler{srv: srv}).handleReset(&agentConn{entry: entry, current: th.id, deviceID: entry.deviceID}, th.id)
	for _, event := range []agent.Event{
		{Type: agent.EventUserInput, Text: "late steering"},
		{Type: agent.EventTextDelta, Text: "late text"},
		{Type: agent.EventReasoning, Reasoning: "late reasoning"},
		{Type: agent.EventToolStart, ToolName: "edit", ToolID: "tool", ToolArgs: json.RawMessage(`{"path":"/file"}`)},
		{Type: agent.EventToolEnd, ToolName: "edit", ToolID: "tool"},
		{Type: agent.EventTodo, ToolResult: `[{"text":"stale","status":"pending"}]`},
		{Type: agent.EventDone, Usage: agent.Usage{InputTokens: 99}},
	} {
		if srv.emitAgentTurnEvent(entry, th, turn.epoch, event) {
			t.Fatalf("accepted late event: %v", event.Type)
		}
	}
	if len(th.live) != 0 || th.lastUsage != nil || len(th.todos) != 0 || th.workingTool != "" || len(th.turnFiles) != 0 {
		t.Fatal("late events repopulated reset state")
	}
	// Current events must still stream and replay normally.
	if !srv.emitAgentTurnEvent(entry, th, th.epoch, agent.Event{Type: agent.EventTextDelta, Text: "current"}) || len(th.live) != 1 {
		t.Fatal("current event was lost")
	}
}

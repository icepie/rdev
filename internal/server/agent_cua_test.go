package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAgentCuaToolCallsDriverToolsDirectly(t *testing.T) {
	fixture := &gpuAgentFixture{
		cua:            true,
		nativeResult:   json.RawMessage(`{"CuaResult":{"result":{"content":[{"type":"text","text":"ok"}]}}}`),
		nativeRequests: make(chan json.RawMessage, 4),
	}
	base, client := newGPUCuaTool(t, fixture)
	cua := &cuaTool{automationTool: base.automationTool}
	defer func() { base.state.opMu.Lock(); closeDesktopAutomation(base.state); base.state.opMu.Unlock() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	inventory, err := cua.Execute(ctx, json.RawMessage(`{"action":"tools"}`))
	if err != nil || !strings.Contains(inventory, `"set_agent_cursor_enabled"`) {
		t.Fatalf("direct cua discovery omitted SDK tool: %s %v", inventory, err)
	}
	if _, err := cua.Execute(ctx, json.RawMessage(`{"action":"call","tool":"set_agent_cursor_enabled","arguments":{"enabled":true}}`)); err != nil {
		t.Fatal(err)
	}
	<-fixture.nativeRequests // tools/list
	call := <-fixture.nativeRequests
	if string(call) != `{"action":"call","name":"set_agent_cursor_enabled","arguments":{"enabled":true}}` {
		t.Fatalf("unexpected direct CUA call: %s", call)
	}
	if fixture.connections.Load() != 1 || base.state.gpu[client.ID] == nil {
		t.Fatal("CUA call did not reuse the native session")
	}
}

package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestAgentCuaAllowsCallWithoutScreenshot(t *testing.T) {
	fixture := &gpuAgentFixture{
		cua:            true,
		nativeResult:   json.RawMessage(`{"CuaResult":{"result":{"content":[],"isError":false}}}`),
		nativeRequests: make(chan json.RawMessage, 4),
	}
	base, _ := newGPUCuaTool(t, fixture)
	cua := &cuaTool{automationTool: base.automationTool}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for range 2 {
		if _, err := cua.Execute(ctx, json.RawMessage(`{"action":"call","tool":"set_agent_cursor_enabled","arguments":{"enabled":true}}`)); err != nil {
			t.Fatalf("native CUA call required a screenshot: %v", err)
		}
	}
	if len(fixture.nativeRequests) != 3 {
		t.Fatalf("native requests=%d, want one discovery and two calls", len(fixture.nativeRequests))
	}
}

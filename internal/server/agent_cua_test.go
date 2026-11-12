package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestAgentCuaRejectsUnobservedCall(t *testing.T) {
	fixture := &gpuAgentFixture{
		cua:            true,
		nativeRequests: make(chan json.RawMessage, 4),
	}
	base, _ := newGPUCuaTool(t, fixture)
	cua := &cuaTool{automationTool: base.automationTool}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := cua.Execute(ctx, json.RawMessage(`{"action":"call","tool":"set_agent_cursor_enabled","arguments":{"enabled":true}}`)); err == nil {
		t.Fatal("native CUA call bypassed the screenshot requirement")
	}
	select {
	case request := <-fixture.nativeRequests:
		t.Fatalf("unobserved call reached device: %s", request)
	default:
	}
}

package server

import (
	"rdev/internal/agent"
	"reflect"
	"testing"
)

func TestRPCThinkingLevelsRespectCurrentModel(t *testing.T) {
	srv := newPrefsTestServer(t)
	srv.Agent.Providers[0].Models = append(srv.Agent.Providers[0].Models, AgentModelConfig{
		ID: "adaptive", API: "anthropic-messages", Thinking: agent.AnthropicThinkingConfig{Mode: "anthropic-adaptive"},
	})
	entry, err := srv.agentSessionFor(agentServerDeviceID, &ClientConn{ID: agentServerDeviceID})
	if err != nil {
		t.Fatal(err)
	}
	ac := &agentConn{entry: entry, current: entry.current}
	handler := &agentWSHandler{srv: srv}
	srv.agentSessionsMu.Lock()
	defer srv.agentSessionsMu.Unlock()
	thread := entry.threads[entry.current]
	thread.model = "p/adaptive"
	if got := handler.rpcThinkingLevels(ac); !reflect.DeepEqual(got, []string{"low", "medium", "high"}) {
		t.Fatalf("adaptive levels: %v", got)
	}
	thread.model = "p/m1"
	if got := handler.rpcThinkingLevels(ac); !reflect.DeepEqual(got, []string{"off", "low", "medium", "high"}) {
		t.Fatalf("default levels: %v", got)
	}
	ac.current = "missing"
	if got := handler.rpcThinkingLevels(ac); got != nil {
		t.Fatalf("unknown thread: %v", got)
	}
}

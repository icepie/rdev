package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"rdev/internal/agent"
)

func TestServerSessionToolsManageOfflineHistoryWithoutSwitchingView(t *testing.T) {
	s := newPrefsTestServer(t)
	t.Cleanup(s.waitAgentWork)
	s.AdminToken = "test-admin"
	owner, err := s.agentSessionFor(agentServerDeviceID, nil)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := s.agentSessionFor("offline", nil)
	if err != nil {
		t.Fatal(err)
	}
	current := owner.current
	read := &agentSessionsTool{srv: s, owner: owner, thread: current}
	write := &agentSessionsTool{srv: s, owner: owner, thread: current, mutate: true}
	if read.Dangerous() || !write.Dangerous() {
		t.Fatal("wrong mutation approval classification")
	}
	run := func(tool *agentSessionsTool, args map[string]any) map[string]json.RawMessage {
		t.Helper()
		raw, _ := json.Marshal(args)
		out, err := tool.Execute(context.Background(), raw)
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]json.RawMessage
		if err = json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	created := run(write, map[string]any{"action": "create", "device": "offline", "title": "Inspect service"})
	var id string
	_ = json.Unmarshal(created["thread"], &id)
	s.agentSessionsMu.Lock()
	th := remote.threads[id]
	if th == nil || th.title != "Inspect service" {
		s.agentSessionsMu.Unlock()
		t.Fatal("created wrong thread")
	}
	th.session.SetHistory([]agent.Message{{Role: "user", Content: "saved question"}, {Role: "assistant", Content: "saved reply"}})
	s.agentSessionsMu.Unlock()
	result := run(read, map[string]any{"action": "read", "device": "offline", "thread": id, "limit": 1})
	if string(result["hasMore"]) != "true" || !strings.Contains(string(result["history"]), "saved question") {
		t.Fatalf("bad history page: %s", result)
	}
	result = run(read, map[string]any{"action": "read", "device": "offline", "thread": id, "offset": 1, "limit": 1})
	if !strings.Contains(string(result["history"]), "saved reply") {
		t.Fatal("missing next page")
	}
	run(write, map[string]any{"action": "rename", "device": "offline", "thread": id, "title": "Renamed"})
	out, err := read.Execute(context.Background(), json.RawMessage(`{"action":"list","device":"offline"}`))
	if err != nil || !strings.Contains(out, "Renamed") {
		t.Fatalf("list: %s %v", out, err)
	}
	run(write, map[string]any{"action": "delete", "device": "offline", "thread": id})
	if owner.current != current {
		t.Fatal("management switched calling conversation")
	}
	s.agentSessionsMu.Lock()
	_, exists := remote.threads[id]
	s.agentSessionsMu.Unlock()
	if exists {
		t.Fatal("deleted thread retained")
	}
	for _, metadata := range s.agentStore().listThreads("offline") {
		if metadata.ID == id {
			t.Fatal("deleted history retained")
		}
	}
}

func TestServerSessionToolsRejectUnauthorizedAndSelfDeletion(t *testing.T) {
	s := newPrefsTestServer(t)
	t.Cleanup(s.waitAgentWork)
	owner, err := s.agentSessionFor(agentServerDeviceID, nil)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := s.agentSessionFor("offline", nil)
	if err != nil {
		t.Fatal(err)
	}
	write := &agentSessionsTool{srv: s, owner: owner, thread: owner.current, mutate: true}
	for _, args := range []map[string]any{
		{"action": "delete", "device": agentServerDeviceID, "thread": owner.current},
		{"action": "rename", "device": "offline", "thread": remote.current, "title": "Unauthorized"},
		{"action": "create", "device": "unknown"},
		{"action": "read", "device": agentServerDeviceID, "thread": owner.current},
	} {
		raw, _ := json.Marshal(args)
		if _, err := write.Execute(context.Background(), raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	read := &agentSessionsTool{srv: s, owner: remote, thread: remote.current}
	if _, err := read.Execute(context.Background(), json.RawMessage(`{"action":"list"}`)); err == nil {
		t.Fatal("device agent gained global history access")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := write.Execute(ctx, json.RawMessage(`{"action":"create","device":"@server"}`)); err == nil {
		t.Fatal("canceled management accepted")
	}
}

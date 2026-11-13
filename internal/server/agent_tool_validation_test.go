package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"rdev/internal/agent"
)

func TestDeviceToolSchemasRejectInvalidCallsBeforeApproval(t *testing.T) {
	for _, tc := range []struct {
		tool agent.Tool
		args string
	}{
		{&shellTool{}, `{}`},
		{&listDirTool{}, `{}`},
		{&writeFileTool{}, `{"path":"/important-file"}`},
		{&writeFileTool{}, `{"path":"/important-file","content":null}`},
		{&backgroundTool{}, `{"command":true}`},
		{&jobsTool{}, `{"action":"wait","timeout":31}`},
		{&subagentTool{}, `{"task":"inspect","tools":"shell"}`},
	} {
		t.Run(tc.tool.Name()+tc.args, func(t *testing.T) {
			var hits atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if hits.Add(1) == 1 {
					chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{
						"tool_calls": []any{map[string]any{"index": 0, "id": "invalid", "type": "function",
							"function": map[string]any{"name": tc.tool.Name(), "arguments": tc.args}}},
					}}}})
					fmt.Fprintf(w, "data: %s\n\n%s", chunk, sseDoneLine)
					return
				}
				var req struct {
					Messages []agent.Message `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				last := req.Messages[len(req.Messages)-1]
				if last.Role != "tool" || last.ToolCallID != "invalid" || !strings.Contains(last.Content, "invalid arguments") ||
					!strings.Contains(last.Content, string(tc.tool.Schema())) {
					t.Errorf("model did not receive schema error: %+v", last)
				}
				fmt.Fprint(w, sseTextLine("correct arguments needed")+sseDoneLine)
			}))
			defer provider.Close()
			approvals := 0
			session := agent.New(agent.Config{
				BaseURL: provider.URL, Model: "fixture", Tools: []agent.Tool{tc.tool}, MaxTurns: 2,
				ApproveAll: func() bool { return true },
				Approve:    func(agent.ToolCall, json.RawMessage) (bool, error) { approvals++; return false, nil },
			})
			if _, err := session.Run(context.Background(), "inspect", nil); err != nil {
				t.Fatal(err)
			}
			if approvals != 0 || hits.Load() != 2 {
				t.Fatalf("approvals=%d requests=%d", approvals, hits.Load())
			}
		})
	}
}

func TestServerModeValidatesNestedDeviceArguments(t *testing.T) {
	srv := newPrefsTestServer(t)
	transport := &recordTransport{}
	srv.clients = map[string]*ClientConn{"remote": {ID: "remote", OS: "linux", Transport: transport}}
	tool := &remoteDeviceTool{srv: srv}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, args := range []string{
		`{"device":"remote","tool":"write_file","arguments":{"path":"/important-file"}}`,
		`{"device":"remote","tool":"write_file","arguments":{"path":"/important-file","content":null}}`,
		`{"device":"remote","tool":"shell","arguments":{"command":42}}`,
	} {
		if _, err := tool.Execute(ctx, json.RawMessage(args)); err == nil || !strings.Contains(err.Error(), "invalid arguments") {
			t.Fatalf("nested invalid args accepted: %s, error=%v", args, err)
		}
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if len(transport.json) != 0 {
		t.Fatalf("invalid calls sent %d device messages", len(transport.json))
	}
}

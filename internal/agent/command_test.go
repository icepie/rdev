package agent

import (
	"context"
	"encoding/json"
	"testing"
)

func TestToolCommandRespectsApprovalWithoutProvider(t *testing.T) {
	for _, allow := range []bool{false, true} {
		tool := &fakeTool{name: "bg", dangerous: true}
		approvals := 0
		sess := New(Config{Tools: []Tool{tool}, Approve: func(tc ToolCall, args json.RawMessage) (bool, error) {
			approvals++
			return allow, nil
		}})
		var outcome Event
		err := sess.RunToolCommand(context.Background(), "/bg echo hi", "bg", json.RawMessage(`{"command":"echo hi"}`), func(e Event) {
			if e.Type == EventToolEnd {
				outcome = e
			}
		})
		if (err == nil) != allow || tool.ran != allow || approvals != 1 {
			t.Fatalf("allow=%v ran=%v approvals=%d err=%v", allow, tool.ran, approvals, err)
		}
		history := sess.History()
		if len(history) != 3 || history[1].ToolCalls[0].ID != history[2].ToolCallID {
			t.Fatalf("incomplete history: %+v", history)
		}
		if history[1].ToolCalls[0].IsError == allow {
			t.Fatalf("persisted tool failure incorrect: %+v", history[1])
		}
		if outcome.Type != EventToolEnd || outcome.IsError == allow {
			t.Fatalf("streamed tool failure incorrect: %+v", outcome)
		}
	}
}

func TestCanceledToolCommandDoesNotExecute(t *testing.T) {
	tool := &fakeTool{name: "bg"}
	sess := New(Config{Tools: []Tool{tool}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sess.RunToolCommand(ctx, "/bg echo hi", "bg", json.RawMessage(`{}`), nil); err != context.Canceled || tool.ran || len(sess.History()) != 0 {
		t.Fatalf("canceled command executed: %v", err)
	}
}

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type cancelingTool struct {
	fakeTool
	cancel context.CancelFunc
}

func (tool *cancelingTool) Execute(context.Context, json.RawMessage) (string, error) {
	tool.ran = true
	tool.cancel()
	return "first tool completed", nil
}

func TestCancelSkipsRemainingToolsAndKeepsResultPairs(t *testing.T) {
	first := sseToolCall("first", "read_file", `{}`)
	second := sseToolCall("second", "write_file", `{}`)
	second["index"] = 1
	provider, hits := newFakeProvider(t, sseChunk("", "", first, second)+sseDone)
	defer provider.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	read := &cancelingTool{fakeTool: fakeTool{name: "read_file"}, cancel: cancel}
	write := &fakeTool{name: "write_file"}
	session := New(testConfig(provider.URL, []Tool{read, write}))
	var events []Event
	_, err := session.Run(ctx, "read then write", func(event Event) { events = append(events, event) })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if !read.ran || write.ran || hits.Load() != 1 {
		t.Fatalf("executed after cancellation: read=%v write=%v requests=%d", read.ran, write.ran, hits.Load())
	}
	history := session.History()
	if len(history) != 4 || len(history[1].ToolCalls) != 2 || history[2].ToolCallID != "first" || history[3].ToolCallID != "second" || history[3].Content != "error: context canceled" {
		t.Fatalf("broken tool/result chain: %+v", history)
	}
	ends := eventsOfType(events, EventToolEnd)
	if len(ends) != 2 || ends[0].IsError || !ends[1].IsError || ends[1].ToolID != "second" {
		t.Fatalf("wrong tool lifecycle: %+v", ends)
	}
}

func TestCancellationBeforeOrDuringApprovalPreventsExecution(t *testing.T) {
	for _, duringApproval := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		tool := &fakeTool{name: "write_file", dangerous: true}
		approvals := 0
		session := New(Config{Tools: []Tool{tool}, Approve: func(ToolCall, json.RawMessage) (bool, error) { approvals++; cancel(); return true, nil }})
		if !duringApproval {
			cancel()
		}
		output, failed := session.executeTool(ctx, tool, ToolCall{Name: tool.name}, json.RawMessage(`{}`))
		cancel()
		expectedApprovals := 0
		if duringApproval {
			expectedApprovals = 1
		}
		if !failed || tool.ran || approvals != expectedApprovals || output != "error: context canceled" {
			t.Fatalf("duringApproval=%v ran=%v approvals=%d result=%s", duringApproval, tool.ran, approvals, output)
		}
	}
}

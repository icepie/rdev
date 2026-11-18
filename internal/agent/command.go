package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// RunToolCommand executes an explicit user command without asking the model to
// choose a tool. Callers serialize it with RunTurn. Approval and history use the
// same contracts as model-requested calls.
func (s *Session) RunToolCommand(ctx context.Context, text, name string, args json.RawMessage, emit func(Event)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tool := s.findTool(name)
	if tool == nil || !json.Valid(args) {
		return fmt.Errorf("invalid command tool or arguments: %s", name)
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	tc := ToolCall{ID: "command_" + hex.EncodeToString(nonce[:]), Name: name, Arguments: string(args)}
	if sm, ok := tool.(Summarizer); ok {
		tc.Summary = sm.Summary(args)
	}
	if emit == nil {
		emit = func(Event) {}
	}
	userID, _ := ctx.Value(userMessageIDKey{}).(string)
	s.appendHistory(Message{MessageID: userID, Role: "user", Content: text})
	if err := s.captureHistory(); err != nil {
		return err
	}
	assistantID := NewMessageID()
	emit(Event{Type: EventAssistantStart, MessageID: assistantID})
	emit(Event{Type: EventToolStart, ToolID: tc.ID, ToolName: name, ToolArgs: args, Summary: tc.Summary})
	result, failed := s.executeTool(ctx, tool, tc, args, emit)
	tc.IsError = failed
	archiveResult := result
	result = truncateToolOutput(result)
	// Keep the complete call/result chain even when approval is denied or the
	// tool is canceled. Reconnect and subsequent model turns see the same result.
	s.appendHistory(
		Message{MessageID: assistantID, Role: "assistant", ToolCalls: []ToolCall{tc}},
		Message{Role: "tool", ToolCallID: tc.ID, Content: result, ArchiveContent: archiveResult},
	)
	if err := s.captureHistory(); err != nil {
		return err
	}
	emit(Event{Type: EventToolEnd, ToolID: tc.ID, ToolName: name, ToolResult: result, IsError: failed})
	if err := ctx.Err(); err != nil {
		return err
	}
	if failed {
		return fmt.Errorf("%s command failed: %s", name, result)
	}
	return nil
}

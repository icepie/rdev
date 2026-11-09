// session.go is the multi-turn agent loop: stream the model, forward text
// deltas, execute requested tool calls, feed results back, until the model
// stops calling tools or MaxTurns is reached.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"
)

// Session is one agent conversation (config + history). Callers must
// serialize Run calls, but History, SetHistory and Reset are safe to call
// while a Run is in progress (e.g. a browser reattaching mid-turn).
type Session struct {
	cfg           Config
	mu            sync.Mutex // guards history
	history       []Message
	toolArguments ToolArgumentValidator
}

// Run appends userText to the conversation and drives the model until it
// produces a final answer (no tool calls) or MaxTurns is exhausted, emitting
// progress events to emit (may be nil). It returns the final assistant text.
func (s *Session) Run(ctx context.Context, userText string, emit func(Event)) (string, error) {
	return s.RunWithImages(ctx, userText, nil, emit)
}

// RunWithImages is Run with image attachments on the user message. The
// images are sent to the provider as content parts; only their names survive
// history persistence.
func (s *Session) RunWithImages(ctx context.Context, userText string, images []MessageImage, emit func(Event)) (string, error) {
	return s.RunTurn(ctx, userText, images, "", emit)
}

// RunTurn is RunWithImages with a per-turn reasoning-effort override:
// effortOverride replaces cfg.ReasoningEffort for this run only ("" keeps the
// configured level). Used for magic keywords like "ultrathink".
func (s *Session) RunTurn(ctx context.Context, userText string, images []MessageImage, effortOverride string, emit func(Event)) (string, error) {
	return s.RunTurnWithSteering(ctx, userText, images, effortOverride, emit, nil)
}

// RunTurnWithSteering drains queued user input only at settled response/tool
// boundaries. The caller owns the queue, so input arriving after the last
// boundary or after cancellation remains available for a subsequent run.
// takeSteering must return at most one user message, or nil when none is ready.
func (s *Session) RunTurnWithSteering(ctx context.Context, userText string, images []MessageImage, effortOverride string, emit func(Event), takeSteering func() *Message) (string, error) {
	if emit == nil {
		emit = func(Event) {}
	}
	cfg := s.cfg
	if effortOverride != "" {
		cfg.ReasoningEffort = effortOverride
	}
	s.appendHistory(Message{Role: "user", Content: userText, Images: images})

	injectSteering := func() bool {
		if takeSteering == nil || ctx.Err() != nil {
			return false
		}
		message := takeSteering()
		if message == nil {
			return false
		}
		s.appendHistory(*message)
		emit(Event{Type: EventUserInput, Text: message.Content, Display: message.Display, Images: message.Images})
		return true
	}
	var total Usage
	for round := 0; cfg.MaxTurns == 0 || round < cfg.MaxTurns; round++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		s.compactToolOutputs()
		emit(Event{Type: EventAssistantStart})
		assistant, usage, err := streamChat(ctx, cfg, s.History(),
			func(delta string) {
				emit(Event{Type: EventTextDelta, Text: delta})
			},
			func(delta string) {
				emit(Event{Type: EventReasoning, Text: delta})
			})
		if err != nil {
			switch {
			case assistant.Content != "" || assistant.Reasoning != "":
				// Preserve already-visible output on any stream failure, but
				// never persist unexecuted calls without matching tool results.
				assistant.ToolCalls = nil
				assistant.Responses = nil
				assistant.Anthropic = nil
				s.appendHistory(assistant)
			case !errors.Is(err, ErrContentFiltered) && round == 0 && assistant.Content == "" && assistant.Reasoning == "" && len(assistant.ToolCalls) == 0:
				// The first round failed with no output at all (both
				// attempts): roll back the user message so the next request
				// does not carry two consecutive user messages.
				s.mu.Lock()
				if n := len(s.history); n > 0 && s.history[n-1].Role == "user" {
					s.history = s.history[:n-1]
				}
				s.mu.Unlock()
			}
			return "", err
		}
		total.InputTokens += usage.InputTokens
		total.OutputTokens += usage.OutputTokens

		if len(assistant.ToolCalls) == 0 {
			s.appendHistory(assistant)
			if (cfg.MaxTurns == 0 || round+1 < cfg.MaxTurns) && injectSteering() {
				continue
			}
			emit(Event{Type: EventDone, Text: assistant.Content, Reasoning: assistant.Reasoning, Usage: total})
			return assistant.Content, nil
		}

		toolMsgs := make([]Message, 0, len(assistant.ToolCalls))
		for i := range assistant.ToolCalls {
			tc := &assistant.ToolCalls[i]
			args := toolArgs(tc.Arguments)
			tool := s.findTool(tc.Name)
			if sm, ok := tool.(Summarizer); ok && json.Valid(args) {
				tc.Summary = sm.Summary(args)
			}
			emit(Event{Type: EventToolStart, ToolID: tc.ID, ToolName: tc.Name, ToolArgs: args, Summary: tc.Summary})
			result, isErr := s.executeTool(ctx, tool, *tc, args)
			tc.IsError = isErr
			result = truncateToolOutput(result)
			if d, ok := tool.(Differ); ok && !isErr {
				tc.Diff = d.LastDiff()
			}
			emit(Event{Type: EventToolEnd, ToolID: tc.ID, ToolName: tc.Name, ToolResult: result, IsError: isErr, Diff: tc.Diff})
			if tc.Name == "todo_write" && !isErr {
				if items := todoItemsArg(args); items != "" {
					emit(Event{Type: EventTodo, ToolResult: items})
				}
			}
			toolMsgs = append(toolMsgs, Message{Role: "tool", ToolCallID: tc.ID, Content: result})
		}
		// Append the assistant message together with all its tool results so
		// an aborted round never leaves dangling tool_calls in the history.
		s.appendHistory(append([]Message{assistant}, toolMsgs...)...)
		if cfg.MaxTurns == 0 || round+1 < cfg.MaxTurns {
			injectSteering()
		}
	}
	return "", fmt.Errorf("agent: model did not finish within MaxTurns limit (%d)", s.cfg.MaxTurns)
}

// appendHistory appends messages under the session lock.
func (s *Session) appendHistory(msgs ...Message) {
	s.mu.Lock()
	s.history = append(s.history, cloneMessages(msgs)...)
	s.mu.Unlock()
}

// findTool looks up a configured tool by name (nil when unknown).
func (s *Session) findTool(name string) Tool {
	for _, t := range s.cfg.Tools {
		if t.Name() == name {
			return t
		}
	}
	return nil
}

// executeTool runs one model-requested tool call, mapping every failure —
// unknown tool, malformed arguments, denied approval, Execute error — to an
// error text result so the model can see what went wrong and recover.
func (s *Session) executeTool(ctx context.Context, tool Tool, tc ToolCall, args json.RawMessage) (result string, isErr bool) {
	// Keep the tool/result pair valid even if an implementation panics.
	defer func() {
		if failure := recover(); failure != nil {
			result, isErr = fmt.Sprintf("error: internal tool failure: %v", failure), true
		}
	}()
	if err := ctx.Err(); err != nil {
		return "error: " + err.Error(), true
	}
	if tool == nil {
		return fmt.Sprintf("error: unknown tool %q", tc.Name), true
	}
	if !json.Valid(args) {
		return fmt.Sprintf("error: malformed arguments JSON: %s", tc.Arguments), true
	}
	if err := s.toolArguments.Validate(tool, args); err != nil {
		return fmt.Sprintf("error: invalid arguments for tool %q: %v", tc.Name, err), true
	}
	if s.cfg.Approve != nil {
		ask := s.cfg.ApproveAll != nil && s.cfg.ApproveAll()
		if !ask {
			d, ok := tool.(DangerousTool)
			ask = ok && d.Dangerous()
		}
		if ask {
			allow, err := s.cfg.Approve(tc, args)
			if err != nil {
				return "error: " + err.Error(), true
			}
			if !allow {
				return "error: denied by user", true
			}
		}
	}
	// Approval may complete concurrently with a stop/reset. Recheck before
	// handing control to a tool, including tools that do not inspect ctx.
	if err := ctx.Err(); err != nil {
		return "error: " + err.Error(), true
	}
	out, err := tool.Execute(ctx, args)
	if err != nil {
		return "error: " + err.Error(), true
	}
	return out, false
}

// todoItemsArg extracts the raw items array from todo_write arguments;
// "" when absent or malformed.
func todoItemsArg(args json.RawMessage) string {
	var parsed struct {
		Items json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(args, &parsed); err != nil || len(parsed.Items) == 0 {
		return ""
	}
	return string(parsed.Items)
}

// Tool-result size policy: results fed back to the model (and persisted in
// history) are capped so one noisy command cannot blow up the context.
const (
	toolOutputMax  = 12000 // results longer than this are head/tail-truncated
	toolOutputHead = 6000
	toolOutputTail = 4000
)

// truncateToolOutput caps a tool result at toolOutputMax bytes, keeping the
// head and tail and replacing the middle with a marker. Rune boundaries are
// preserved. Valid tool images have a separate bounded envelope and stay intact.
func truncateToolOutput(s string) string {
	if _, ok := toolPreviewImage(s); ok {
		return s
	}
	if len(s) > toolOutputMax {
		if result, ok := mapShellStreams(s, func(stream string) string {
			return truncateOutputText(stream, 5000, 3000, 1800)
		}); ok {
			return result
		}
	}
	return truncateOutputText(s, toolOutputMax, toolOutputHead, toolOutputTail)
}

// Preserve shell stream boundaries during model-context reduction. The same
// stored result is replayed to the UI, which needs the complete envelope.
func mapShellStreams(s string, transform func(string) string) (string, bool) {
	prefix := ""
	for _, cause := range []string{"context canceled", "context deadline exceeded", "error: context canceled", "error: context deadline exceeded"} {
		candidate := cause + "; partial output:\n"
		if strings.HasPrefix(s, candidate+"exit code: unknown (interrupted)\n<stdout>\n") {
			prefix = candidate
			s = strings.TrimPrefix(s, candidate)
			break
		}
	}
	status, body, ok := strings.Cut(s, "\n<stdout>\n")
	if !ok || !strings.HasPrefix(status, "exit code: ") || strings.Contains(status, "\n") {
		return "", false
	}
	stdout, rest, ok := strings.Cut(body, "\n</stdout>\n<stderr>\n")
	if !ok || !strings.HasSuffix(rest, "\n</stderr>") {
		return "", false
	}
	stderr := strings.TrimSuffix(rest, "\n</stderr>")
	return prefix + status + "\n<stdout>\n" + transform(stdout) + "\n</stdout>\n<stderr>\n" + transform(stderr) + "\n</stderr>", true
}

func truncateOutputText(s string, limit, head, tail int) string {
	if len(s) <= limit {
		return s
	}
	for head < len(s) && !utf8.RuneStart(s[head]) {
		head++
	}
	tailStart := len(s) - tail
	for tailStart > head && !utf8.RuneStart(s[tailStart]) {
		tailStart++
	}
	omitted := utf8.RuneCountInString(s[head:tailStart])
	return fmt.Sprintf("%s\n...[truncated %d chars]...\n%s", s[:head], omitted, s[tailStart:])
}

// History compaction: old tool outputs are slimmed before each model round
// (Claude Code /compact style, automatic). Only messages older than the most
// recent compactKeepRecent are touched; assistant text is never rewritten.
const (
	compactKeepRecent = 6
	compactMaxLen     = 2000
	compactHeadLen    = 800
	compactMarker     = "\n...[earlier output truncated]"
)

// compactToolOutputs rewrites large tool outputs deep in the history to
// their head plus a marker, shrinking every subsequent model round.
func (s *Session) compactToolOutputs() {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := len(s.history) - compactKeepRecent
	for i := range max(cutoff, 0) {
		m := &s.history[i]
		if m.Role != "tool" || len(m.Content) <= compactMaxLen || strings.HasSuffix(m.Content, compactMarker) {
			continue
		}
		compact := func(text string) string {
			if len(text) <= compactHeadLen {
				return text
			}
			head := compactHeadLen
			for head < len(text) && !utf8.RuneStart(text[head]) {
				head++
			}
			return text[:head] + compactMarker
		}
		if result, ok := mapShellStreams(m.Content, compact); ok {
			m.Content = result
		} else {
			m.Content = compact(m.Content)
		}
	}
}

// toolArgs normalizes a model-produced arguments string: empty/whitespace
// becomes an empty object.
func toolArgs(raw string) json.RawMessage {
	if strings.TrimSpace(raw) == "" {
		return json.RawMessage("{}")
	}
	return json.RawMessage(raw)
}

// Package agent is a minimal LLM agent loop for rdev, distilled from
// github.com/smallnest/pigo (agent SDK + agentcore loop + OpenAI provider),
// keeping only what rdev needs: an OpenAI-compatible streaming chat endpoint
// with tool calling, a multi-turn tool-execution loop, and structured events.
//
// A Session holds one conversation (system prompt + history + tools). Run
// appends a user message, then loops: stream the model, forward text deltas,
// execute requested tool calls against the device, feed results back, until
// the model stops calling tools or MaxTurns is reached.
package agent

import (
	"context"
	"encoding/json"
	"net/http"
)

// EventType identifies Session.Run stream events.
type EventType string

const (
	EventTextDelta EventType = "text_delta" // Text holds an assistant text chunk
	EventReasoning EventType = "reasoning"  // Text holds a reasoning (thinking) chunk
	EventToolStart EventType = "tool_start" // ToolID/ToolName/ToolArgs/Summary set
	EventToolEnd   EventType = "tool_end"   // ToolID/ToolName/ToolResult/IsError/Diff set
	EventTodo      EventType = "todo"       // ToolResult holds the todo items JSON array
	EventDone      EventType = "done"       // Text holds final assistant text; Reasoning/Usage set
)

// Event is one streamed unit of agent progress.
type Event struct {
	Type       EventType
	Text       string          // delta (text_delta/reasoning) or full final text (done)
	Reasoning  string          // done: full reasoning text accumulated over the turn
	ToolID     string          // tool_start / tool_end
	ToolName   string          // tool_start / tool_end
	ToolArgs   json.RawMessage // tool_start: arguments JSON
	ToolResult string          // tool_end: result text (or error text when IsError); todo: items JSON array
	Summary    string          // tool_start: one-line summary of the call (empty when the tool has none)
	Diff       string          // tool_end: unified diff of the change (edit-style tools only)
	IsError    bool            // tool_end: execution failed
	Usage      Usage           // done
}

// Usage reports token accounting for a completed turn.
type Usage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
}

// Tool is a capability the model can invoke. Execute receives the raw
// arguments JSON and returns the text result fed back to the model; a non-nil
// error is reported to the model as the (failed) tool result, so tools should
// return errors for expected failures (bad path, command exit != 0 is NOT an
// error — report it in the result text) and reserve Go errors for exceptional
// cases.
type Tool interface {
	Name() string
	Description() string
	// Schema returns the JSON Schema object for the tool's parameters.
	Schema() json.RawMessage
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}

// DangerousTool is an optional Tool interface: when it reports true, the
// session asks Config.Approve for confirmation before executing the call.
type DangerousTool interface {
	Dangerous() bool
}

// Summarizer is an optional Tool interface producing a one-line human
// summary of a call (carried on EventToolStart.Summary).
type Summarizer interface {
	Summary(args json.RawMessage) string
}

// Differ is an optional Tool interface exposing the unified diff of the last
// executed change (carried on EventToolEnd.Diff; empty when nothing changed).
type Differ interface {
	LastDiff() string
}

// Config configures a Session.
type Config struct {
	BaseURL      string // OpenAI-compatible endpoint root, e.g. "https://api.openai.com/v1"
	APIKey       string
	Model        string
	SystemPrompt string
	Tools        []Tool
	// MaxTurns caps model round-trips per Run (each round may include tool
	// calls). 0 means DefaultMaxTurns.
	MaxTurns int
	// ReasoningEffort is the model thinking level sent as the
	// reasoning_effort request field ("low" | "medium" | "high"). "" and
	// "off" omit the field entirely.
	ReasoningEffort string
	// Approve is called before executing any tool that implements
	// DangerousTool and reports true. allow=false feeds "error: denied by
	// user" back to the model as the tool result (the loop continues).
	// Nil Approve executes dangerous tools without confirmation.
	Approve func(tc ToolCall, args json.RawMessage) (allow bool, err error)
	// ApproveAll, when non-nil and reporting true, routes every tool call
	// (not only DangerousTool ones) through Approve — the "ask everything"
	// permission mode. Evaluated per call so mode switches apply to
	// existing sessions.
	ApproveAll func() bool
	// HTTPClient is optional; nil uses a default client with no total timeout
	// (streaming relies on ctx for cancellation).
	HTTPClient *http.Client
}

const DefaultMaxTurns = 25

// Message is one conversation entry in OpenAI chat-completions wire shape.
type Message struct {
	Role       string     `json:"role"` // "system" | "user" | "assistant" | "tool"
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"` // role == "tool"
	// Reasoning holds the model's thinking text for assistant messages. It
	// is never sent back to the provider (DeepSeek rejects it); it exists
	// only for display and history persistence.
	Reasoning string `json:"-"`
	// Images are user-message attachments (screenshots, photos). When
	// non-empty, MarshalJSON emits content as a text+image_url parts array.
	// Only Name survives history persistence; Mime/Data live for one turn.
	Images []MessageImage `json:"-"`
}

// MessageImage is one image attached to a user message.
type MessageImage struct {
	Name string `json:"name,omitempty"`
	Mime string `json:"mime,omitempty"`
	Data string `json:"data,omitempty"` // base64
}

// messageContentPart is one element of the multi-modal content array.
type messageContentPart struct {
	Type     string               `json:"type"`
	Text     string               `json:"text,omitempty"`
	ImageURL *messageImageURLPart `json:"image_url,omitempty"`
}

type messageImageURLPart struct {
	URL string `json:"url"`
}

// MarshalJSON emits the OpenAI wire shape: a plain string content normally,
// or a parts array when the message carries images. Images without data
// (history reloads keep only names) are skipped, so a reloaded conversation
// degrades to plain text instead of sending empty data URLs.
func (m Message) MarshalJSON() ([]byte, error) {
	withData := 0
	for _, img := range m.Images {
		if img.Data != "" {
			withData++
		}
	}
	if withData == 0 {
		type plainMessage Message
		return json.Marshal(plainMessage(m))
	}
	parts := make([]messageContentPart, 0, withData+1)
	if m.Content != "" {
		parts = append(parts, messageContentPart{Type: "text", Text: m.Content})
	}
	for _, img := range m.Images {
		if img.Data == "" {
			continue
		}
		parts = append(parts, messageContentPart{
			Type:     "image_url",
			ImageURL: &messageImageURLPart{URL: "data:" + img.Mime + ";base64," + img.Data},
		})
	}
	return json.Marshal(struct {
		Role       string               `json:"role"`
		Content    []messageContentPart `json:"content"`
		ToolCalls  []ToolCall           `json:"tool_calls,omitempty"`
		ToolCallID string               `json:"tool_call_id,omitempty"`
	}{
		Role:       m.Role,
		Content:    parts,
		ToolCalls:  m.ToolCalls,
		ToolCallID: m.ToolCallID,
	})
}

// ToolCall is one model-requested tool invocation.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // raw JSON text as produced by the model
	// Summary and Diff are display metadata filled in by the session while
	// executing the call; they never go back to the provider.
	Summary string `json:"-"`
	Diff    string `json:"-"`
}

// toolCallWire is the OpenAI chat-completions request shape for a tool call:
// {"id": ..., "type": "function", "function": {"name": ..., "arguments": ...}}.
// Strict providers (DeepSeek) reject the bare {id,name,arguments} form.
type toolCallWire struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// MarshalJSON emits the OpenAI wire shape.
func (tc ToolCall) MarshalJSON() ([]byte, error) {
	w := toolCallWire{ID: tc.ID, Type: "function"}
	w.Function.Name = tc.Name
	w.Function.Arguments = tc.Arguments
	return json.Marshal(w)
}

// New creates a Session. It performs no network I/O.
func New(cfg Config) *Session {
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = DefaultMaxTurns
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{}
	}
	return &Session{cfg: cfg}
}

// Reset clears the conversation history (system prompt and tools are kept).
func (s *Session) Reset() {
	s.mu.Lock()
	s.history = nil
	s.mu.Unlock()
}

// History returns a copy of the conversation so far (excluding the system
// prompt), for display or persistence.
func (s *Session) History() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Message, len(s.history))
	copy(out, s.history)
	return out
}

// SetHistory replaces the conversation history with a copy of h, e.g. to
// carry a conversation over when the session is rebuilt for a model switch.
func (s *Session) SetHistory(h []Message) {
	s.mu.Lock()
	s.history = make([]Message, len(h))
	copy(s.history, h)
	s.mu.Unlock()
}

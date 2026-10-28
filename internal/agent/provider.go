// provider.go is the OpenAI-compatible streaming chat-completions client,
// distilled from pigo's internal/provider/openai.go + transport.go down to the
// single wire format rdev needs: POST {BaseURL}/chat/completions with
// server-sent events, tool calls, and usage reporting.
package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// streamIdleTimeout caps how long a streaming chat request may go without
// any SSE payload (connecting, waiting for the first byte, or mid-stream
// stalls on a half-open connection). On expiry the request context is
// canceled so the agent turn fails instead of hanging forever. It is a var
// only so tests can shrink it.
var streamIdleTimeout = 120 * time.Second

// errorBodyLimit caps how many bytes of a non-2xx response body are read into
// the returned error message.
const errorBodyLimit = 4096

// chatRequest is the OpenAI chat-completions request body.
type chatRequest struct {
	Model           string         `json:"model"`
	Messages        []Message      `json:"messages"`
	Tools           []toolSpec     `json:"tools,omitempty"`
	Stream          bool           `json:"stream"`
	StreamOptions   *streamOptions `json:"stream_options,omitempty"`
	ReasoningEffort string         `json:"reasoning_effort,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type toolSpec struct {
	Type     string       `json:"type"` // always "function"
	Function functionSpec `json:"function"`
}

type functionSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// streamChunk is the streamed chat.completion.chunk envelope. Usage arrives on
// a final choices-empty chunk when stream_options.include_usage is set.
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string          `json:"content"`
			ReasoningContent string          `json:"reasoning_content"`
			ToolCalls        []toolCallDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	// Some gateways surface an error object inline on the stream.
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// toolCallDelta is one streamed tool-call fragment: id and name arrive once
// (usually in the first fragment); arguments accumulate across fragments.
type toolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// pendingToolCall accumulates one streamed tool call, keyed by delta index.
type pendingToolCall struct {
	id   string
	name string
	args strings.Builder
}

// streamChat performs one streaming chat-completions round against the
// configured endpoint. It sends history (with a leading system message when
// cfg.SystemPrompt is set) plus the configured tool definitions, forwards each
// assistant content delta to onDelta and each reasoning delta to onReasoning
// (both may be nil), and returns the assembled assistant message and the usage
// reported by the server. ctx cancels the in-flight request.
//
// A failed round is retried once when it produced no output at all (no
// content, reasoning, or tool-call delta): such failures are almost always
// transient connection/setup errors and nothing was emitted downstream yet.
func streamChat(ctx context.Context, cfg Config, history []Message, onDelta, onReasoning func(string)) (Message, Usage, error) {
	payload, err := marshalChatRequest(cfg, history)
	if err != nil {
		return Message{}, Usage{}, err
	}
	assistant, usage, progress, err := streamChatOnce(ctx, cfg, payload, onDelta, onReasoning)
	if err != nil && !progress && ctx.Err() == nil {
		assistant, usage, _, err = streamChatOnce(ctx, cfg, payload, onDelta, onReasoning)
	}
	return assistant, usage, err
}

// marshalChatRequest encodes the chat-completions request body for one round.
func marshalChatRequest(cfg Config, history []Message) ([]byte, error) {
	msgs := history
	if cfg.SystemPrompt != "" {
		msgs = make([]Message, 0, len(history)+1)
		msgs = append(msgs, Message{Role: "system", Content: cfg.SystemPrompt})
		msgs = append(msgs, history...)
	}
	reqBody := chatRequest{
		Model:         cfg.Model,
		Messages:      msgs,
		Stream:        true,
		StreamOptions: &streamOptions{IncludeUsage: true},
	}
	// "off" (and "") mean "do not set": the field is omitted entirely.
	if cfg.ReasoningEffort != "" && cfg.ReasoningEffort != "off" {
		reqBody.ReasoningEffort = cfg.ReasoningEffort
	}
	if len(cfg.Tools) > 0 {
		reqBody.Tools = make([]toolSpec, len(cfg.Tools))
		for i, t := range cfg.Tools {
			reqBody.Tools[i] = toolSpec{
				Type: "function",
				Function: functionSpec{
					Name:        t.Name(),
					Description: t.Description(),
					Parameters:  t.Schema(),
				},
			}
		}
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("agent: encode chat request: %w", err)
	}
	return payload, nil
}

// streamChatOnce performs a single streaming attempt for an already-encoded
// request body. progress reports whether any content, reasoning, or tool-call
// delta was observed before the result (or error), so the caller can safely
// retry error rounds that emitted nothing.
func streamChatOnce(ctx context.Context, cfg Config, payload []byte, onDelta, onReasoning func(string)) (assistant Message, usage Usage, progress bool, err error) {
	// Idle watchdog: any stall longer than streamIdleTimeout (no response
	// headers or no SSE payload) cancels the request so a wedged gateway
	// cannot hang the agent turn forever. Reset on every payload line.
	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()
	watchdog := time.AfterFunc(streamIdleTimeout, cancelStream)
	defer watchdog.Stop()

	url := strings.TrimRight(cfg.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(streamCtx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return Message{}, Usage{}, false, fmt.Errorf("agent: build chat request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

	resp, err := cfg.HTTPClient.Do(req)
	if err != nil {
		return Message{}, Usage{}, false, fmt.Errorf("agent: chat request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
		return Message{}, Usage{}, false, fmt.Errorf("agent: chat request failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	assistant, usage, err = readStream(resp.Body, onDelta, onReasoning, func() { watchdog.Reset(streamIdleTimeout) })
	progress = assistant.Content != "" || assistant.Reasoning != "" || len(assistant.ToolCalls) > 0
	return assistant, usage, progress, err
}

// readStream consumes one SSE body: lines of `data: {chunk}` terminated by
// `data: [DONE]` (or EOF), and assembles the assistant message from the
// content, reasoning and tool-call deltas. poke (may be nil) is called on
// every payload line to signal stream liveness. On error the partially
// assembled message is still returned so the caller can tell whether any
// progress was made.
func readStream(body io.Reader, onDelta, onReasoning func(string), poke func()) (Message, Usage, error) {
	var (
		content   strings.Builder
		reasoning strings.Builder
		calls     = make(map[int]*pendingToolCall)
		callOrder []int // delta indices in first-seen order
		usage     Usage
	)
	assemble := func() Message {
		assistant := Message{Role: "assistant", Content: content.String(), Reasoning: reasoning.String()}
		if len(callOrder) > 0 {
			sort.Ints(callOrder)
			assistant.ToolCalls = make([]ToolCall, 0, len(callOrder))
			for _, idx := range callOrder {
				c := calls[idx]
				assistant.ToolCalls = append(assistant.ToolCalls, ToolCall{ID: c.id, Name: c.name, Arguments: c.args.String()})
			}
		}
		return assistant
	}

	reader := bufio.NewReader(body)
	for {
		line, err := reader.ReadBytes('\n')
		if payload, ok := ssePayload(line); ok {
			if poke != nil {
				poke()
			}
			if bytes.Equal(payload, []byte("[DONE]")) {
				break
			}
			var chunk streamChunk
			if jerr := json.Unmarshal(payload, &chunk); jerr != nil {
				return assemble(), usage, fmt.Errorf("agent: parse stream chunk: %w", jerr)
			}
			if chunk.Error != nil {
				msg := "agent: stream error"
				if chunk.Error.Type != "" {
					msg += " (" + chunk.Error.Type + ")"
				}
				if chunk.Error.Message != "" {
					msg += ": " + chunk.Error.Message
				}
				return assemble(), usage, fmt.Errorf("%s", msg)
			}
			if chunk.Usage != nil {
				usage.InputTokens = chunk.Usage.PromptTokens
				usage.OutputTokens = chunk.Usage.CompletionTokens
			}
			for _, choice := range chunk.Choices {
				if choice.Delta.ReasoningContent != "" {
					reasoning.WriteString(choice.Delta.ReasoningContent)
					if onReasoning != nil {
						onReasoning(choice.Delta.ReasoningContent)
					}
				}
				if choice.Delta.Content != "" {
					content.WriteString(choice.Delta.Content)
					if onDelta != nil {
						onDelta(choice.Delta.Content)
					}
				}
				for _, tc := range choice.Delta.ToolCalls {
					call := calls[tc.Index]
					if call == nil {
						call = &pendingToolCall{}
						calls[tc.Index] = call
						callOrder = append(callOrder, tc.Index)
					}
					if tc.ID != "" {
						call.id = tc.ID
					}
					if tc.Function.Name != "" {
						call.name = tc.Function.Name
					}
					call.args.WriteString(tc.Function.Arguments)
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return assemble(), usage, fmt.Errorf("agent: read stream: %w", err)
		}
	}

	return assemble(), usage, nil
}

// ssePayload extracts the payload of one SSE `data:` line, tolerating `\r\n`
// endings and both `data:` and `data: ` forms. ok is false for blank lines,
// comments, and other SSE fields.
func ssePayload(line []byte) (payload []byte, ok bool) {
	line = bytes.TrimRight(line, "\r\n")
	if !bytes.HasPrefix(line, []byte("data:")) {
		return nil, false
	}
	return bytes.TrimSpace(line[len("data:"):]), true
}

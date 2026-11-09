// provider.go contains the shared HTTP transport and chat-completions adapter.
// Responses serialization/stream events live in provider_responses.go.
package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
			Reasoning        string          `json:"reasoning"`
			ReasoningText    string          `json:"reasoning_text"`
			Refusal          string          `json:"refusal"`
			ToolCalls        []toolCallDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason       string          `json:"finish_reason"`
		NativeFinishReason json.RawMessage `json:"native_finish_reason"`
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
// A retryable failed round is retried once only before any content, reasoning,
// or tool-call delta. Permanent HTTP errors, filtering and output limits are
// terminal; retries never duplicate already-emitted content or device calls.
func streamChat(ctx context.Context, cfg Config, history []Message, onDelta, onReasoning func(string)) (Message, Usage, error) {
	var payload []byte
	var err error
	switch cfg.API {
	case "", "openai-completions":
		payload, err = marshalChatRequest(cfg, history)
	case "openai-responses":
		payload, err = marshalResponsesRequest(cfg, history)
	case "anthropic-messages":
		payload, err = marshalAnthropicRequest(cfg, history)
	default:
		return Message{}, Usage{}, fmt.Errorf("agent: unsupported provider API %q", cfg.API)
	}
	if err != nil {
		return Message{}, Usage{}, err
	}
	assistant, usage, progress, err := streamChatOnce(ctx, cfg, payload, onDelta, onReasoning)
	if err != nil && !progress && ctx.Err() == nil && retryEmptyProviderResponse(err) {
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
		Messages:      providerMessages(msgs),
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

	endpoint := "/chat/completions"
	if cfg.API == "openai-responses" {
		endpoint = "/responses"
	} else if cfg.API == "anthropic-messages" {
		endpoint = "/messages"
	}
	url := strings.TrimRight(cfg.BaseURL, "/") + endpoint
	req, err := http.NewRequestWithContext(streamCtx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return Message{}, Usage{}, false, fmt.Errorf("agent: build chat request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if cfg.API == "anthropic-messages" {
		req.Header.Set("x-api-key", cfg.APIKey)
		req.Header.Set("anthropic-version", "2023-06-01")
		if cfg.Thinking.Mode == "anthropic-adaptive" && cfg.ReasoningEffort != "" {
			req.Header.Set("anthropic-beta", "effort-2025-11-24")
		}
	} else {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}

	resp, err := cfg.HTTPClient.Do(req)
	if err != nil {
		return Message{}, Usage{}, false, fmt.Errorf("agent: chat request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
		return Message{}, Usage{}, false, &providerHTTPError{status: resp.StatusCode, detail: strings.TrimSpace(string(body))}
	}

	if cfg.API == "openai-responses" {
		assistant, usage, err = readResponsesStream(resp.Body, providerReplayScope(cfg), onDelta, onReasoning, func() { watchdog.Reset(streamIdleTimeout) })
	} else if cfg.API == "anthropic-messages" {
		assistant, usage, err = readAnthropicStream(resp.Body, providerReplayScope(cfg), onDelta, onReasoning, func() { watchdog.Reset(streamIdleTimeout) })
	} else {
		var finishTimer *time.Timer
		defer func() {
			if finishTimer != nil {
				finishTimer.Stop()
			}
		}()
		assistant, usage, err = readStreamWithFinish(resp.Body, onDelta, onReasoning, func() { watchdog.Reset(streamIdleTimeout) }, func() {
			if finishTimer == nil {
				finishTimer = time.AfterFunc(2500*time.Millisecond, func() { _ = resp.Body.Close() })
			}
		})
	}
	progress = assistant.Anthropic != nil || assistant.Responses != nil || assistant.Content != "" || assistant.Reasoning != "" || len(assistant.ToolCalls) > 0
	return assistant, usage, progress, err
}

// readStream consumes one SSE body: lines of `data: {chunk}` terminated by
// `data: [DONE]` (or EOF), and assembles the assistant message from the
// content, reasoning and tool-call deltas. poke (may be nil) is called on
// every payload line to signal stream liveness. On error the partially
// assembled message is still returned so the caller can tell whether any
// progress was made.
func readStream(body io.Reader, onDelta, onReasoning func(string), poke func()) (Message, Usage, error) {
	return readStreamWithFinish(body, onDelta, onReasoning, poke, nil)
}

func readStreamWithFinish(body io.Reader, onDelta, onReasoning func(string), poke, onFinish func()) (Message, Usage, error) {
	var (
		content      strings.Builder
		reasoning    strings.Builder
		calls        = make(map[int]*pendingToolCall)
		callOrder    []int // delta indices in first-seen order
		usage        Usage
		sawDone      bool   // the stream ended with `data: [DONE]`
		finish       string // last observed finish_reason
		terminalErr  error
		terminalSeen bool
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
		// Any non-empty line (including SSE comment keepalives like
		// ": ping") proves the stream is alive; poke before the data: check
		// or comment-only gateways would starve the watchdog.
		if poke != nil && len(bytes.TrimSpace(line)) > 0 {
			poke()
		}
		if payload, ok := ssePayload(line); ok {
			if bytes.Equal(payload, []byte("[DONE]")) {
				sawDone = true
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
				var nativeReason string
				_ = json.Unmarshal(choice.NativeFinishReason, &nativeReason)
				if native := nativeFinishReasonError(nativeReason); native != nil && terminalErr == nil {
					terminalErr = native
					terminalSeen = true
				}
				if choice.FinishReason != "" {
					finish = choice.FinishReason
					terminalSeen = true
					if terminalErr == nil {
						terminalErr = finishReasonError(finish)
					}
				}
				if terminalSeen && onFinish != nil {
					onFinish()
				}
				if choice.Delta.ReasoningContent == "" {
					choice.Delta.ReasoningContent = choice.Delta.Reasoning
				}
				if choice.Delta.ReasoningContent == "" {
					choice.Delta.ReasoningContent = choice.Delta.ReasoningText
				}
				if choice.Delta.Refusal != "" {
					choice.Delta.Content += choice.Delta.Refusal
				}
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
			if err == io.EOF || terminalSeen {
				break
			}
			return assemble(), usage, fmt.Errorf("agent: read stream: %w", err)
		}
	}

	assistant := assemble()
	if terminalErr != nil {
		return assistant, usage, terminalErr
	}

	switch {
	case !sawDone && !terminalSeen:
		// Connection dropped mid-stream with no terminal signal at all.
		return assistant, usage, errors.New("agent: stream truncated (EOF without [DONE])")
	case sawDone && assistant.Content == "" && assistant.Reasoning == "" && len(assistant.ToolCalls) == 0 &&
		usage.InputTokens == 0 && usage.OutputTokens == 0:
		// A well-formed but empty stream: nothing was emitted downstream,
		// so the caller's no-progress retry applies.
		return assistant, usage, errors.New("agent: empty response stream")
	}
	return assistant, usage, nil
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

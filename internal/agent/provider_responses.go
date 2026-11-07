package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// ResponseReplay retains opaque reasoning and assistant phase metadata for
// stateless continuation. It is persisted, but never exposed as UI/model text.
type ResponseReplay struct {
	Scope string            `json:"scope"`
	Items []json.RawMessage `json:"items"`
}

func responseScope(cfg Config) string {
	scope, _ := json.Marshal([]string{cfg.ProviderID, strings.TrimRight(cfg.BaseURL, "/"), cfg.Model})
	digest := sha256.Sum256(scope)
	return hex.EncodeToString(digest[:])
}

func marshalResponsesRequest(cfg Config, history []Message) ([]byte, error) {
	input := make([]any, 0, len(history))
	scope := responseScope(cfg)
	for _, m := range providerMessages(history) {
		if m.Role == "assistant" && m.Responses != nil && m.Responses.Scope == scope && len(m.Responses.Items) > 0 {
			for _, item := range m.Responses.Items {
				input = append(input, item)
			}
			continue
		}
		if m.Role == "tool" {
			input = append(input, map[string]any{"type": "function_call_output", "call_id": m.ToolCallID, "output": m.Content})
			continue
		}
		if m.Role == "assistant" {
			if m.Content != "" {
				input = append(input, map[string]any{"role": "assistant", "content": m.Content})
			}
		} else if m.Content != "" || len(m.Images) > 0 {
			role := m.Role
			if role == "system" {
				role = "developer"
			}
			parts := make([]any, 0, len(m.Images)+1)
			if m.Content != "" {
				parts = append(parts, map[string]any{"type": "input_text", "text": m.Content})
			}
			for _, img := range m.Images {
				if img.Data != "" {
					parts = append(parts, map[string]any{"type": "input_image", "image_url": "data:" + img.Mime + ";base64," + img.Data})
				}
			}
			if len(parts) > 0 {
				input = append(input, map[string]any{"role": role, "content": parts})
			}
		}
		for _, call := range m.ToolCalls {
			input = append(input, map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Name, "arguments": call.Arguments})
		}
	}
	request := map[string]any{"model": cfg.Model, "input": input, "stream": true, "store": false, "include": []string{"reasoning.encrypted_content"}}
	if cfg.SystemPrompt != "" {
		request["instructions"] = cfg.SystemPrompt
	}
	if cfg.ReasoningEffort != "" && cfg.ReasoningEffort != "off" {
		request["reasoning"] = map[string]string{"effort": cfg.ReasoningEffort, "summary": "auto"}
	}
	if len(cfg.Tools) > 0 {
		tools := make([]any, 0, len(cfg.Tools))
		for _, t := range cfg.Tools {
			tools = append(tools, map[string]any{"type": "function", "name": t.Name(), "description": t.Description(), "parameters": t.Schema(), "strict": false})
		}
		request["tools"] = tools
	}
	return json.Marshal(request)
}

type responseItem struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Content   []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content"`
	Summary []struct {
		Text string `json:"text"`
	} `json:"summary"`
}

type responseEvent struct {
	Type        string          `json:"type"`
	OutputIndex int             `json:"output_index"`
	Delta       string          `json:"delta"`
	Arguments   string          `json:"arguments"`
	Item        json.RawMessage `json:"item"`
	Message     string          `json:"message"`
	Response    struct {
		Status string            `json:"status"`
		Output []json.RawMessage `json:"output"`
		Usage  struct {
			Input  int `json:"input_tokens"`
			Output int `json:"output_tokens"`
		} `json:"usage"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Incomplete struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
	} `json:"response"`
}

// readResponsesStream consumes semantic SSE events. Only a completed response
// grants replayable output; EOF/incomplete/error returns readable partial text.
func readResponsesStream(body io.Reader, scope string, onText, onReasoning func(string), poke func()) (assistant Message, usage Usage, err error) {
	assistant.Role = "assistant"
	pending := map[int]*ToolCall{}
	appendText := func(delta string) {
		assistant.Content += delta
		if onText != nil && delta != "" {
			onText(delta)
		}
	}
	appendReasoning := func(delta string) {
		assistant.Reasoning += delta
		if onReasoning != nil && delta != "" {
			onReasoning(delta)
		}
	}
	defer func() {
		if err != nil {
			// Preserve progress for retry classification, not executable tool calls.
			keys := make([]int, 0, len(pending))
			for key := range pending {
				keys = append(keys, key)
			}
			sort.Ints(keys)
			for _, key := range keys {
				assistant.ToolCalls = append(assistant.ToolCalls, *pending[key])
			}
		}
	}()
	process := func(payload string) (bool, error) {
		if payload == "[DONE]" {
			return false, fmt.Errorf("agent: Responses stream ended without response.completed")
		}
		var event responseEvent
		if e := json.Unmarshal([]byte(payload), &event); e != nil {
			return false, fmt.Errorf("agent: decode Responses event: %w", e)
		}
		switch event.Type {
		case "response.output_text.delta", "response.refusal.delta":
			appendText(event.Delta)
		case "response.reasoning_summary_text.delta":
			appendReasoning(event.Delta)
		case "response.output_item.added":
			var item responseItem
			if e := json.Unmarshal(event.Item, &item); e != nil {
				return false, e
			}
			assistant.Responses = &ResponseReplay{Scope: scope} // progress even before visible output
			if item.Type == "function_call" {
				pending[event.OutputIndex] = &ToolCall{ID: item.CallID, Name: item.Name, Arguments: item.Arguments}
			}
		case "response.function_call_arguments.delta":
			call := pending[event.OutputIndex]
			if call == nil {
				call = &ToolCall{}
				pending[event.OutputIndex] = call
			}
			call.Arguments += event.Delta
		case "response.function_call_arguments.done":
			if call := pending[event.OutputIndex]; call != nil {
				call.Arguments = event.Arguments
			}
		case "response.failed", "response.incomplete":
			detail := event.Response.Error.Message
			if detail == "" {
				detail = event.Response.Incomplete.Reason
			}
			return false, fmt.Errorf("agent: %s: %s", event.Type, detail)
		case "error":
			return false, fmt.Errorf("agent: Responses error: %s", event.Message)
		case "response.completed":
			if event.Response.Status != "completed" {
				return false, fmt.Errorf("agent: unexpected Responses status %q", event.Response.Status)
			}
			final := Message{Role: "assistant"}
			for _, raw := range event.Response.Output {
				var item responseItem
				if e := json.Unmarshal(raw, &item); e != nil {
					return false, e
				}
				switch item.Type {
				case "message":
					for _, part := range item.Content {
						switch part.Type {
						case "output_text":
							final.Content += part.Text
						case "refusal":
							final.Content += part.Refusal
						default:
							return false, fmt.Errorf("agent: unsupported Responses content %q", part.Type)
						}
					}
				case "reasoning":
					for _, part := range item.Summary {
						final.Reasoning += part.Text
					}
				case "function_call":
					if item.CallID == "" || item.Name == "" || !json.Valid([]byte(item.Arguments)) {
						return false, fmt.Errorf("agent: invalid Responses function call")
					}
					final.ToolCalls = append(final.ToolCalls, ToolCall{ID: item.CallID, Name: item.Name, Arguments: item.Arguments})
				default:
					return false, fmt.Errorf("agent: unsupported Responses output %q", item.Type)
				}
			}
			// A provider may send only final items. Fill missing suffixes, but never
			// silently replace text already streamed to the UI with different text.
			if !strings.HasPrefix(final.Content, assistant.Content) || !strings.HasPrefix(final.Reasoning, assistant.Reasoning) {
				return false, fmt.Errorf("agent: Responses final output differs from streamed output")
			}
			appendText(strings.TrimPrefix(final.Content, assistant.Content))
			appendReasoning(strings.TrimPrefix(final.Reasoning, assistant.Reasoning))
			final.Responses = &ResponseReplay{Scope: scope, Items: event.Response.Output}
			assistant = final
			usage = Usage{InputTokens: event.Response.Usage.Input, OutputTokens: event.Response.Usage.Output}
			return true, nil
		}
		return false, nil
	}
	done, err := readSSEEvents(body, poke, process)
	if err != nil {
		return assistant, usage, err
	}
	if !done {
		return assistant, usage, fmt.Errorf("agent: Responses stream ended before completion")
	}
	return assistant, usage, nil
}

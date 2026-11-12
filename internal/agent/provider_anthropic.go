package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// AnthropicThinkingConfig is explicit model capability metadata, following OMP's
// mode names. New adaptive models must not receive legacy budget requests.
type AnthropicThinkingConfig struct {
	Mode            string `json:"mode,omitempty"`
	BudgetTokens    int    `json:"budgetTokens,omitempty"`
	SupportsDisplay bool   `json:"supportsDisplay,omitempty"`
}

func (c AnthropicThinkingConfig) Validate(maxTokens int) error {
	if maxTokens < 0 {
		return fmt.Errorf("maxTokens must not be negative")
	}
	if maxTokens == 0 {
		maxTokens = 8192
	}
	switch c.Mode {
	case "", "anthropic-adaptive":
		if c.BudgetTokens != 0 {
			return fmt.Errorf("budgetTokens requires anthropic-budget mode")
		}
	case "anthropic-budget":
		if maxTokens < 2048 {
			return fmt.Errorf("budget thinking requires maxTokens >= 2048")
		}
		if c.BudgetTokens != 0 && (c.BudgetTokens < 1024 || c.BudgetTokens > maxTokens-1024) {
			return fmt.Errorf("budgetTokens must be between 1024 and maxTokens - 1024")
		}
	default:
		return fmt.Errorf("unsupported Anthropic thinking mode %q", c.Mode)
	}
	return nil
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content []any  `json:"content"`
}

func marshalAnthropicRequest(cfg Config, history []Message) ([]byte, error) {
	if err := cfg.Thinking.Validate(cfg.MaxOutputTokens); err != nil {
		return nil, err
	}
	maxTokens := cfg.MaxOutputTokens
	if maxTokens == 0 {
		maxTokens = 8192
	}
	messages := make([]anthropicMessage, 0, len(history))
	system := make([]any, 0, 1)
	cacheControl := map[string]string{"type": "ephemeral"}
	if cfg.SystemPrompt != "" {
		system = append(system, map[string]any{"type": "text", "text": cfg.SystemPrompt, "cache_control": cacheControl})
	}
	scope := providerReplayScope(cfg)
	appendBlocks := func(role string, blocks []any) {
		if len(blocks) == 0 {
			return
		}
		if len(messages) > 0 && messages[len(messages)-1].Role == role {
			messages[len(messages)-1].Content = append(messages[len(messages)-1].Content, blocks...)
		} else {
			messages = append(messages, anthropicMessage{Role: role, Content: blocks})
		}
	}
	// Tool errors are display metadata on the owning assistant calls, not on
	// tool messages. Scope the lookup to that turn (IDs can be reused later).
	toolErrors := map[string]bool{}
	for _, m := range history {
		if m.Role == "system" {
			if m.Content != "" {
				system = append(system, map[string]any{"type": "text", "text": m.Content})
			}
			continue
		}
		if m.Role == "tool" {
			var content any = m.Content
			if img, ok := toolPreviewImage(m.Content); ok {
				content = []any{anthropicImage(img)}
			} else if len(m.Images) > 0 {
				blocks := make([]any, 0, len(m.Images)+1)
				if m.Content != "" {
					blocks = append(blocks, map[string]any{"type": "text", "text": strings.ReplaceAll(m.Content, nativeImagePlaceholder, "Image returned by tool (see attached image).")})
				}
				for _, image := range m.Images {
					if image.Data != "" && (image.Mime == "image/png" || image.Mime == "image/jpeg") {
						blocks = append(blocks, anthropicImage(image))
					}
				}
				content = blocks
			}
			appendBlocks("user", []any{map[string]any{"type": "tool_result", "tool_use_id": m.ToolCallID, "content": content, "is_error": toolErrors[m.ToolCallID]}})
			continue
		}
		toolErrors = map[string]bool{}
		if m.Role == "assistant" {
			for _, call := range m.ToolCalls {
				toolErrors[call.ID] = call.IsError
			}
			if m.Anthropic != nil && m.Anthropic.Scope == scope && len(m.Anthropic.Items) > 0 {
				blocks := make([]any, 0, len(m.Anthropic.Items))
				for _, item := range m.Anthropic.Items {
					blocks = append(blocks, item)
				}
				appendBlocks("assistant", blocks)
				continue
			}
		}
		if m.Role != "user" && m.Role != "assistant" {
			return nil, fmt.Errorf("agent: unsupported Anthropic message role %q", m.Role)
		}
		blocks := make([]any, 0, 1+len(m.Images)+len(m.ToolCalls))
		if m.Content != "" {
			blocks = append(blocks, map[string]any{"type": "text", "text": m.Content})
		}
		for _, img := range m.Images {
			if img.Data != "" {
				blocks = append(blocks, anthropicImage(img))
			}
		}
		for _, call := range m.ToolCalls {
			input := json.RawMessage(call.Arguments)
			if !isJSONObject(input) {
				return nil, fmt.Errorf("agent: invalid Anthropic tool input for %q", call.Name)
			}
			blocks = append(blocks, map[string]any{"type": "tool_use", "id": call.ID, "name": call.Name, "input": input})
		}
		appendBlocks(m.Role, blocks)
	}
	// Keep the previous user boundary explicitly reachable even when the new
	// turn adds more than the provider's 20-block cache lookback window.
	// Only request-owned user blocks are marked; signed assistant replay is
	// opaque and must remain byte-for-byte unchanged.
	remaining := 2
	for i := len(messages) - 1; i >= 0 && remaining > 0; i-- {
		if messages[i].Role != "user" {
			continue
		}
		blocks := messages[i].Content
		for j := len(blocks) - 1; j >= 0; j-- {
			block, ok := blocks[j].(map[string]any)
			if !ok {
				continue
			}
			switch block["type"] {
			case "text", "image", "tool_result":
				block["cache_control"] = cacheControl
				remaining--
			default:
				continue
			}
			break
		}
	}
	request := map[string]any{"model": cfg.Model, "messages": messages, "max_tokens": maxTokens, "stream": true}
	if len(system) > 0 {
		request["system"] = system
	}
	if cfg.Thinking.Mode == "anthropic-adaptive" {
		request["thinking"] = map[string]any{"type": "adaptive"}
		effort := cfg.ReasoningEffort
		if effort == "off" {
			// Adaptive-only models cannot disable thinking.
			effort = "low"
		}
		if effort != "" {
			if effort != "low" && effort != "medium" && effort != "high" {
				return nil, fmt.Errorf("agent: unsupported Anthropic effort %q", effort)
			}
			request["output_config"] = map[string]string{"effort": effort}
		}
	} else if cfg.Thinking.Mode == "anthropic-budget" && cfg.ReasoningEffort != "" && cfg.ReasoningEffort != "off" {
		budget := cfg.Thinking.BudgetTokens
		if budget == 0 {
			switch cfg.ReasoningEffort {
			case "low":
				budget = 1024
			case "medium":
				budget = 4096
			case "high":
				budget = 8192
			default:
				return nil, fmt.Errorf("agent: unsupported Anthropic effort %q", cfg.ReasoningEffort)
			}
			budget = min(budget, maxTokens-1024)
		}
		request["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
	}
	if cfg.Thinking.SupportsDisplay {
		if thinking, ok := request["thinking"].(map[string]any); ok {
			thinking["display"] = "summarized"
		}
	}
	if len(cfg.Tools) > 0 {
		tools := make([]any, 0, len(cfg.Tools))
		for _, tool := range cfg.Tools {
			tools = append(tools, map[string]any{"name": tool.Name(), "description": tool.Description(), "input_schema": tool.Schema()})
		}
		tools[len(tools)-1].(map[string]any)["cache_control"] = cacheControl
		request["tools"] = tools
	}
	return json.Marshal(request)
}

func anthropicImage(img MessageImage) any {
	return map[string]any{"type": "image", "source": map[string]string{"type": "base64", "media_type": img.Mime, "data": img.Data}}
}

func isJSONObject(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return strings.HasPrefix(trimmed, "{") && json.Valid(raw)
}

type anthropicUsage struct {
	Input         *int `json:"input_tokens"`
	Output        *int `json:"output_tokens"`
	CacheRead     *int `json:"cache_read_input_tokens"`
	CacheCreation *int `json:"cache_creation_input_tokens"`
}

type anthropicEvent struct {
	Type         string                     `json:"type"`
	Index        *int                       `json:"index"`
	ContentBlock map[string]json.RawMessage `json:"content_block"`
	Message      struct {
		Usage anthropicUsage `json:"usage"`
	} `json:"message"`
	Usage anthropicUsage `json:"usage"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Error providerErrorDetail `json:"error"`
}

type anthropicPendingBlock struct {
	fields    map[string]json.RawMessage
	kind      string
	args      strings.Builder
	content   strings.Builder
	signature strings.Builder
	closed    bool
}

func (b *anthropicPendingBlock) text(key string) string {
	var value string
	_ = json.Unmarshal(b.fields[key], &value)
	return value
}
func readAnthropicStream(body io.Reader, scope string, onText, onReasoning func(string), poke func()) (assistant Message, usage Usage, err error) {
	assistant.Role = "assistant"
	var text, reasoning strings.Builder
	defer func() { assistant.Content = text.String(); assistant.Reasoning = reasoning.String() }()
	var blocks []*anthropicPendingBlock
	started := false
	finish := ""
	var input, cacheRead, cacheCreation int
	applyUsage := func(u anthropicUsage) {
		if u.Input != nil {
			input = *u.Input
		}
		if u.CacheRead != nil {
			cacheRead = *u.CacheRead
		}
		if u.CacheCreation != nil {
			cacheCreation = *u.CacheCreation
		}
		if u.Output != nil {
			usage.OutputTokens = *u.Output
		}
		usage.InputTokens = input + cacheRead + cacheCreation
		usage.CacheReadTokens = cacheRead
	}
	appendVisible := func(kind, delta string) {
		if kind == "text" {
			text.WriteString(delta)
			if onText != nil && delta != "" {
				onText(delta)
			}
		}
		if kind == "thinking" {
			reasoning.WriteString(delta)
			if onReasoning != nil && delta != "" {
				onReasoning(delta)
			}
		}
	}
	finalize := func() (bool, error) {
		switch finish {
		case "end_turn", "stop_sequence", "tool_use", "refusal":
		case "max_tokens":
			return false, fmt.Errorf("agent: Anthropic response truncated (max_tokens)")
		default:
			return false, fmt.Errorf("agent: unsupported Anthropic stop reason %q", finish)
		}
		var items []json.RawMessage
		var toolItems []json.RawMessage
		var calls []ToolCall
		seen := map[string]bool{}
		for _, b := range blocks {
			if !b.closed {
				return false, fmt.Errorf("agent: Anthropic stream ended with open block")
			}
			if b.kind == "tool_use" {
				id := b.text("id")
				if seen[id] {
					return false, fmt.Errorf("agent: duplicate Anthropic tool ID %q", id)
				}
				seen[id] = true
				calls = append(calls, ToolCall{ID: id, Name: b.text("name"), Arguments: string(b.fields["input"])})
			}
			raw, e := json.Marshal(b.fields)
			if e != nil {
				return false, e
			}
			// Match OMP's stable partition: Anthropic expects tool_use at
			// the tail, immediately followed by the next user tool_results.
			// Preserve the relative order of signed/non-tool blocks.
			if b.kind == "tool_use" {
				toolItems = append(toolItems, raw)
			} else {
				items = append(items, raw)
			}
		}
		items = append(items, toolItems...)
		if (finish == "tool_use") != (len(calls) > 0) {
			return false, fmt.Errorf("agent: Anthropic tool calls disagree with stop reason")
		}
		if len(items) == 0 {
			return false, fmt.Errorf("agent: empty Anthropic response")
		}
		assistant.ToolCalls = calls
		assistant.Anthropic = &ProviderReplay{Scope: scope, Items: items}
		return true, nil
	}
	process := func(payload string) (bool, error) {
		var event anthropicEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return false, fmt.Errorf("agent: decode Anthropic event: %w", err)
		}
		if event.Type == "ping" {
			return false, nil
		}
		if event.Type == "error" {
			return false, providerStreamError("Anthropic error", event.Error)
		}
		if event.Type == "message_start" {
			if started {
				return false, fmt.Errorf("agent: duplicate Anthropic message_start")
			}
			started = true
			applyUsage(event.Message.Usage)
			return false, nil
		}
		if !started {
			return false, fmt.Errorf("agent: Anthropic event before message_start")
		}
		switch event.Type {
		case "content_block_start":
			if finish != "" || event.Index == nil || *event.Index != len(blocks) || event.ContentBlock == nil {
				return false, fmt.Errorf("agent: invalid Anthropic block start")
			}
			b := &anthropicPendingBlock{fields: event.ContentBlock}
			b.kind = b.text("type")
			// Marker prevents automatic retry after any block starts, including an
			// opaque-only block. Items become replayable only after validated completion.
			assistant.Anthropic = &ProviderReplay{Scope: scope}
			switch b.kind {
			case "text":
				b.content.WriteString(b.text("text"))
				appendVisible(b.kind, b.text("text"))
			case "thinking":
				b.content.WriteString(b.text("thinking"))
				b.signature.WriteString(b.text("signature"))
				appendVisible(b.kind, b.text("thinking"))
			case "redacted_thinking", "tool_use":
			default:
				return false, fmt.Errorf("agent: unsupported Anthropic block %q", b.kind)
			}
			blocks = append(blocks, b)
		case "content_block_delta", "content_block_stop":
			if finish != "" || event.Index == nil || *event.Index < 0 || *event.Index >= len(blocks) {
				return false, fmt.Errorf("agent: invalid Anthropic block index")
			}
			b := blocks[*event.Index]
			if b.closed {
				return false, fmt.Errorf("agent: Anthropic block already closed")
			}
			if event.Type == "content_block_stop" {
				b.closed = true
				if b.kind == "text" {
					b.fields["text"], _ = json.Marshal(b.content.String())
				}
				if b.kind == "thinking" {
					b.fields["thinking"], _ = json.Marshal(b.content.String())
					b.fields["signature"], _ = json.Marshal(b.signature.String())
				}
				if b.kind == "tool_use" {
					if b.args.Len() > 0 {
						b.fields["input"] = json.RawMessage(b.args.String())
					}
					if b.text("id") == "" || b.text("name") == "" || !isJSONObject(b.fields["input"]) {
						return false, fmt.Errorf("agent: invalid Anthropic tool_use")
					}
				}
				if b.kind == "thinking" && b.text("signature") == "" {
					return false, fmt.Errorf("agent: Anthropic thinking block has no signature")
				}
				if b.kind == "redacted_thinking" && b.text("data") == "" {
					return false, fmt.Errorf("agent: empty Anthropic redacted thinking")
				}
				return false, nil
			}
			switch {
			case b.kind == "text" && event.Delta.Type == "text_delta":
				b.content.WriteString(event.Delta.Text)
				appendVisible(b.kind, event.Delta.Text)
			case b.kind == "thinking" && event.Delta.Type == "thinking_delta":
				b.content.WriteString(event.Delta.Thinking)
				appendVisible(b.kind, event.Delta.Thinking)
			case b.kind == "thinking" && event.Delta.Type == "signature_delta":
				b.signature.WriteString(event.Delta.Signature)
			case b.kind == "tool_use" && event.Delta.Type == "input_json_delta":
				b.args.WriteString(event.Delta.PartialJSON)
			default:
				return false, fmt.Errorf("agent: unsupported Anthropic delta %q for %q", event.Delta.Type, b.kind)
			}
		case "message_delta":
			if finish != "" {
				return false, fmt.Errorf("agent: duplicate Anthropic terminal delta")
			}
			finish = event.Delta.StopReason
			applyUsage(event.Usage)
		case "message_stop":
			return finalize()
		}
		return false, nil
	}
	done, err := readSSEEvents(body, poke, process)
	if err != nil {
		return assistant, usage, err
	}
	if !done && finish != "" {
		// Match OMP: a gateway may omit message_stop after its terminal delta.
		// The finalizer still requires closed blocks and a usable stop reason.
		_, err := finalize()
		return assistant, usage, err
	}
	if !done {
		return assistant, usage, fmt.Errorf("agent: Anthropic stream ended before message_stop")
	}
	return assistant, usage, nil
}

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func anthropicSSE(kind string, fields map[string]any) string {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["type"] = kind
	data, _ := json.Marshal(fields)
	return "event: " + kind + "\ndata: " + string(data) + "\n\n"
}
func anthropicStart() string {
	return anthropicSSE("message_start", map[string]any{"message": map[string]any{"usage": map[string]int{"input_tokens": 10, "output_tokens": 1, "cache_read_input_tokens": 5, "cache_creation_input_tokens": 2}}})
}
func anthropicBlockStart(index int, block map[string]any) string {
	return anthropicSSE("content_block_start", map[string]any{"index": index, "content_block": block})
}
func anthropicDelta(index int, kind, key, value string) string {
	return anthropicSSE("content_block_delta", map[string]any{"index": index, "delta": map[string]string{"type": kind, key: value}})
}
func anthropicBlockStop(index int) string {
	return anthropicSSE("content_block_stop", map[string]any{"index": index})
}
func anthropicEnd(reason string) string {
	return anthropicSSE("message_delta", map[string]any{"delta": map[string]string{"stop_reason": reason}, "usage": map[string]int{"output_tokens": 7}}) + anthropicSSE("message_stop", nil)
}
func anthropicText(index int, text string) string {
	return anthropicBlockStart(index, map[string]any{"type": "text", "text": ""}) + anthropicDelta(index, "text_delta", "text", text) + anthropicBlockStop(index)
}
func anthropicThinking(index int) string {
	return anthropicBlockStart(index, map[string]any{"type": "thinking", "thinking": "plan ", "signature": ""}) +
		anthropicDelta(index, "thinking_delta", "thinking", "carefully") +
		anthropicDelta(index, "signature_delta", "signature", "private-signature") + anthropicBlockStop(index)
}
func anthropicCall(index int, id string) string {
	return anthropicBlockStart(index, map[string]any{"type": "tool_use", "id": id, "name": "shell", "input": map[string]any{}}) +
		anthropicDelta(index, "input_json_delta", "partial_json", "{") + anthropicDelta(index, "input_json_delta", "partial_json", "}") + anthropicBlockStop(index)
}

func TestAnthropicHTTPToolContinuationAndScopedReplay(t *testing.T) {
	var mu sync.Mutex
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path != "/messages" || r.Header.Get("x-api-key") != "fixture-key" || r.Header.Get("anthropic-version") != "2023-06-01" || r.Header.Get("Authorization") != "" {
			t.Error("wrong Anthropic endpoint/auth headers")
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		requests = append(requests, request)
		w.Header().Set("Content-Type", "text/event-stream")
		if len(requests) == 1 {
			fmt.Fprint(w, anthropicStart()+anthropicThinking(0)+anthropicBlockStart(1, map[string]any{"type": "redacted_thinking", "data": "private-redacted"})+anthropicBlockStop(1)+anthropicText(2, "inspect")+anthropicCall(3, "call-1")+anthropicCall(4, "call-2")+anthropicEnd("tool_use"))
		} else {
			fmt.Fprint(w, anthropicStart()+anthropicText(0, "done")+anthropicEnd("end_turn"))
		}
	}))
	defer server.Close()
	cfg := testConfig(server.URL, []Tool{&fakeTool{name: "shell"}})
	cfg.API = "anthropic-messages"
	cfg.ProviderID = "one"
	cfg.APIKey = "fixture-key"
	cfg.MaxOutputTokens = 12000
	cfg.Thinking = AnthropicThinkingConfig{Mode: "anthropic-budget"}
	cfg.ReasoningEffort = "high"
	session := New(cfg)
	emit, events := collectEvents()
	answer, err := session.RunWithImages(context.Background(), "inspect", []MessageImage{{Mime: "image/png", Data: "fixture-image"}}, emit)
	if err != nil || answer != "done" {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("requests=%d", len(requests))
	}
	if requests[0]["max_tokens"] != float64(12000) || requests[0]["system"] != "sys" {
		t.Fatalf("options=%+v", requests[0])
	}
	thinking := requests[0]["thinking"].(map[string]any)
	if thinking["type"] != "enabled" || thinking["budget_tokens"] != float64(8192) {
		t.Fatalf("thinking=%+v", thinking)
	}
	wire, _ := json.Marshal(requests[1])
	for _, want := range []string{"private-signature", "private-redacted", "tool_result", "tool output", "media_type", "fixture-image", "input_schema"} {
		if !strings.Contains(string(wire), want) {
			t.Fatalf("missing %s", want)
		}
	}
	messages := requests[1]["messages"].([]any)
	last := messages[len(messages)-1].(map[string]any)
	results := last["content"].([]any)
	if last["role"] != "user" || len(results) != 2 || results[0].(map[string]any)["tool_use_id"] != "call-1" || results[1].(map[string]any)["tool_use_id"] != "call-2" {
		t.Fatalf("tool results not grouped: %+v", last)
	}
	done := eventsOfType(*events, EventDone)
	if len(done) != 1 || done[0].Usage.InputTokens != 34 || done[0].Usage.OutputTokens != 14 {
		t.Fatalf("usage=%+v", done)
	}
	history := session.History()
	if history[1].Reasoning != "plan carefully" || history[1].Anthropic == nil || len(history[1].Anthropic.Items) != 5 {
		t.Fatalf("assistant=%+v", history[1])
	}
	history[1].Anthropic.Items[0][0] = '!'
	if session.History()[1].Anthropic.Items[0][0] != '{' {
		t.Fatal("shared replay bytes")
	}
	history = session.History()
	for _, change := range []func(*Config){func(c *Config) { c.ProviderID = "two" }, func(c *Config) { c.BaseURL += "/other" }, func(c *Config) { c.Model = "other" }} {
		other := cfg
		change(&other)
		raw, e := marshalAnthropicRequest(other, history)
		if e != nil || strings.Contains(string(raw), "private-") || !strings.Contains(string(raw), "tool_use") {
			t.Fatalf("cross-route fallback: %s %v", raw, e)
		}
	}
	for _, marshal := range []func(Config, []Message) ([]byte, error){marshalChatRequest, marshalResponsesRequest} {
		raw, e := marshal(cfg, history)
		if e != nil || strings.Contains(string(raw), "private-") || strings.Contains(string(raw), "anthropic") {
			t.Fatalf("opaque state leaked to other API: %s %v", raw, e)
		}
	}
}

func TestAnthropicMalformedAndFailedStreamsNeverExecute(t *testing.T) {
	tool := anthropicBlockStart(1, map[string]any{"type": "tool_use", "id": "call", "name": "shell", "input": map[string]any{}})
	for name, ending := range map[string]string{
		"EOF":               tool + anthropicDelta(1, "input_json_delta", "partial_json", "{"),
		"provider error":    tool + anthropicSSE("error", map[string]any{"error": map[string]string{"type": "overloaded_error", "message": "busy"}}),
		"max tokens":        anthropicCall(1, "call") + anthropicEnd("max_tokens"),
		"open block":        tool + anthropicEnd("tool_use"),
		"invalid JSON":      tool + anthropicDelta(1, "input_json_delta", "partial_json", "{") + anthropicBlockStop(1),
		"array input":       tool + anthropicDelta(1, "input_json_delta", "partial_json", "[]") + anthropicBlockStop(1),
		"missing stop":      anthropicCall(1, "call") + anthropicSSE("message_stop", nil),
		"inconsistent stop": anthropicCall(1, "call") + anthropicEnd("end_turn"),
		"duplicate ID":      anthropicCall(1, "call") + anthropicCall(2, "call") + anthropicEnd("tool_use"),
		"wrong index":       anthropicDelta(4, "text_delta", "text", "wrong"),
		"unsigned thinking": anthropicBlockStart(1, map[string]any{"type": "thinking", "thinking": "unsigned"}) + anthropicBlockStop(1),
		"hosted tool":       anthropicBlockStart(1, map[string]any{"type": "server_tool_use", "id": "srv", "name": "web_search"}),
	} {
		t.Run(name, func(t *testing.T) {
			provider, hits := newFakeProvider(t, anthropicStart()+anthropicText(0, "partial")+ending)
			tool := &fakeTool{name: "shell"}
			cfg := testConfig(provider.URL, []Tool{tool})
			cfg.API = "anthropic-messages"
			session := New(cfg)
			_, err := session.Run(context.Background(), "test", nil)
			if err == nil || tool.ran || hits.Load() != 1 {
				t.Fatalf("err=%v ran=%v hits=%d", err, tool.ran, hits.Load())
			}
			history := session.History()
			if len(history) != 2 || history[1].Content != "partial" || history[1].Anthropic != nil || len(history[1].ToolCalls) != 0 {
				t.Fatalf("unsafe partial history=%+v", history)
			}
		})
	}
}

func TestAnthropicCancellationAndRetryBoundary(t *testing.T) {
	t.Run("cancel drops signature state", func(t *testing.T) {
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, anthropicStart()+anthropicBlockStart(0, map[string]any{"type": "thinking", "thinking": ""})+anthropicDelta(0, "thinking_delta", "thinking", "partial thought"))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}))
		defer provider.Close()
		cfg := testConfig(provider.URL, nil)
		cfg.API = "anthropic-messages"
		session := New(cfg)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		_, err := session.Run(ctx, "test", func(event Event) {
			if event.Type == EventReasoning {
				cancel()
			}
		})
		history := session.History()
		if err == nil || len(history) != 2 || history[1].Reasoning != "partial thought" || history[1].Anthropic != nil {
			t.Fatalf("err=%v history=%+v", err, history)
		}
	})
	t.Run("retry before blocks", func(t *testing.T) {
		provider, hits := newFakeProvider(t, anthropicSSE("error", map[string]any{"error": map[string]string{"type": "overloaded_error", "message": "retry"}}), anthropicStart()+anthropicText(0, "recovered")+anthropicEnd("end_turn"))
		cfg := testConfig(provider.URL, nil)
		cfg.API = "anthropic-messages"
		result, err := New(cfg).Run(context.Background(), "test", nil)
		if err != nil || result != "recovered" || hits.Load() != 2 {
			t.Fatalf("result=%q err=%v hits=%d", result, err, hits.Load())
		}
	})
	t.Run("opaque progress prevents retry", func(t *testing.T) {
		provider, hits := newFakeProvider(t, anthropicStart()+anthropicBlockStart(0, map[string]any{"type": "redacted_thinking", "data": "opaque"}))
		cfg := testConfig(provider.URL, nil)
		cfg.API = "anthropic-messages"
		_, err := New(cfg).Run(context.Background(), "test", nil)
		if err == nil || hits.Load() != 1 {
			t.Fatalf("err=%v hits=%d", err, hits.Load())
		}
	})
}

func TestAnthropicThinkingConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, mode, effort string
		max, budget        int
		want               any
		wantEffort         string
		invalid            bool
	}{
		{name: "unconfigured has no thinking", effort: "medium"},
		{name: "budget off", mode: "anthropic-budget", effort: "off"},
		{name: "budget low", mode: "anthropic-budget", effort: "low", want: map[string]any{"type": "enabled", "budget_tokens": float64(1024)}},
		{name: "budget capped", mode: "anthropic-budget", effort: "high", max: 4096, want: map[string]any{"type": "enabled", "budget_tokens": float64(3072)}},
		{name: "explicit budget", mode: "anthropic-budget", effort: "high", budget: 2048, want: map[string]any{"type": "enabled", "budget_tokens": float64(2048)}},
		{name: "adaptive", mode: "anthropic-adaptive", effort: "high", want: map[string]any{"type": "adaptive"}, wantEffort: "high"},
		{name: "adaptive off uses low", mode: "anthropic-adaptive", effort: "off", want: map[string]any{"type": "adaptive"}, wantEffort: "low"},
		{name: "unknown mode", mode: "guess", invalid: true},
		{name: "negative cap", max: -1, invalid: true},
		{name: "too small cap", mode: "anthropic-budget", max: 1024, invalid: true},
		{name: "budget exceeds cap", mode: "anthropic-budget", budget: 8192, max: 8192, invalid: true},
		{name: "adaptive cannot set budget", mode: "anthropic-adaptive", budget: 2048, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Model: "fixture", MaxOutputTokens: tc.max, Thinking: AnthropicThinkingConfig{Mode: tc.mode, BudgetTokens: tc.budget}, ReasoningEffort: tc.effort}
			raw, err := marshalAnthropicRequest(cfg, []Message{{Role: "user", Content: "hello"}})
			if tc.invalid {
				if err == nil {
					t.Fatal("invalid config accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var request map[string]any
			json.Unmarshal(raw, &request)
			if !reflect.DeepEqual(request["thinking"], tc.want) {
				t.Fatalf("thinking=%+v want=%+v", request["thinking"], tc.want)
			}
			if tc.wantEffort != "" && request["output_config"].(map[string]any)["effort"] != tc.wantEffort {
				t.Fatalf("effort=%+v", request["output_config"])
			}
		})
	}
}

func TestAnthropicToolImagesAndErrorScope(t *testing.T) {
	preview := testToolPreview(t)
	history := []Message{
		{Role: "user", Content: "read"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "same", Name: "read_file", Arguments: "{}", IsError: true}}},
		{Role: "tool", ToolCallID: "same", Content: "denied"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "same", Name: "read_file", Arguments: "{}"}}},
		{Role: "tool", ToolCallID: "same", Content: preview},
		{Role: "user", Content: "describe it"},
	}
	before := cloneMessages(history)
	raw, err := marshalAnthropicRequest(Config{Model: "fixture"}, history)
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		Messages []struct {
			Role    string
			Content []struct {
				Type    string
				IsError bool `json:"is_error"`
				Content json.RawMessage
			}
		}
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	if len(request.Messages) != 5 || !request.Messages[2].Content[0].IsError || request.Messages[4].Content[0].IsError || len(request.Messages[4].Content) != 2 {
		t.Fatalf("wrong grouping/error scope: %s", raw)
	}
	var imageParts []struct {
		Type   string
		Source map[string]string
	}
	if err := json.Unmarshal(request.Messages[4].Content[0].Content, &imageParts); err != nil {
		t.Fatal(err)
	}
	if len(imageParts) != 1 || imageParts[0].Type != "image" || imageParts[0].Source["media_type"] != "image/jpeg" || imageParts[0].Source["data"] != strings.TrimPrefix(preview, "[image preview]\ndata:image/jpeg;base64,") {
		t.Fatal("missing tool result image")
	}
	if !reflect.DeepEqual(history, before) {
		t.Fatal("request conversion mutated history")
	}
}

func TestAnthropicCompactionAndReplayBlockOrder(t *testing.T) {
	// A provider may emit later text after a call. Keep signed blocks in order
	// but move tool_use to the tail when replaying, as OMP does.
	wire := anthropicStart() + anthropicThinking(0) + anthropicCall(1, "call") + anthropicText(2, "later text") + anthropicEnd("tool_use")
	assistant, _, err := readAnthropicStream(strings.NewReader(wire), "scope", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, raw := range assistant.Anthropic.Items {
		var block struct{ Type string }
		json.Unmarshal(raw, &block)
		types = append(types, block.Type)
	}
	if !reflect.DeepEqual(types, []string{"thinking", "text", "tool_use"}) {
		t.Fatalf("replay order=%v", types)
	}
	provider, _ := newFakeProvider(t, anthropicStart()+anthropicText(0, "earlier summary")+anthropicEnd("end_turn"))
	cfg := testConfig(provider.URL, nil)
	cfg.API = "anthropic-messages"
	assistant.Anthropic.Scope = providerReplayScope(cfg)
	session := New(cfg)
	session.SetHistory([]Message{{Role: "user", Content: "old"}, {Role: "assistant", Content: "old answer"}, {Role: "user", Content: "new"}, assistant, {Role: "tool", ToolCallID: "call", Content: "result"}})
	if _, err := session.Compact(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	history := session.History()
	if len(history) != 5 || !strings.Contains(history[0].Content, "earlier summary") || !reflect.DeepEqual(history[3].Anthropic, assistant.Anthropic) {
		t.Fatalf("compaction lost replay: %+v", history)
	}
}

func TestAnthropicTerminalDeltaWithoutMessageStop(t *testing.T) {
	terminal := anthropicSSE("message_delta", map[string]any{"delta": map[string]string{"stop_reason": "tool_use"}})
	good := anthropicStart() + anthropicCall(0, "call") + terminal
	msg, _, err := readAnthropicStream(strings.NewReader(good), "scope", nil, nil, nil)
	if err != nil || len(msg.ToolCalls) != 1 || msg.Anthropic == nil || len(msg.Anthropic.Items) != 1 {
		t.Fatalf("complete terminal delta discarded: %+v %v", msg, err)
	}
	for name, wire := range map[string]string{
		"open block":        anthropicStart() + anthropicBlockStart(0, map[string]any{"type": "tool_use", "id": "call", "name": "shell", "input": map[string]any{}}) + terminal,
		"no terminal delta": anthropicStart() + anthropicCall(0, "call"),
		"length limit":      anthropicStart() + anthropicCall(0, "call") + anthropicSSE("message_delta", map[string]any{"delta": map[string]string{"stop_reason": "max_tokens"}}),
	} {
		t.Run(name, func(t *testing.T) {
			msg, _, err := readAnthropicStream(strings.NewReader(wire), "scope", nil, nil, nil)
			if err == nil || len(msg.ToolCalls) != 0 || (msg.Anthropic != nil && len(msg.Anthropic.Items) > 0) {
				t.Fatalf("incomplete turn made executable: %+v %v", msg, err)
			}
		})
	}
}

func TestAnthropicThinkingDisplayRequiresExplicitCapability(t *testing.T) {
	for _, mode := range []string{"anthropic-budget", "anthropic-adaptive"} {
		for _, supported := range []bool{false, true} {
			cfg := Config{Model: "fixture", Thinking: AnthropicThinkingConfig{Mode: mode, SupportsDisplay: supported}, ReasoningEffort: "medium"}
			raw, err := marshalAnthropicRequest(cfg, []Message{{Role: "user", Content: "hello"}})
			if err != nil {
				t.Fatal(err)
			}
			var request struct{ Thinking map[string]any }
			json.Unmarshal(raw, &request)
			_, sent := request.Thinking["display"]
			if sent != supported || (sent && request.Thinking["display"] != "summarized") {
				t.Fatalf("mode=%s capability=%v request=%s", mode, supported, raw)
			}
		}
	}
}

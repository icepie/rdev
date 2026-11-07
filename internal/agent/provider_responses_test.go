package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func responseSSE(event any) string {
	data, _ := json.Marshal(event)
	return "data: " + string(data) + "\n\n"
}
func responseComplete(output []any) string {
	return responseSSE(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": output, "usage": map[string]int{"input_tokens": 10, "output_tokens": 4}}})
}
func responseText(text string) map[string]any {
	return map[string]any{"id": "msg_1", "type": "message", "role": "assistant", "status": "completed", "phase": "final_answer", "content": []any{map[string]string{"type": "output_text", "text": text}}}
}

func TestResponsesToolContinuationAndReplayScope(t *testing.T) {
	var requests []map[string]any
	var mu sync.Mutex
	reasoning := map[string]any{"id": "rs_1", "type": "reasoning", "encrypted_content": "opaque-fixture", "summary": []any{map[string]string{"type": "summary_text", "text": "thinking"}}}
	call := map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "shell", "arguments": "{}", "status": "completed"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path != "/responses" {
			t.Errorf("wrong route %s", r.URL.Path)
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		requests = append(requests, request)
		w.Header().Set("Content-Type", "text/event-stream")
		if len(requests) == 1 {
			fmt.Fprint(w, responseSSE(map[string]any{"type": "response.output_item.added", "output_index": 1, "item": call}))
			fmt.Fprint(w, responseSSE(map[string]any{"type": "response.reasoning_summary_text.delta", "delta": "thinking"}))
			fmt.Fprint(w, responseComplete([]any{reasoning, call}))
		} else {
			fmt.Fprint(w, responseComplete([]any{responseText("done")}))
		}
	}))
	defer server.Close()
	cfg := testConfig(server.URL, []Tool{&fakeTool{name: "shell"}})
	cfg.API = "openai-responses"
	cfg.ProviderID = "one"
	session := New(cfg)
	emit, events := collectEvents()
	result, err := session.RunWithImages(context.Background(), "inspect", []MessageImage{{Mime: "image/png", Data: "fixture"}}, emit)
	if err != nil || result != "done" {
		t.Fatalf("run: %q %v", result, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("requests=%d", len(requests))
	}
	if requests[0]["store"] != false || requests[0]["instructions"] != "sys" {
		t.Fatalf("wrong request options: %+v", requests[0])
	}
	wire, _ := json.Marshal(requests[1])
	for _, want := range []string{"opaque-fixture", "function_call_output", "call_1", "tool output", "input_image"} {
		if !strings.Contains(string(wire), want) {
			t.Fatalf("missing %s: %s", want, wire)
		}
	}
	if done := eventsOfType(*events, EventDone); len(done) != 1 || done[0].Usage.InputTokens != 20 {
		t.Fatalf("usage: %+v", done)
	}
	history := session.History()
	if history[1].Responses == nil || len(history[1].Responses.Items) != 2 {
		t.Fatal("missing reasoning replay")
	}
	// Snapshot isolation is necessary before persistence/compaction and model changes.
	history[1].Responses.Items[0][0] = '!'
	if session.History()[1].Responses.Items[0][0] != '{' {
		t.Fatal("shared replay bytes")
	}
	final := session.History()
	replay, _ := marshalResponsesRequest(cfg, final)
	if !strings.Contains(string(replay), `"phase":"final_answer"`) {
		t.Fatal("assistant phase discarded")
	}
	for _, change := range []func(*Config){func(c *Config) { c.ProviderID = "two" }, func(c *Config) { c.Model = "other" }, func(c *Config) { c.BaseURL += "/different" }} {
		other := cfg
		change(&other)
		payload, e := marshalResponsesRequest(other, final)
		if e != nil || strings.Contains(string(payload), "opaque-fixture") || !strings.Contains(string(payload), "function_call_output") {
			t.Fatalf("cross-provider fallback: %s %v", payload, e)
		}
	}
	chat, e := marshalChatRequest(cfg, final)
	if e != nil || strings.Contains(string(chat), "encrypted_content") || strings.Contains(string(chat), "responses") {
		t.Fatalf("replay leaked to chat format: %s %v", chat, e)
	}
}

func TestResponsesIncompletePreservesTextWithoutExecutingOrRetrying(t *testing.T) {
	for _, ending := range []string{
		"", responseSSE(map[string]any{"type": "response.incomplete", "response": map[string]any{"incomplete_details": map[string]string{"reason": "max_output_tokens"}}}),
		responseSSE(map[string]any{"type": "response.failed", "response": map[string]any{"error": map[string]string{"message": "upstream failed"}}}),
		responseSSE(map[string]string{"type": "error", "message": "failed"}),
	} {
		t.Run(fmt.Sprintf("ending-%d", len(ending)), func(t *testing.T) {
			var hits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				fmt.Fprint(w, responseSSE(map[string]string{"type": "response.output_text.delta", "delta": "partial"})+responseSSE(map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]string{"type": "function_call", "call_id": "call", "name": "shell", "arguments": "{}"}})+ending)
			}))
			defer server.Close()
			tool := &fakeTool{name: "shell"}
			cfg := testConfig(server.URL, []Tool{tool})
			cfg.API = "openai-responses"
			s := New(cfg)
			emit, events := collectEvents()
			_, err := s.Run(context.Background(), "test", emit)
			if err == nil || tool.ran || hits.Load() != 1 || len(eventsOfType(*events, EventDone)) != 0 {
				t.Fatalf("err=%v hits=%d", err, hits.Load())
			}
			h := s.History()
			if len(h) != 2 || h[1].Content != "partial" || h[1].Responses != nil || len(h[1].ToolCalls) != 0 {
				t.Fatalf("bad partial history %+v", h)
			}
		})
	}
}

func TestResponsesStreamFramingAndToolOnlyFailure(t *testing.T) {
	// Multiline data and a final event without the trailing blank line are valid SSE.
	raw := "event: response.completed\r\ndata: {\"type\":\"response.completed\",\n" +
		"data: \"response\":{\"status\":\"completed\",\"output\":[]}}"
	_, _, err := readResponsesStream(strings.NewReader(raw), "scope", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	call := responseSSE(map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]string{"type": "function_call", "call_id": "c", "name": "shell"}})
	call += responseSSE(map[string]any{"type": "response.function_call_arguments.delta", "output_index": 0, "delta": "{"})
	result, _, err := readResponsesStream(strings.NewReader(call), "scope", nil, nil, nil)
	if err == nil || len(result.ToolCalls) != 1 || result.Responses == nil {
		t.Fatalf("lost progress classification: %+v %v", result, err)
	}
}

func TestResponsesCancellationPreservesOnlyReadablePartialOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, responseSSE(map[string]string{"type": "response.reasoning_summary_text.delta", "delta": "partial thought"}))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	cfg := testConfig(server.URL, nil)
	cfg.API = "openai-responses"
	session := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := session.Run(ctx, "question", func(event Event) {
		if event.Type == EventReasoning {
			cancel()
		}
	})
	if err == nil {
		t.Fatal("canceled response succeeded")
	}
	history := session.History()
	if len(history) != 2 || history[1].Reasoning != "partial thought" || history[1].Responses != nil {
		t.Fatalf("partial cancellation: %+v", history)
	}
}

func TestResponsesCompactionRetainsRecentOpaqueState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("compaction used %s", r.URL.Path)
		}
		fmt.Fprint(w, responseComplete([]any{responseText("Earlier task summary")}))
	}))
	defer server.Close()
	cfg := testConfig(server.URL, nil)
	cfg.API = "openai-responses"
	session := New(cfg)
	recent := Message{Role: "assistant", Content: "recent", Responses: &ResponseReplay{Scope: responseScope(cfg), Items: []json.RawMessage{json.RawMessage(`{"type":"message","phase":"final_answer","role":"assistant","content":[{"type":"output_text","text":"recent"}]}`)}}}
	session.SetHistory([]Message{{Role: "user", Content: "old"}, {Role: "assistant", Content: "old answer"}, {Role: "user", Content: "new"}, recent})
	if _, err := session.Compact(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	history := session.History()
	if len(history) != 4 || !strings.Contains(history[0].Content, "Earlier task summary") || history[3].Responses == nil {
		t.Fatalf("compacted history: %+v", history)
	}
}

func TestResponsesRetriesOnlyBeforeOutput(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			http.Error(w, "temporary", http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, responseComplete([]any{responseText("recovered")}))
	}))
	defer server.Close()
	cfg := testConfig(server.URL, nil)
	cfg.API = "openai-responses"
	result, err := New(cfg).Run(context.Background(), "hello", nil)
	if err != nil || result != "recovered" || hits.Load() != 2 {
		t.Fatalf("result=%q err=%v requests=%d", result, err, hits.Load())
	}
}

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

const overflowJSON = `{"error":{"code":"context_length_exceeded","type":"invalid_request_error","message":"input exceeds context"}}`
const overflowSSE = "data: " + overflowJSON + "\n\n"

func TestContextOverflowClassificationAcrossProtocols(t *testing.T) {
	for _, tc := range []struct {
		name, api, body string
		status          int
		want            bool
	}{
		{"chat-http", "", overflowJSON, 400, true},
		{"responses-http", "openai-responses", overflowJSON, 400, true},
		{"anthropic-http", "anthropic-messages", `{"error":{"type":"invalid_request_error","message":"prompt is too long: 500 tokens > 100 maximum"}}`, 400, true},
		{"chat-stream", "", overflowSSE, 200, true},
		{"responses-failed", "openai-responses", `data: {"type":"response.failed","response":{"error":{"code":"context_length_exceeded","message":"too long"}}}` + "\n\n", 200, true},
		{"responses-error", "openai-responses", `data: {"type":"error","code":"context_length_exceeded","message":"too long"}` + "\n\n", 200, true},
		{"anthropic-stream", "anthropic-messages", `data: {"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 500 > 100"}}` + "\n\n", 200, true},
		{"generic-invalid", "", `{"error":{"type":"invalid_request_error","message":"invalid tool schema"}}`, 400, false},
		{"generic-413", "", `request too large`, 413, false},
		{"provider-body-limit-413", "", `{"error":{"message":"Failed to buffer the request body: length limit exceeded (request_id: fixture)","type":"payload_too_large_error","code":"invalid_request_error"}}`, 413, true},
		{"generic-message", "", `{"error":{"message":"context_length_exceeded appears in a tool name"}}`, 400, false},
		{"output-limit", "openai-responses", `data: {"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}}}` + "\n\n", 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			cfg := testConfig(server.URL, nil)
			cfg.API = tc.api
			cfg.HTTPClient = server.Client()
			_, _, err := streamChat(t.Context(), cfg, []Message{{Role: "user", Content: "task"}}, nil, nil)
			if errors.Is(err, ErrContextLengthExceeded) != tc.want {
				t.Fatalf("overflow=%v err=%v", tc.want, err)
			}
			if tc.want && hits.Load() != 1 {
				t.Fatalf("overflow retried unchanged input %d times", hits.Load())
			}
		})
	}
}

func TestRequestBodyLimitTriggersContextRecovery(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch hits.Add(1) {
		case 1:
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			fmt.Fprint(w, `{"error":{"message":"Failed to buffer the request body: length limit exceeded","type":"payload_too_large_error"}}`)
		case 2, 3:
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, sseChunk("recovered", "")+sseDone)
		default:
			t.Errorf("unexpected provider request %d", hits.Load())
		}
	}))
	defer server.Close()
	session := New(testConfig(server.URL, nil))
	session.SetHistory([]Message{{Role: "user", Content: "old request"}, {Role: "assistant", Content: "old answer"}})
	text, err := session.Run(t.Context(), "current request", nil)
	if err != nil || text != "recovered" || hits.Load() != 3 {
		t.Fatalf("text=%q err=%v requests=%d, want compaction and retry", text, err, hits.Load())
	}
}

func TestContextRecoveryNeverRetriesPartialOutput(t *testing.T) {
	for _, tc := range []struct{ name, api, wire string }{
		{"text", "", sseChunk("visible", "") + overflowSSE},
		{"reasoning", "", sseChunk("", "thinking") + overflowSSE},
		{"tool", "", sseChunk("", "", sseToolCall("c1", "shell", `{}`)) + overflowSSE},
		{"opaque-response", "openai-responses", `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning"}}` + "\n\n" + `data: {"type":"error","code":"context_length_exceeded","message":"too long"}` + "\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, hits := newFakeProvider(t, tc.wire)
			tool := &fakeTool{name: "shell"}
			cfg := testConfig(server.URL, []Tool{tool})
			cfg.API = tc.api
			session := New(cfg)
			session.SetHistory([]Message{{Role: "user", Content: "old"}, {Role: "assistant", Content: "old answer"}})
			_, err := session.Run(t.Context(), "latest", nil)
			if !errors.Is(err, ErrContextLengthExceeded) || hits.Load() != 1 || tool.ran {
				t.Fatalf("err=%v hits=%d tool=%v", err, hits.Load(), tool.ran)
			}
			if len(session.History()) < 3 {
				t.Fatal("latest request lost")
			}
		})
	}
}

func TestContextRecoveryCompactsWithoutDuplicatingTools(t *testing.T) {
	server, hits := newFakeProvider(t,
		sseChunk("", "", sseToolCall("c1", "shell", `{}`))+sseDone,
		overflowSSE,
		sseChunk("Earlier goal and completed work.", "")+sseDone,
		sseChunk("done", "")+sseDone,
	)
	tool := &fakeTool{name: "shell"}
	session := New(testConfig(server.URL, []Tool{tool}))
	session.SetHistory([]Message{{Role: "user", Content: "old"}, {Role: "assistant", Content: "old answer"}})
	revision := session.HistoryRevision()
	emit, events := collectEvents()
	text, err := session.Run(t.Context(), "latest", emit)
	if err != nil || text != "done" || hits.Load() != 4 {
		t.Fatalf("text=%q err=%v hits=%d", text, err, hits.Load())
	}
	if !tool.ran || len(eventsOfType(*events, EventToolStart)) != 1 || len(eventsOfType(*events, EventToolEnd)) != 1 {
		t.Fatal("tool execution was lost or duplicated")
	}
	if session.HistoryRevision() != revision+1 || session.TurnHistoryIndex() != 2 {
		t.Fatal("history replacement metadata not remapped")
	}
	history := session.History()
	if history[2].Content != "latest" || len(history[3].ToolCalls) != 1 || history[4].ToolCallID != "c1" {
		t.Fatalf("retained tool group changed: %+v", history)
	}
}

func TestCompactAdaptsOversizedSingleRecordsWithoutDroppingText(t *testing.T) {
	var rejected atomic.Int32
	var seenFirst, seenLast atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request chatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if len(request.Tools) != 0 {
			t.Error("compaction offered tools")
		}
		content := request.Messages[len(request.Messages)-1].Content
		if len(content) > 4000 {
			rejected.Add(1)
			w.WriteHeader(400)
			fmt.Fprint(w, overflowJSON)
			return
		}
		if strings.Contains(content, "FIRST_FACT") {
			seenFirst.Store(true)
		}
		if strings.Contains(content, "LAST_FACT") {
			seenLast.Store(true)
		}
		fmt.Fprint(w, sseChunk("Preserved goals, completed results, and pending checks.", "")+sseDone)
	}))
	defer server.Close()
	session := New(testConfig(server.URL, nil))
	latest := Message{Role: "user", Content: "latest request must remain exact"}
	session.SetHistory([]Message{{Role: "user", Content: "FIRST_FACT" + strings.Repeat("large record ", 5000) + "LAST_FACT"}, {Role: "assistant", Content: "old result"}, latest})
	if _, err := session.Compact(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if rejected.Load() == 0 || !seenFirst.Load() || !seenLast.Load() {
		t.Fatalf("rejections=%d first=%v last=%v", rejected.Load(), seenFirst.Load(), seenLast.Load())
	}
	if !reflect.DeepEqual(session.History()[2], latest) {
		t.Fatal("latest request changed")
	}
}

func TestContextRecoverySingleLongTurnRetainsLatestRequest(t *testing.T) {
	var rounds []string
	for i := range 6 {
		rounds = append(rounds, sseChunk("", "", sseToolCall(fmt.Sprintf("tool-%d", i), "shell", `{}`))+sseDone)
	}
	rounds = append(rounds, overflowSSE, sseChunk("Completed six tool operations; verification pending.", "")+sseDone, sseChunk("done", "")+sseDone)
	server, hits := newFakeProvider(t, rounds...)
	session := New(testConfig(server.URL, []Tool{&fakeTool{name: "shell"}}))
	emit, events := collectEvents()
	text, err := session.Run(t.Context(), "Complete task exactly", emit)
	if err != nil || text != "done" || hits.Load() != 9 {
		t.Fatalf("text=%q err=%v hits=%d", text, err, hits.Load())
	}
	got := session.History()
	if got[2].Content != "Complete task exactly" || session.TurnHistoryIndex() != 2 {
		t.Fatalf("latest request not preserved: %+v", got)
	}
	if len(eventsOfType(*events, EventToolStart)) != 6 || len(eventsOfType(*events, EventToolEnd)) != 6 {
		t.Fatal("completed tools were duplicated")
	}
	for i, message := range got {
		if message.Role == "tool" && (i == 0 || len(got[i-1].ToolCalls) == 0 || got[i-1].ToolCalls[0].ID != message.ToolCallID) {
			t.Fatal("orphaned tool result")
		}
	}
}

func TestRecoveryFailureAndBoundedOverflowPreserveRequest(t *testing.T) {
	for _, summaryFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(summaryFailure), func(t *testing.T) {
			var hits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request chatRequest
				_ = json.NewDecoder(r.Body).Decode(&request)
				hits.Add(1)
				if len(request.Messages) > 0 && strings.HasPrefix(request.Messages[0].Content, "Summarize conversation records") && !summaryFailure {
					fmt.Fprint(w, sseChunk("summary", "")+sseDone)
					return
				}
				w.WriteHeader(400)
				fmt.Fprint(w, overflowJSON)
			}))
			defer server.Close()
			session := New(testConfig(server.URL, nil))
			before := []Message{{Role: "user", Content: "old"}, {Role: "assistant", Content: "old result"}}
			session.SetHistory(before)
			_, err := session.Run(context.Background(), "latest", nil)
			if !errors.Is(err, ErrContextLengthExceeded) || hits.Load() > 6 {
				t.Fatalf("err=%v hits=%d", err, hits.Load())
			}
			got := session.History()
			if got[len(got)-1].Content != "latest" {
				t.Fatal("overflow lost latest request")
			}
			if summaryFailure && !reflect.DeepEqual(got, append(before, Message{Role: "user", Content: "latest"})) {
				t.Fatal("failed summary changed history")
			}
		})
	}
}

func TestCompactPartialSummaryOverflowDoesNotRetry(t *testing.T) {
	server, hits := newFakeProvider(t, sseChunk("partial summary", "")+overflowSSE)
	session := New(testConfig(server.URL, nil))
	before := []Message{{Role: "user", Content: strings.Repeat("old context ", 600)}, {Role: "assistant", Content: "completed"}, {Role: "user", Content: "latest"}}
	session.SetHistory(before)
	if _, err := session.Compact(t.Context(), ""); err == nil {
		t.Fatal("expected partial summary failure")
	}
	if hits.Load() != 1 || !reflect.DeepEqual(session.History(), before) {
		t.Fatal("partial summary was retried or committed")
	}
}

func TestCompactLateFailureKeepsEntireOriginalHistory(t *testing.T) {
	server, hits := newFakeProvider(t, sseChunk("first fragment summary", "")+sseDone, `data: {"error":{"type":"invalid_request_error","message":"invalid schema"}}`+"\n\n", `data: {"error":{"type":"invalid_request_error","message":"invalid schema"}}`+"\n\n")
	session := New(testConfig(server.URL, nil))
	before := []Message{{Role: "user", Content: strings.Repeat("large old context ", 3000)}, {Role: "assistant", Content: "result"}, {Role: "user", Content: "latest"}}
	session.SetHistory(before)
	if _, err := session.Compact(t.Context(), ""); err == nil {
		t.Fatal("expected later fragment failure")
	}
	if hits.Load() < 2 || !reflect.DeepEqual(session.History(), before) {
		t.Fatal("earlier successful fragments changed original history")
	}
}

func TestContextRecoverySingleOversizedToolGroup(t *testing.T) {
	server, hits := newFakeProvider(t,
		sseChunk("", "", sseToolCall("only-call", "shell", `{}`))+sseDone,
		overflowSSE,
		sseChunk("User requested task; shell operation completed once with result tool output.", "")+sseDone,
		sseChunk("done", "")+sseDone,
	)
	session := New(testConfig(server.URL, []Tool{&fakeTool{name: "shell"}}))
	emit, events := collectEvents()
	text, err := session.Run(t.Context(), "latest request", emit)
	if err != nil || text != "done" || hits.Load() != 4 {
		t.Fatalf("text=%q err=%v hits=%d", text, err, hits.Load())
	}
	if len(eventsOfType(*events, EventToolStart)) != 1 || session.History()[2].Content != "latest request" {
		t.Fatal("tool duplicated or request lost")
	}
}

func TestGenericInvalidRequestDoesNotTriggerCompaction(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"type":"invalid_request_error","message":"invalid tool schema"}}`)
	}))
	defer server.Close()
	session := New(testConfig(server.URL, nil))
	before := []Message{{Role: "user", Content: "old"}, {Role: "assistant", Content: "old result"}}
	session.SetHistory(before)
	revision := session.HistoryRevision()
	_, err := session.Run(t.Context(), "new", nil)
	if err == nil || errors.Is(err, ErrContextLengthExceeded) || hits.Load() != 1 {
		t.Fatalf("err=%v hits=%d", err, hits.Load())
	}
	if !reflect.DeepEqual(session.History(), before) || session.HistoryRevision() != revision || session.TurnHistoryIndex() != -1 {
		t.Fatal("generic error triggered compaction or broke rollback index")
	}
}

func TestCompactionRequestBudgetStopsBeforeProviderCall(t *testing.T) {
	worker := compactWorker{requests: compactMaxRequests}
	if _, err := worker.call(t.Context(), "records", nil); err == nil || !strings.Contains(err.Error(), "request limit") {
		t.Fatalf("err=%v", err)
	}
}

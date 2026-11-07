package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func finishChunk(reason string) string {
	data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{
		"delta": map[string]any{}, "finish_reason": reason,
	}}})
	return "data: " + string(data) + "\n\n"
}

func TestReadStreamFinishReasons(t *testing.T) {
	for _, reason := range []string{"stop", "STOP", "end", "tool_calls", "function_call",
		"length", "MAX_TOKENS", "content_filter", "network_error", "error",
		"insufficient_system_resource", "unknown", "other", "unspecified", "unexpected"} {
		t.Run(reason, func(t *testing.T) {
			msg, _, err := readStream(strings.NewReader(sseChunk("partial", "")+finishChunk(reason)+sseDone), nil, nil, nil)
			success := reason == "stop" || reason == "STOP" || reason == "end" || reason == "tool_calls" || reason == "function_call"
			if (err == nil) != success || msg.Content != "partial" {
				t.Fatalf("message=%+v error=%v success=%v", msg, err, success)
			}
		})
	}
}

func TestRunTurnFailedStreamPreservesOutputWithoutPendingCalls(t *testing.T) {
	for _, failure := range []string{"cancel", "eof", "length", "content_filter", "upstream"} {
		t.Run(failure, func(t *testing.T) {
			var requests atomic.Int32
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if requests.Add(1) > 1 {
					var req chatRequest
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						t.Error(err)
					}
					found := false
					for _, msg := range req.Messages {
						if len(msg.ToolCalls) != 0 {
							t.Error("next request contains an unexecuted tool call")
						}
						if msg.Role == "assistant" && msg.Content == "partial" {
							found = true
						}
					}
					if !found {
						t.Error("next request lost partial answer")
					}
					fmt.Fprint(w, sseChunk("recovered", "")+sseDone)
					return
				}
				// A separate first chunk ensures the pending call was parsed
				// before the text callback cancels the stream.
				fmt.Fprint(w, sseChunk("", "", sseToolCall("pending", "probe", "{")))
				fmt.Fprint(w, sseChunk("partial", "thinking"))
				if failure == "cancel" {
					w.(http.Flusher).Flush()
					<-r.Context().Done()
				} else if failure == "upstream" {
					fmt.Fprint(w, "data: {\"error\":{\"message\":\"upstream failed\"}}\n\n")
				} else if failure != "eof" {
					fmt.Fprint(w, finishChunk(failure)+sseDone)
				}
			}))
			defer srv.Close()
			tool := &fakeTool{name: "probe"}
			sess := New(testConfig(srv.URL, []Tool{tool}))
			var events []Event
			_, err := sess.RunTurn(ctx, "hello", nil, "", func(e Event) {
				events = append(events, e)
				if failure == "cancel" && e.Type == EventTextDelta {
					cancel()
				}
			})
			if err == nil || tool.ran || len(eventsOfType(events, EventDone)) != 0 {
				t.Fatalf("failed turn: error=%v tool ran=%v events=%+v", err, tool.ran, events)
			}
			history := sess.History()
			if len(history) != 2 || history[1].Content != "partial" || history[1].Reasoning != "thinking" || len(history[1].ToolCalls) != 0 {
				t.Fatalf("invalid persisted history: %+v", history)
			}
			if answer, err := sess.Run(context.Background(), "continue", nil); err != nil || answer != "recovered" {
				t.Fatalf("next turn: answer=%q error=%v", answer, err)
			}
			if requests.Load() != 2 {
				t.Fatalf("partial output was retried: %d requests", requests.Load())
			}
		})
	}
}

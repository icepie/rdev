package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestCompactPreservesLatestCompleteToolTurn(t *testing.T) {
	provider, _ := newFakeProvider(t, sseChunk("Goal: fix deployment. Pending: verify device build.", "")+sseDone)
	sess := New(Config{BaseURL: provider.URL, Model: "test"})
	recent := []Message{{Role: "user", Content: "Verify build"}, {Role: "assistant", ToolCalls: []ToolCall{{ID: "call-1", Name: "shell", Arguments: `{"command":"build"}`}}}, {Role: "tool", ToolCallID: "call-1", Content: "passed"}, {Role: "assistant", Content: "Build passed"}}
	sess.SetHistory(append([]Message{{Role: "user", Content: "Fix deployment"}, {Role: "assistant", Content: "Updated config"}}, recent...))
	if _, err := sess.Compact(context.Background(), "Keep verification details"); err != nil {
		t.Fatal(err)
	}
	got := sess.History()
	if len(got) != 6 || !reflect.DeepEqual(got[2:], recent) {
		t.Fatalf("recent tool turn changed: %+v", got)
	}
	if got[0].Role != "user" || got[1].Role != "assistant" {
		t.Fatalf("invalid summary boundary: %+v", got[:2])
	}
}

func TestCompactFailurePreservesHistory(t *testing.T) {
	provider, _ := newFakeProvider(t, sseChunk("", "thinking")+sseDone)
	sess := New(Config{BaseURL: provider.URL, Model: "test"})
	before := []Message{{Role: "user", Content: "first"}, {Role: "assistant", Content: "answer"}, {Role: "user", Content: "second"}}
	sess.SetHistory(before)
	if _, err := sess.Compact(context.Background(), ""); err == nil {
		t.Fatal("expected empty summary error")
	}
	if !reflect.DeepEqual(sess.History(), before) {
		t.Fatal("failed compaction changed conversation")
	}
}

func TestCompactDoesNotOverwriteResetOrCanceledConversation(t *testing.T) {
	for _, action := range []string{"reset", "cancel"} {
		t.Run(action, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var sess *Session
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Tools    []json.RawMessage `json:"tools"`
					Messages []Message         `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if len(request.Tools) != 0 {
					t.Error("compaction must not expose execution tools")
				}
				for _, m := range request.Messages {
					if m.Content == "latest user request" {
						t.Error("latest turn should be retained verbatim, not summarized")
					}
				}
				if action == "reset" {
					sess.Reset()
				} else {
					cancel()
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, sseChunk("stale summary", "")+sseDone)
			}))
			defer provider.Close()
			sess = New(Config{BaseURL: provider.URL, Model: "test", Tools: []Tool{&fakeTool{name: "shell"}}})
			before := []Message{{Role: "user", Content: "earlier request"}, {Role: "assistant", Content: "earlier response"}, {Role: "user", Content: "latest user request"}}
			sess.SetHistory(before)
			if _, err := sess.Compact(ctx, ""); err == nil {
				t.Fatal("expected compaction to abort")
			}
			got := sess.History()
			if action == "reset" {
				if len(got) != 0 {
					t.Fatal("summary resurrected a reset conversation")
				}
			} else if !reflect.DeepEqual(got, before) {
				t.Fatal("cancellation changed conversation")
			}
		})
	}
}

func TestCompactSingleLongTurnKeepsToolCallResultPairs(t *testing.T) {
	provider, _ := newFakeProvider(t, sseChunk("Task goal, completed steps and pending checks.", "")+sseDone)
	session := New(Config{BaseURL: provider.URL, Model: "test"})
	history := []Message{{Role: "user", Content: "Complete this large task"}}
	for i := range 6 {
		id := fmt.Sprintf("tool-%d", i)
		history = append(history, Message{Role: "assistant", ToolCalls: []ToolCall{{ID: id, Name: "read_file", Arguments: `{}`}}}, Message{Role: "tool", ToolCallID: id, Content: "read result"})
	}
	history = append(history, Message{Role: "assistant", Content: "Still need verification"})
	session.SetHistory(history)
	if _, err := session.Compact(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	got := session.History()
	if len(got) >= len(history) {
		t.Fatal("long single turn was not compacted")
	}
	outstanding := map[string]bool{}
	for _, message := range got {
		for _, call := range message.ToolCalls {
			outstanding[call.ID] = true
		}
		if message.Role == "tool" {
			if !outstanding[message.ToolCallID] {
				t.Fatalf("orphaned result %q", message.ToolCallID)
			}
			delete(outstanding, message.ToolCallID)
		}
	}
	if len(outstanding) > 0 {
		t.Fatalf("missing tool results: %v", outstanding)
	}
	if got[len(got)-1].Content != "Still need verification" {
		t.Fatal("latest answer was not retained")
	}
}

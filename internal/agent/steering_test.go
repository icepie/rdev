package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestSteeringSettlesToolBatchBeforeInjection(t *testing.T) {
	first := &fakeTool{name: "read"}
	second := &fakeTool{name: "write"}
	var hits atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if hits.Add(1) == 1 {
			a, b := sseToolCall("a", "read", "{}"), sseToolCall("b", "write", "{}")
			b["index"] = 1
			fmt.Fprint(w, sseChunk("working", "", a, b)+sseDone)
			return
		}
		var request struct {
			Messages []Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		messages := request.Messages
		n := len(messages)
		if n < 4 || messages[n-3].ToolCallID != "a" || messages[n-2].ToolCallID != "b" || messages[n-1].Role != "user" || messages[n-1].Content != "new direction" {
			t.Errorf("steering broke tool-result ordering: %+v", messages)
		}
		fmt.Fprint(w, sseChunk("adjusted", "")+sseDone)
	}))
	defer provider.Close()
	session := New(testConfig(provider.URL, []Tool{first, second}))
	var events []Event
	pending := true
	result, err := session.RunTurnWithSteering(t.Context(), "start", nil, "", func(e Event) { events = append(events, e) }, func() *Message {
		if !pending {
			return nil
		}
		if !first.ran || !second.ran {
			t.Fatal("dequeued before the whole tool batch settled")
		}
		pending = false
		return &Message{Role: "user", Content: "new direction", Display: json.RawMessage(`{"steering":true}`)}
	})
	if err != nil || result != "adjusted" || hits.Load() != 2 {
		t.Fatalf("result=%q err=%v requests=%d", result, err, hits.Load())
	}
	accepted := eventsOfType(events, EventUserInput)
	if len(accepted) != 1 || len(eventsOfType(events, EventDone)) != 1 {
		t.Fatalf("unexpected lifecycle: %+v", events)
	}
	history := session.History()
	if string(history[len(history)-2].Display) != `{"steering":true}` {
		t.Fatal("steering metadata missing from history")
	}
}

func TestSteeringRemainsQueuedWhenCanceledOrOutOfRounds(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprint("canceled=", canceled), func(t *testing.T) {
			provider, _ := newFakeProvider(t, sseChunk("done", "")+sseDone)
			cfg := testConfig(provider.URL, nil)
			cfg.MaxTurns = 1
			if canceled {
				cfg.MaxTurns = 3
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			session := New(cfg)
			_, _ = session.RunTurnWithSteering(ctx, "start", nil, "", func(e Event) {
				if canceled && e.Type == EventTextDelta {
					cancel()
				}
			}, func() *Message { t.Fatal("dequeued input without a subsequent model round"); return nil })
		})
	}
}

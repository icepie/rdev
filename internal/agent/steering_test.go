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

func TestCancellationDuringFinalResponseIsNotReportedAsSuccess(t *testing.T) {
	provider, hits := newFakeProvider(t, sseChunk("partial", "")+sseDone)
	defer provider.Close()
	ctx, cancel := context.WithCancel(t.Context())
	session := New(testConfig(provider.URL, nil))
	var events []Event
	result, err := session.Run(ctx, "start", func(event Event) {
		events = append(events, event)
		if event.Type == EventTextDelta {
			cancel()
		}
	})
	if err != context.Canceled || result != "" || hits.Load() != 1 {
		t.Fatalf("result=%q err=%v requests=%d", result, err, hits.Load())
	}
	if len(eventsOfType(events, EventDone)) != 0 {
		t.Fatalf("canceled response emitted done: %+v", events)
	}
	history := session.History()
	if len(history) != 2 || history[1].Role != "assistant" || history[1].Content != "partial" {
		t.Fatalf("partial visible response was not retained: %+v", history)
	}
}

func TestCancellationAtTurnLimitWinsOverLimitError(t *testing.T) {
	call := sseToolCall("c1", "cancel", `{}`)
	provider, hits := newFakeProvider(t, sseChunk("", "", call)+sseDone)
	defer provider.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	tool := &cancelingTool{fakeTool: fakeTool{name: "cancel"}, cancel: cancel}
	cfg := testConfig(provider.URL, []Tool{tool})
	cfg.MaxTurns = 1
	session := New(cfg)
	_, err := session.Run(ctx, "start", func(Event) {})
	if err != context.Canceled || !tool.ran || hits.Load() != 1 {
		t.Fatalf("err=%v ran=%v requests=%d", err, tool.ran, hits.Load())
	}
	history := session.History()
	if len(history) != 3 || len(history[1].ToolCalls) != 1 || history[2].Role != "tool" || history[2].ToolCallID != "c1" {
		t.Fatalf("canceled tool call lost its result pair: %+v", history)
	}
}

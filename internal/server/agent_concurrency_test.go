package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"rdev/internal/agent"
	"sync"
	"testing"
)

func TestBusyConversationDoesNotBlockAnotherThread(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []agent.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if request.Messages[len(request.Messages)-1].Content == "slow" {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer provider.Close()
	defer unblock()
	srv := newPrefsTestServer(t)
	srv.Agent.Providers[0].BaseURL = provider.URL
	entry, err := srv.agentSessionFor(agentServerDeviceID, &ClientConn{ID: agentServerDeviceID})
	if err != nil {
		t.Fatal(err)
	}
	first := entry.threads[entry.current]
	srv.agentSessionsMu.Lock()
	second := srv.newAgentThreadLocked(entry, "second", nil, "", "")
	entry.threads[second.id] = second
	srv.agentSessionsMu.Unlock()
	srv.startAgentTurn(entry, first, "slow", nil)
	waitFor(t, "first provider request", func() bool {
		select {
		case <-entered:
			return true
		default:
			return false
		}
	})
	srv.startAgentTurn(entry, second, "fast", nil)
	waitFor(t, "second conversation completed independently", func() bool { srv.agentSessionsMu.Lock(); defer srv.agentSessionsMu.Unlock(); return !second.running })
	srv.agentSessionsMu.Lock()
	busy := first.running
	srv.agentSessionsMu.Unlock()
	if !busy {
		t.Fatal("first conversation was not still waiting")
	}
	history, _, _, _ := srv.agentStore().load(entry.deviceID, second.id)
	if len(history) != 2 || history[0].Content != "fast" || history[1].Content != "done" {
		t.Fatalf("mixed thread history: %+v", history)
	}
	unblock()
	waitFor(t, "first completed", func() bool { srv.agentSessionsMu.Lock(); defer srv.agentSessionsMu.Unlock(); return !first.running })
}

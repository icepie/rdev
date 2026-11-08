package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestReasoningChangeAppliesAtNextTurn(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(fmt.Sprintf("queued=%t", queued), func(t *testing.T) {
			type request struct {
				Effort   string `json:"reasoning_effort"`
				Messages []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"messages"`
			}
			seen := make(chan request, 2)
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req request
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				seen <- req
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"original answer\"}}]}\n\ndata: [DONE]\n\n")
			}))
			defer provider.Close()
			defer unblock()
			srv := newPrefsTestServer(t)
			srv.Agent.Providers[0].BaseURL = provider.URL
			entry, err := srv.agentSessionFor(agentServerDeviceID, &ClientConn{ID: agentServerDeviceID})
			if err != nil {
				t.Fatal(err)
			}
			th := entry.threads[entry.current]
			handler := &agentWSHandler{srv: srv}
			ac := &agentConn{entry: entry, current: th.id}
			handler.handleChat(ac, "first task", "", "", nil)
			receive := func() request {
				t.Helper()
				select {
				case req := <-seen:
					return req
				case <-time.After(3 * time.Second):
					t.Fatal("provider request missing")
					return request{}
				}
			}
			if got := receive(); got.Effort != "medium" {
				t.Fatalf("first effort=%s", got.Effort)
			}
			srv.agentSessionsMu.Lock()
			original := th.session
			srv.agentSessionsMu.Unlock()
			handler.handleSetReasoningLevel(ac, "low")
			handler.handleSetReasoningLevel(ac, "high")
			srv.agentSessionsMu.Lock()
			unchanged := th.session == original && th.running
			srv.agentSessionsMu.Unlock()
			if !unchanged {
				t.Fatal("setting replaced the in-flight session")
			}
			if queued {
				handler.handleChat(ac, "second task", "", "", nil)
			}
			unblock()
			if !queued {
				waitFor(t, "first turn finished", func() bool { srv.agentSessionsMu.Lock(); defer srv.agentSessionsMu.Unlock(); return !th.running })
				handler.handleChat(ac, "second task", "", "", nil)
			}
			got := receive()
			if got.Effort != "high" {
				t.Fatalf("next request ignored latest setting: %s", got.Effort)
			}
			found := false
			for _, msg := range got.Messages {
				if msg.Role == "assistant" && msg.Content == "original answer" {
					found = true
				}
			}
			if !found {
				t.Fatal("rebuild lost the first turn's answer")
			}
			srv.waitAgentWork()
			if prefs := srv.loadAgentPrefs(entry.deviceID); prefs.ReasoningLevel != "high" {
				t.Fatalf("preference not persisted: %+v", prefs)
			}
		})
	}
}

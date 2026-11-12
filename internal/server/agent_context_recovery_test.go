package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"rdev/internal/agent"
)

func TestContextRecoveryKeepsRunningSnapshotAndPersistsReplacement(t *testing.T) {
	retryStarted := make(chan struct{})
	releaseRetry := make(chan struct{})
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []agent.Message   `json:"messages"`
			Tools    []json.RawMessage `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"code":"context_length_exceeded","message":"Your input exceeds the context window of this model.","param":"input","type":"invalid_request_error"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if len(request.Tools) == 0 {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Earlier task completed; now verify the deployment.\"}}]}\n\ndata: [DONE]\n\n")
			return
		}
		close(retryStarted)
		<-releaseRetry
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Deployment verified.\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer provider.Close()
	released := false
	defer func() {
		if !released {
			close(releaseRetry)
		}
	}()
	srv := newPrefsTestServer(t)
	srv.Agent.Providers[0].BaseURL = provider.URL
	entry, err := srv.agentSessionFor(agentServerDeviceID, &ClientConn{ID: agentServerDeviceID})
	if err != nil {
		t.Fatal(err)
	}
	th := entry.threads[entry.current]
	original := []agent.Message{{Role: "user", Content: "Deploy the application"}, {Role: "assistant", Content: strings.Repeat("Earlier completed deployment details. ", 300)}}
	th.session.SetHistory(original)
	srv.agentStore().save(entry.deviceID, th.id, th.model, original)
	handler := &agentWSHandler{srv: srv}
	handler.handleChat(&agentConn{deviceID: entry.deviceID, entry: entry, current: th.id}, "Verify the deployment", "", "", nil)
	waitFor(t, "recovered provider request", func() bool {
		select {
		case <-retryStarted:
			return true
		default:
			return false
		}
	})
	srv.agentSessionsMu.Lock()
	snapshot := srv.threadSnapshotLocked(entry, th)
	srv.agentSessionsMu.Unlock()
	if !reflect.DeepEqual(snapshot.history, renderAgentHistory(original)) {
		t.Fatal("running reconnect history changed during context recovery")
	}
	close(releaseRetry)
	released = true
	waitFor(t, "recovered turn finished", func() bool { srv.agentSessionsMu.Lock(); defer srv.agentSessionsMu.Unlock(); return !th.running })
	srv.agentSessionsMu.Lock()
	live := th.live.Snapshot()
	var terminal agentMsg
	err = json.Unmarshal(live[len(live)-1], &terminal)
	expected := th.session.History()
	frozenReleased := th.turnHistory == nil
	srv.agentSessionsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Op != "done" || !terminal.Compacted {
		t.Fatalf("terminal=%+v", terminal)
	}
	if !frozenReleased {
		t.Fatal("completed turn retained frozen transcript")
	}
	loaded, _, _, _ := srv.agentStore().load(entry.deviceID, th.id)
	if !reflect.DeepEqual(loaded, expected) || !reflect.DeepEqual(terminal.History, renderAgentHistory(loaded)) {
		t.Fatal("persisted, terminal and reconnect histories differ")
	}
	found := false
	for _, m := range loaded {
		if m.Role == "user" && m.Content == "Verify the deployment" {
			found = true
			if len(m.Display) == 0 {
				t.Fatal("recovered user request lost turn metadata")
			}
		}
	}
	if !found {
		t.Fatal("recovery lost latest user request")
	}
}

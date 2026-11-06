package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"rdev/internal/agent"
	"reflect"
	"strings"
	"testing"
)

func TestSplitAgentCommandAcceptsComposerWhitespace(t *testing.T) {
	for _, tc := range []struct{ input, command, argument string }{
		{"/compact", "/compact", ""},
		{" /COMPACT\tkeep device IDs\n ", "/compact", "keep device IDs"},
		{"/compact\nkeep errors", "/compact", "keep errors"},
		{"/compactly explain", "/compactly", "explain"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			command, argument := splitAgentCommand(tc.input)
			if command != tc.command || argument != tc.argument {
				t.Fatalf("got (%q, %q), want (%q, %q)", command, argument, tc.command, tc.argument)
			}
		})
	}
}

// Exercise the actual turn boundary: the compact command must replace the
// persisted context and publish the same history that reconnect will replay.
func TestCompactCommandPersistsAndPublishesReplacement(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Tools    []json.RawMessage `json:"tools"`
			Messages []agent.Message   `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if len(request.Tools) != 0 {
			t.Error("compaction exposed executable tools")
		}
		if len(request.Messages) == 0 || !strings.Contains(request.Messages[len(request.Messages)-1].Content, "retain paths") {
			t.Error("compaction focus was not sent")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Goal and file paths retained\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer provider.Close()
	srv := newPrefsTestServer(t)
	srv.Agent.Providers[0].BaseURL = provider.URL
	entry, err := srv.agentSessionFor(agentServerDeviceID, &ClientConn{ID: agentServerDeviceID})
	if err != nil {
		t.Fatal(err)
	}
	thread := entry.threads[entry.current]
	original := []agent.Message{{Role: "user", Content: "first task"}, {Role: "assistant", Content: "first result"}, {Role: "user", Content: "latest request"}, {Role: "assistant", Content: "latest result"}}
	thread.session.SetHistory(original)
	srv.agentStore().save(entry.deviceID, thread.id, thread.model, original)
	handler := &agentWSHandler{srv: srv}
	handler.handleChat(&agentConn{deviceID: entry.deviceID, entry: entry, current: thread.id}, "/compact retain paths", "", "", nil)
	waitFor(t, "compaction turn finished", func() bool { srv.agentSessionsMu.Lock(); defer srv.agentSessionsMu.Unlock(); return !thread.running })
	srv.agentSessionsMu.Lock()
	var terminal agentMsg
	err = json.Unmarshal(thread.live[len(thread.live)-1], &terminal)
	expected := thread.session.History()
	srv.agentSessionsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Op != "done" || !terminal.Compacted {
		t.Fatalf("terminal: %+v", terminal)
	}
	loaded, _, _, _ := srv.agentStore().load(entry.deviceID, thread.id)
	if !reflect.DeepEqual(loaded, expected) {
		t.Fatal("reconnect would load stale history")
	}
	if !reflect.DeepEqual(terminal.History, renderAgentHistory(loaded)) {
		t.Fatal("live client and reconnect histories differ")
	}
}

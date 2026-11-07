package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQueueIdentityRejectsStaleAndCrossThreadRemoval(t *testing.T) {
	srv := newTurnTestServer(t, "http://127.0.0.1:1")
	entry, err := srv.agentSessionFor("dev1", srv.clients["dev1"])
	if err != nil {
		t.Fatal(err)
	}
	th := entry.threads[entry.current]
	srv.agentSessionsMu.Lock()
	th.queue = []agentQueuedChat{{text: "same text"}, {text: "same text"}, {text: "last"}}
	srv.agentSessionsMu.Unlock()
	server := httptest.NewServer(http.HandlerFunc(srv.HandleAgentWS))
	defer server.Close()
	socket, rec := dialAgentWS(t, server)
	auth := waitAgentMsg(t, rec.msgs, "auth", func(m agentMsg) bool { return m.Op == "auth_ok" })
	if len(auth.QueueIDs) != 3 || auth.QueueIDs[0] == auth.QueueIDs[1] {
		t.Fatalf("missing stable identities: %+v", auth.QueueIDs)
	}
	// Simulate the worker admitting the first item before a browser click.
	srv.agentSessionsMu.Lock()
	th.queue = th.queue[1:]
	srv.agentSessionsMu.Unlock()
	request := func(id, queueID, thread string) agentMsg {
		raw, _ := json.Marshal(agentMsg{Op: "dequeue", ID: id, QueueID: queueID, Thread: thread, Index: 0})
		sendAgentOp(t, socket, string(raw))
		return waitAgentMsg(t, rec.msgs, id, func(m agentMsg) bool { return m.Op == "dequeued" && m.ID == id })
	}
	stale := request("stale", auth.QueueIDs[0], th.id)
	if stale.Removed || stale.Text != "" {
		t.Fatal("stale row removed its replacement")
	}
	// Matching text at the same index must not act as an identity fallback.
	current := waitAgentMsg(t, rec.msgs, "refresh", func(m agentMsg) bool { return m.Op == "queue" })
	if len(current.QueueIDs) != 2 || current.QueueIDs[0] != auth.QueueIDs[1] {
		t.Fatalf("queue shifted identity: %+v", current.QueueIDs)
	}
	cross := request("wrong-thread", auth.QueueIDs[1], "not-this-thread")
	if cross.Removed {
		t.Fatal("identity escaped its thread")
	}
	removed := request("take-back", auth.QueueIDs[1], th.id)
	if !removed.Removed || removed.Text != "same text" || removed.Thread != th.id {
		t.Fatalf("wrong acknowledgement: %+v", removed)
	}
	duplicate := request("duplicate", auth.QueueIDs[1], th.id)
	if duplicate.Removed {
		t.Fatal("duplicate request removed another message")
	}
	srv.agentSessionsMu.Lock()
	defer srv.agentSessionsMu.Unlock()
	if len(th.queue) != 1 || th.queue[0].text != "last" {
		t.Fatalf("wrong remaining queue: %+v", th.queue)
	}
}

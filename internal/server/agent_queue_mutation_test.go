package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"rdev/internal/agent"
	"reflect"
	"testing"
)

func TestQueueEditAndReorderPreserveIdentityAndRejectStaleWrites(t *testing.T) {
	srv := newTurnTestServer(t, "http://127.0.0.1:1")
	entry, err := srv.agentSessionFor("dev1", srv.clients["dev1"])
	if err != nil {
		t.Fatal(err)
	}
	th := entry.threads[entry.current]
	th.running = true
	th.queue = []agentQueuedChat{{text: "first", images: []agent.MessageImage{{Name: "original.png", Data: "private"}}}, {text: "second"}}
	server := httptest.NewServer(http.HandlerFunc(srv.HandleAgentWS))
	defer server.Close()
	socket, rec := dialAgentWS(t, server)
	auth := waitAgentMsg(t, rec.msgs, "auth", func(m agentMsg) bool { return m.Op == "auth_ok" })
	request := func(msg agentMsg) agentMsg {
		msg.Thread = th.id
		msg.ID = "test"
		raw, _ := json.Marshal(msg)
		sendAgentOp(t, socket, string(raw))
		return waitAgentMsg(t, rec.msgs, "mutation", func(m agentMsg) bool { return m.Op == "queue_updated" })
	}
	original := "first"
	if ack := request(agentMsg{Op: "update_queue", QueueID: auth.QueueIDs[0], ExpectedText: &original, Text: "edited"}); ack.IsError {
		t.Fatal(ack.Message)
	}
	if ack := request(agentMsg{Op: "update_queue", QueueID: auth.QueueIDs[0], ExpectedText: &original, Text: "stale"}); !ack.IsError {
		t.Fatal("stale edit accepted")
	}
	if ack := request(agentMsg{Op: "reorder_queue", QueueID: auth.QueueIDs[1], BeforeID: auth.QueueIDs[0], ExpectedQueueIDs: auth.QueueIDs}); ack.IsError {
		t.Fatal(ack.Message)
	}
	if ack := request(agentMsg{Op: "reorder_queue", QueueID: auth.QueueIDs[1], BeforeID: auth.QueueIDs[0], ExpectedQueueIDs: auth.QueueIDs}); !ack.IsError {
		t.Fatal("stale ordering accepted")
	}
	srv.agentSessionsMu.Lock()
	if !reflect.DeepEqual(queueIDs(th.queue), []string{auth.QueueIDs[1], auth.QueueIDs[0]}) || th.queue[1].text != "edited" || th.queue[1].images[0].Data != "private" {
		t.Fatal("queue identity or attachment lost")
	}
	takeQueuedChat(&th.queue, 1)
	srv.agentSessionsMu.Unlock()
	edited := "edited"
	if ack := request(agentMsg{Op: "update_queue", QueueID: auth.QueueIDs[0], ExpectedText: &edited, Text: "resurrect"}); !ack.IsError {
		t.Fatal("claimed item recreated")
	}
}

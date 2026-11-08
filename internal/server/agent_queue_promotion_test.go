package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"rdev/internal/agent"
	"reflect"
	"strings"
	"testing"
)

func TestQueuePromotionIdentityFIFOAndSubscribers(t *testing.T) {
	srv := newTurnTestServer(t, "http://127.0.0.1:1")
	entry, err := srv.agentSessionFor("dev1", srv.clients["dev1"])
	if err != nil {
		t.Fatal(err)
	}
	th := entry.threads[entry.current]
	srv.agentSessionsMu.Lock()
	th.running = true
	th.steeringWake = make(chan struct{})
	wake := th.steeringWake
	th.queue = []agentQueuedChat{
		{text: "older guidance", steering: true},
		{text: "same text"},
		{text: "same text", images: []agent.MessageImage{{Name: "original.png", Data: "original-private-bytes"}}},
		{text: "/bg echo command"},
		{text: "different model", model: "other/model"},
		{text: "job result", display: json.RawMessage(`{"job":{}}`)},
	}
	srv.agentSessionsMu.Unlock()
	server := httptest.NewServer(http.HandlerFunc(srv.HandleAgentWS))
	defer server.Close()
	socket, rec := dialAgentWS(t, server)
	auth := waitAgentMsg(t, rec.msgs, "auth", func(m agentMsg) bool { return m.Op == "auth_ok" })
	_, other := dialAgentWS(t, server)
	waitAgentMsg(t, other.msgs, "other auth", func(m agentMsg) bool { return m.Op == "auth_ok" })
	if !reflect.DeepEqual(auth.QueueModes, []string{"steer", "follow_up", "follow_up", "command", "model", "notification"}) {
		t.Fatal(auth.QueueModes)
	}
	request := func(id, thread string) agentMsg {
		raw, _ := json.Marshal(agentMsg{Op: "promote_queue", Thread: thread, QueueID: id})
		sendAgentOp(t, socket, string(raw))
		return waitAgentMsg(t, rec.msgs, "promotion", func(m agentMsg) bool { return m.Op == "queue_promoted" && m.QueueID == id })
	}
	for _, id := range []string{auth.QueueIDs[3], auth.QueueIDs[4], auth.QueueIDs[5], "missing", ""} {
		if !request(id, th.id).IsError {
			t.Fatal("invalid promotion accepted", id)
		}
	}
	if !request(auth.QueueIDs[2], "other-thread").IsError {
		t.Fatal("cross-thread promotion accepted")
	}
	if ack := request(auth.QueueIDs[2], th.id); ack.IsError {
		t.Fatal(ack.Message)
	}
	select {
	case <-wake:
	default:
		t.Fatal("passive job wait not woken")
	}
	expected := []string{auth.QueueIDs[0], auth.QueueIDs[2], auth.QueueIDs[1], auth.QueueIDs[3], auth.QueueIDs[4], auth.QueueIDs[5]}
	updated := waitAgentMsg(t, other.msgs, "subscriber update", func(m agentMsg) bool { return m.Op == "queue" && reflect.DeepEqual(m.QueueIDs, expected) })
	raw, _ := json.Marshal(updated)
	if strings.Contains(string(raw), "original-private-bytes") {
		t.Fatal("queue leaked original image")
	}
	if updated.QueueModes[1] != "steer" {
		t.Fatal(updated.QueueModes)
	}
	if request(auth.QueueIDs[2], th.id).IsError {
		t.Fatal("repeat promotion not idempotent")
	}
	if request(auth.QueueIDs[1], th.id).IsError {
		t.Fatal("second promotion failed")
	}
	srv.agentSessionsMu.Lock()
	got := queueIDs(th.queue)
	image := th.queue[1].images[0].Data
	takeQueuedChat(&th.queue, 1)
	srv.agentSessionsMu.Unlock()
	if !reflect.DeepEqual(got, expected) || image != "original-private-bytes" {
		t.Fatal("promotion reordered existing guidance or lost image", got)
	}
	if !request(auth.QueueIDs[2], th.id).IsError {
		t.Fatal("consumed message promoted a replacement")
	}
	srv.agentSessionsMu.Lock()
	th.running = false
	srv.agentSessionsMu.Unlock()
	if !request(auth.QueueIDs[1], th.id).IsError {
		t.Fatal("idle promotion accepted")
	}
	srv.agentSessionsMu.Lock()
	th.running = true
	th.turnCommand = true
	srv.agentSessionsMu.Unlock()
	if !request(auth.QueueIDs[1], th.id).IsError {
		t.Fatal("command turn accepted promotion")
	}
}

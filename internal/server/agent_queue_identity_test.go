package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"rdev/internal/agent"
	"strings"
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
	images := []agent.MessageImage{{Name: "original.png", Mime: "image/png", Data: "b3JpZ2luYWw="}}
	th.queue = []agentQueuedChat{{text: "same text"}, {text: "same text", images: images}, {text: "last", images: images}}
	srv.agentSessionsMu.Unlock()
	server := httptest.NewServer(http.HandlerFunc(srv.HandleAgentWS))
	defer server.Close()
	socket, rec := dialAgentWS(t, server)
	auth := waitAgentMsg(t, rec.msgs, "auth", func(m agentMsg) bool { return m.Op == "auth_ok" })
	if len(auth.QueueIDs) != 3 || auth.QueueIDs[0] == auth.QueueIDs[1] {
		t.Fatalf("missing stable identities: %+v", auth.QueueIDs)
	}
	rawAuth, _ := json.Marshal(auth)
	if strings.Contains(string(rawAuth), images[0].Data) {
		t.Fatal("queue snapshot exposed original image bytes")
	}
	// Simulate the worker admitting the first item before a browser click.
	srv.agentSessionsMu.Lock()
	th.queue = th.queue[1:]
	srv.agentSessionsMu.Unlock()
	request := func(id, queueID, thread string) agentMsg {
		raw, _ := json.Marshal(agentMsg{Op: "dequeue", ID: id, QueueID: queueID, Thread: thread, Index: 0, Restore: id == "take-back"})
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
	var restored []agent.MessageImage
	if err := json.Unmarshal(removed.Images, &restored); err != nil || len(restored) != 1 || restored[0].Data != images[0].Data {
		t.Fatalf("lost original image: %s %v", removed.Images, err)
	}
	duplicate := request("duplicate", auth.QueueIDs[1], th.id)
	if duplicate.Removed {
		t.Fatal("duplicate request removed another message")
	}
	srv.agentSessionsMu.Lock()
	remaining := queueTexts(th.queue)
	srv.agentSessionsMu.Unlock()
	if len(remaining) != 1 || remaining[0] != "last" {
		t.Fatalf("wrong remaining queue: %+v", remaining)
	}
	dropped := request("drop", auth.QueueIDs[2], th.id)
	if !dropped.Removed || len(dropped.Images) != 0 {
		t.Fatal("plain removal returned original image bytes")
	}
}

func TestTakingQueuedChatReleasesImageReferences(t *testing.T) {
	items := []agentQueuedChat{{text: "first", images: []agent.MessageImage{{Data: "large-original"}}}, {text: "keep"}}
	queue := items
	taken := takeQueuedChat(&queue, 0)
	if taken.images[0].Data != "large-original" || len(queue) != 1 || queue[0].text != "keep" {
		t.Fatal("incorrect queue removal")
	}
	if items[0].text != "" || items[0].images != nil {
		t.Fatal("vacated slot retained content")
	}
	takeQueuedChat(&queue, 0)
	if items[1].images != nil || items[1].text != "" {
		t.Fatal("empty queue retained the last slot")
	}
	items = []agentQueuedChat{{text: "keep"}, {images: []agent.MessageImage{{Data: "removed-original"}}}, {images: []agent.MessageImage{{Data: "remaining-original"}}}}
	queue = items
	takeQueuedChat(&queue, 1)
	if len(queue) != 2 || queue[1].images[0].Data != "remaining-original" || items[2].images != nil {
		t.Fatal("middle removal retained the vacated slot or corrupted its successor")
	}
}

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"rdev/internal/agent"
)

func TestRPCRemoveQueuedMessage(t *testing.T) {
	srv := newTurnTestServer(t, "http://127.0.0.1:1")
	entry, err := srv.agentSessionFor("dev1", srv.clients["dev1"])
	if err != nil {
		t.Fatal(err)
	}
	th := entry.threads[entry.current]
	images := []agent.MessageImage{{Name: "original.png", Mime: "image/png", Data: "b3JpZ2luYWw="}}
	srv.agentSessionsMu.Lock()
	th.queue = []agentQueuedChat{
		{text: "same", steering: true}, {text: "same"}, {text: "same", images: images},
		{text: "", steering: true, images: images}, {text: "notification", display: json.RawMessage(`{"job":{}}`)},
	}
	foreign := srv.newAgentThreadLocked(entry, newAgentThreadID(), nil, "", "")
	entry.threads[foreign.id] = foreign
	srv.agentSessionsMu.Unlock()
	server := httptest.NewServer(http.HandlerFunc(srv.HandleAgentWS))
	defer server.Close()
	socket, rec := dialAgentWS(t, server)
	auth := waitAgentMsg(t, rec.msgs, "auth", func(m agentMsg) bool { return m.Op == "auth_ok" })
	observer, other := dialAgentWS(t, server)
	waitAgentMsg(t, other.msgs, "observer auth", func(m agentMsg) bool { return m.Op == "auth_ok" })
	call := func(fields map[string]any, success bool) rpcRemoveQueuedResult {
		t.Helper()
		fields["type"] = "remove_queued_message"
		fields["id"] = "remove"
		raw, _ := json.Marshal(fields)
		sendAgentOp(t, socket, string(raw))
		reply := waitAgentMsg(t, rec.msgs, "removal", func(m agentMsg) bool { return m.Type == "response" })
		if reply.ID != "remove" || reply.Command != "remove_queued_message" || reply.Success == nil || *reply.Success != success {
			t.Fatalf("wrong response: %+v", reply)
		}
		if !success {
			if reply.Code != "invalid_request" {
				t.Fatal(reply)
			}
			return rpcRemoveQueuedResult{}
		}
		var result rpcRemoveQueuedResult
		if err := json.Unmarshal(reply.Data, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	for _, fields := range []map[string]any{
		{"message": "same", "queue": "wrong"}, {"queue": "steering"}, {"message": nil, "queue": "steering"},
		{"message": "same", "queue": "followUp", "queueId": ""}, {"message": "same", "queue": "followUp", "thread": "missing"},
	} {
		call(fields, false)
	}
	if call(map[string]any{"message": "same", "queue": "followUp", "thread": foreign.id, "queueId": auth.QueueIDs[2]}, true).Removed {
		t.Fatal("cross-thread removal")
	}
	if call(map[string]any{"message": "same", "queue": "steering", "queueId": auth.QueueIDs[2]}, true).Removed {
		t.Fatal("cross-queue removal")
	}
	if !call(map[string]any{"message": "same", "queue": "steering"}, true).Removed {
		t.Fatal("native text matching failed")
	}
	if call(map[string]any{"message": "notification", "queue": "followUp"}, true).Removed {
		t.Fatal("removed system notification")
	}
	result := call(map[string]any{"message": "same", "queue": "followUp", "queueId": auth.QueueIDs[2]}, true)
	if !result.Removed || !reflect.DeepEqual(result.Images, rpcImageContents(images)) {
		t.Fatalf("lost original attachment: %+v", result)
	}
	if call(map[string]any{"message": "same", "queue": "followUp", "queueId": auth.QueueIDs[2]}, true).Removed {
		t.Fatal("stale ID removed duplicate text")
	}
	result = call(map[string]any{"message": "", "queue": "steering"}, true)
	if !result.Removed || !reflect.DeepEqual(result.Images, rpcImageContents(images)) {
		t.Fatal("cannot restore image-only input")
	}
	// The observer receives queue invalidation, never the private image payload.
	sendAgentOp(t, observer, `{"op":"ping"}`)
	for {
		msg := waitAgentMsg(t, other.msgs, "observer barrier", func(m agentMsg) bool { return true })
		if msg.Type == "response" || len(msg.Images) > 0 {
			t.Fatal("request result leaked to observer")
		}
		if msg.Op == "pong" {
			break
		}
	}
	srv.agentSessionsMu.Lock()
	defer srv.agentSessionsMu.Unlock()
	if got := queueTexts(th.queue); !reflect.DeepEqual(got, []string{"same", "Background task:  ()"}) {
		t.Fatalf("unexpected remaining queue: %v", got)
	}
	if len(foreign.queue) != 0 {
		t.Fatal("foreign queue changed")
	}
}

func TestRPCPromoteQueuedMessage(t *testing.T) {
	srv := newTurnTestServer(t, "http://127.0.0.1:1")
	entry, _ := srv.agentSessionFor("dev1", srv.clients["dev1"])
	th := entry.threads[entry.current]
	srv.agentSessionsMu.Lock()
	th.running = true
	th.queue = []agentQueuedChat{{text: "guidance", steering: true}, {text: "follow"}, {text: "second"}}
	srv.agentSessionsMu.Unlock()
	conn := &agentConn{authed: true, entry: entry, current: th.id, socket: nil}
	h := &agentWSHandler{srv: srv}
	h.handleAgentRPCFrame(conn, []byte(`{"type":"promote_queued_message","id":"p","message":"second"}`))
	srv.agentSessionsMu.Lock()
	defer srv.agentSessionsMu.Unlock()
	if got := queueModes(th); !reflect.DeepEqual(got, []string{"steer", "steer", "follow_up"}) {
		t.Fatalf("wrong promoted modes: %v", got)
	}
	if got := queueTexts(th.queue); !reflect.DeepEqual(got, []string{"guidance", "second", "follow"}) {
		t.Fatalf("wrong promoted order: %v", got)
	}
}

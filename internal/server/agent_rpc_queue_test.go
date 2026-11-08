package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lxzan/gws"

	"rdev/internal/agent"
)

func TestRPCQueueAdmissionAndImages(t *testing.T) {
	srv := newPrefsTestServer(t)
	entry, err := srv.agentSessionFor(agentServerDeviceID, &ClientConn{ID: agentServerDeviceID})
	if err != nil {
		t.Fatal(err)
	}
	th := entry.threads[entry.current]
	// Hold the foreground at a boundary without any provider worker consuming
	// the queue, so admission order and rejected requests can be inspected.
	srv.agentSessionsMu.Lock()
	th.running = true
	th.steeringWake = make(chan struct{})
	wake := th.steeringWake
	other := srv.newAgentThreadLocked(entry, newAgentThreadID(), nil, "", "")
	entry.threads[other.id] = other
	srv.agentSessionsMu.Unlock()
	server := httptest.NewServer(http.HandlerFunc(srv.HandleAgentWS))
	defer server.Close()
	rec := &rawWSRecorder{msgs: make(chan map[string]json.RawMessage, 128)}
	socket, _, err := gws.NewClient(rec, &gws.ClientOption{Addr: "ws" + strings.TrimPrefix(server.URL, "http") + "/agent?device=%40server", HandshakeTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.WriteClose(1000, nil)
	go socket.ReadLoop()
	waitRawMsg(t, rec.msgs, "auth", func(m map[string]json.RawMessage) bool { return string(m["op"]) == `"auth_ok"` })
	call := func(raw string, success bool) {
		t.Helper()
		sendAgentOp(t, socket, raw)
		reply := waitRawMsg(t, rec.msgs, "queue admission", func(m map[string]json.RawMessage) bool {
			return string(m["type"]) == `"response"` || string(m["op"]) == `"error"`
		})
		want := "false"
		if success {
			want = "true"
		}
		if string(reply["success"]) != want || string(reply["id"]) != `"queue"` {
			t.Fatalf("incorrect admission: %s", reply)
		}
		if success && len(reply["data"]) != 0 {
			t.Fatalf("OMP admission should not fabricate result data: %s", reply)
		}
		if !success && string(reply["code"]) != `"invalid_request"` {
			t.Fatalf("missing validation error: %s", reply)
		}
	}
	call(`{"type":"follow_up","id":"queue","message":"first follow-up"}`, true)
	call(`{"type":"steer","id":"queue","message":"first guidance"}`, true)
	select {
	case <-wake:
	default:
		t.Fatal("guidance did not release passive wait")
	}
	// Real PNG: prove OMP mimeType becomes provider mime plus a browser preview.
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	pngData := base64.StdEncoding.EncodeToString(pngBytes.Bytes())
	call(`{"type":"steer","id":"queue","message":"","images":[{"type":"image","mimeType":"image/png","data":"`+pngData+`"}]}`, true)
	call(`{"type":"follow_up","id":"queue","message":"second follow-up"}`, true)
	for _, raw := range []string{
		`{"type":"steer","id":"queue","thread":"missing","message":"bad"}`,
		`{"type":"follow_up","id":"queue","thread":"missing","message":"bad"}`,
		`{"type":"steer","id":"queue","message":"  "}`,
		`{"type":"follow_up","id":"queue","message":null}`,
		`{"type":"follow_up","id":"queue","images":null}`,
		`{"type":"steer","id":"queue","message":"bad image","images":[{"type":"image","mimeType":"image/png","data":"?"}]}`,
		`{"type":"follow_up","id":"queue","message":"bad image","images":[{"type":"text","mimeType":"image/png","data":"YQ=="}]}`,
	} {
		call(raw, false)
	}
	// Barrier detects a second success emitted after a failed browser handler.
	call(`{"type":"follow_up","id":"queue","message":"last follow-up"}`, true)
	srv.agentSessionsMu.Lock()
	defer srv.agentSessionsMu.Unlock()
	if got := queueTexts(th.queue); !reflect.DeepEqual(got, []string{"first guidance", "", "first follow-up", "second follow-up", "last follow-up"}) {
		t.Fatalf("FIFO or rejected-input pollution: %v", got)
	}
	if !reflect.DeepEqual(queueModes(th), []string{"steer", "steer", "follow_up", "follow_up", "follow_up"}) {
		t.Fatal("wrong queue modes")
	}
	if len(other.queue) != 0 || other.running {
		t.Fatal("input crossed threads")
	}
	image := th.queue[1].images
	if len(image) != 1 || image[0].Mime != "image/png" || image[0].Data != pngData || !strings.HasPrefix(image[0].Preview, "data:image/png;base64,") {
		t.Fatalf("image was lost: %+v", image)
	}
}

func TestRPCAbortAndRestoreQueueReturnsUserMessages(t *testing.T) {
	srv := newPrefsTestServer(t)
	entry, err := srv.agentSessionFor(agentServerDeviceID, &ClientConn{ID: agentServerDeviceID})
	if err != nil {
		t.Fatal(err)
	}
	th := entry.threads[entry.current]
	image := []agent.MessageImage{{Name: "queued.png", Mime: "image/png", Data: "cG5n"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.agentSessionsMu.Lock()
	th.running = true
	th.cancel = cancel
	foreign := srv.newAgentThreadLocked(entry, newAgentThreadID(), nil, "", "")
	entry.threads[foreign.id] = foreign
	foreign.queue = []agentQueuedChat{{text: "keep foreign"}}
	th.queue = []agentQueuedChat{{text: "guide", steering: true, display: json.RawMessage(`{"steering":true}`)}, {text: "next", images: image}, {text: "system", display: json.RawMessage(`{"job":{}}`)}}
	srv.agentSessionsMu.Unlock()
	httpSrv := httptest.NewServer(http.HandlerFunc(srv.HandleAgentWS))
	defer httpSrv.Close()
	rec := &agentWSRecorder{msgs: make(chan agentMsg, 64)}
	socket, _, err := gws.NewClient(rec, &gws.ClientOption{Addr: "ws" + strings.TrimPrefix(httpSrv.URL, "http") + "/agent?device=%40server", HandshakeTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	go socket.ReadLoop()
	defer socket.WriteClose(1000, nil)
	waitAgentMsg(t, rec.msgs, "auth", func(m agentMsg) bool { return m.Op == "auth_ok" })
	sendAgentOp(t, socket, `{"type":"abort_and_restore_queue","id":"invalid","thread":"missing"}`)
	invalid := waitAgentMsg(t, rec.msgs, "invalid restore", func(m agentMsg) bool { return m.Type == "response" })
	if invalid.Success == nil || *invalid.Success || ctx.Err() != nil {
		t.Fatalf("invalid restore affected running work: %+v", invalid)
	}
	sendAgentOp(t, socket, `{"type":"abort_and_restore_queue","id":"restore","thread":"`+th.id+`"}`)
	reply := waitAgentMsg(t, rec.msgs, "restore response", func(m agentMsg) bool { return m.Type == "response" && m.ID == "restore" })
	if reply.Success == nil || !*reply.Success {
		t.Fatalf("restore failed: %+v", reply)
	}
	var result rpcAbortRestoreQueueResult
	if err := json.Unmarshal(reply.Data, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Steering) != 1 || result.Steering[0].Text != "guide" || len(result.FollowUp) != 1 || result.FollowUp[0].Text != "next" || len(result.FollowUp[0].Images) != 1 {
		t.Fatalf("unexpected restored queue: %+v", result)
	}
	if !reflect.DeepEqual(result.FollowUp[0].Images, rpcImageContents(image)) || ctx.Err() != context.Canceled {
		t.Fatal("original image or cancellation lost")
	}
	sendAgentOp(t, socket, `{"type":"abort_and_restore_queue","id":"again","thread":"`+th.id+`"}`)
	again := waitAgentMsg(t, rec.msgs, "repeat restore", func(m agentMsg) bool { return m.Type == "response" && m.ID == "again" })
	if string(again.Data) != `{"steering":[],"followUp":[]}` {
		t.Fatalf("restored a message twice: %s", again.Data)
	}
	srv.agentSessionsMu.Lock()
	if len(th.queue) != 0 {
		srv.agentSessionsMu.Unlock()
		t.Fatalf("queue not cleared: %+v", th.queue)
	}
	if len(foreign.queue) != 1 || foreign.queue[0].text != "keep foreign" {
		srv.agentSessionsMu.Unlock()
		t.Fatal("restore crossed threads")
	}
	// An oversized reply must neither lose the queue nor cancel the active turn.
	largeCtx, largeCancel := context.WithCancel(context.Background())
	defer largeCancel()
	th.cancel = largeCancel
	largeText := strings.Repeat("x", 16*1024*1024)
	th.queue = []agentQueuedChat{{text: largeText}}
	srv.agentSessionsMu.Unlock()
	sendAgentOp(t, socket, `{"type":"abort_and_restore_queue","id":"large","thread":"`+th.id+`"}`)
	large := waitAgentMsg(t, rec.msgs, "oversized restore", func(m agentMsg) bool { return m.Type == "response" && m.ID == "large" })
	if large.Success == nil || *large.Success || large.Code != "response_too_large" || largeCtx.Err() != nil {
		t.Fatalf("oversized restore changed the turn: %+v", large)
	}
	srv.agentSessionsMu.Lock()
	defer srv.agentSessionsMu.Unlock()
	if len(th.queue) != 1 || th.queue[0].text != largeText {
		t.Fatal("oversized restore lost queued input")
	}
}

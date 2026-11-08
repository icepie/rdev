package server

import (
	"bytes"
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

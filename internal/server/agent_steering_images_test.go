package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lxzan/gws"
)

func TestImageOnlySteeringReachesProviderLiveReplayAndHistory(t *testing.T) {
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	data := base64.StdEncoding.EncodeToString(pngBytes.Bytes())
	firstStarted, secondStarted := make(chan struct{}), make(chan struct{})
	firstRelease, secondRelease := make(chan struct{}), make(chan struct{})
	var firstOnce, secondOnce sync.Once
	releaseFirst := func() { firstOnce.Do(func() { close(firstRelease) }) }
	releaseSecond := func() { secondOnce.Do(func() { close(secondRelease) }) }
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		n := calls.Add(1)
		if n == 1 {
			close(firstStarted)
			select {
			case <-firstRelease:
			case <-r.Context().Done():
				t.Error("guidance canceled active request")
				return
			}
		}
		if n >= 2 {
			var last json.RawMessage
			for _, message := range request.Messages {
				if message.Role == "user" {
					last = message.Content
				}
			}
			var parts []struct {
				Type     string `json:"type"`
				ImageURL struct {
					URL string `json:"url"`
				} `json:"image_url"`
			}
			if err := json.Unmarshal(last, &parts); err != nil {
				t.Errorf("image guidance was not multimodal: %s", last)
			}
			found := false
			for _, part := range parts {
				if part.Type == "image_url" && part.ImageURL.URL == "data:image/png;base64,"+data {
					found = true
				}
			}
			if !found {
				t.Error("original image did not reach the next provider request")
			}
			if n == 2 {
				close(secondStarted)
				select {
				case <-secondRelease:
				case <-r.Context().Done():
					return
				}
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"checked\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer func() { releaseFirst(); releaseSecond(); provider.Close() }()
	srv := newPrefsTestServer(t)
	t.Cleanup(srv.waitAgentWork)
	srv.Agent.Providers[0].BaseURL = provider.URL
	server := httptest.NewServer(http.HandlerFunc(srv.HandleAgentWS))
	defer server.Close()
	rec := &agentWSRecorder{msgs: make(chan agentMsg, 64)}
	socket, _, err := gws.NewClient(rec, &gws.ClientOption{Addr: "ws" + strings.TrimPrefix(server.URL, "http") + "/agent?device=%40server", HandshakeTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.WriteClose(1000, nil)
	go socket.ReadLoop()
	auth := waitAgentMsg(t, rec.msgs, "auth", func(m agentMsg) bool { return m.Op == "auth_ok" })
	sendAgentOp(t, socket, `{"op":"chat","text":"inspect"}`)
	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first request did not start")
	}
	sendAgentOp(t, socket, `{"op":"steer","text":"invalid","images":[{"name":"bad.png","mime":"image/png","data":"!"}]}`)
	waitAgentMsg(t, rec.msgs, "invalid image rejected", func(m agentMsg) bool { return m.Op == "error" })
	srv.agentSessionsMu.Lock()
	entry := srv.agentSessions[agentServerDeviceID]
	thread := entry.threads[auth.Thread]
	queued := len(thread.queue)
	canceled := thread.turnCtx.Err()
	srv.agentSessionsMu.Unlock()
	if queued != 0 || canceled != nil {
		t.Fatal("invalid image mutated queue or canceled the active turn")
	}
	images, _ := json.Marshal([]agentImage{{Name: "guidance.png", Mime: "image/png", Data: data}})
	payload, _ := json.Marshal(agentMsg{Op: "steer", Thread: auth.Thread, Images: images})
	sendAgentOp(t, socket, string(payload))
	waitAgentMsg(t, rec.msgs, "queued image", func(m agentMsg) bool { return m.Op == "queue" && len(m.QueueIDs) == 1 })
	releaseFirst()
	steering := waitAgentMsg(t, rec.msgs, "live image guidance", func(m agentMsg) bool { return m.Op == "steering" })
	if steering.Text != "" || string(steering.Images) != `["guidance.png"]` || len(steering.ImagePreviews) != 1 || !strings.HasPrefix(steering.ImagePreviews[0], "data:image/png;base64,") {
		t.Fatalf("missing live image preview: %+v", steering)
	}
	select {
	case <-secondStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("guided request did not start")
	}
	srv.agentSessionsMu.Lock()
	frames := thread.live.Snapshot()
	srv.agentSessionsMu.Unlock()
	foundReplay := false
	for _, raw := range frames {
		var frame agentMsg
		if json.Unmarshal(raw, &frame) == nil && frame.Op == "steering" {
			foundReplay = string(frame.Images) == string(steering.Images) && len(frame.ImagePreviews) == 1 && frame.ImagePreviews[0] == steering.ImagePreviews[0]
		}
	}
	if !foundReplay {
		t.Fatal("reconnect replay lost image guidance")
	}
	releaseSecond()
	waitAgentMsg(t, rec.msgs, "done", func(m agentMsg) bool { return m.Op == "done" })
	srv.waitAgentWork()
	loaded, _, _, _ := srv.agentStore().load(agentServerDeviceID, auth.Thread)
	foundHistory := false
	for _, message := range loaded {
		if message.Role == "user" && len(message.Images) == 1 {
			foundHistory = message.Images[0].Name == "guidance.png" && message.Images[0].Data == "" && message.Images[0].Preview == steering.ImagePreviews[0]
		}
	}
	if !foundHistory || calls.Load() != 2 {
		t.Fatalf("image history missing or extra turn: found=%v calls=%d", foundHistory, calls.Load())
	} // A response can finish between the UI click and server admission. Idle
	// steering must fall back to a normal turn without dropping its images.
	sendAgentOp(t, socket, string(payload))
	start := waitAgentMsg(t, rec.msgs, "idle image turn", func(m agentMsg) bool { return m.Op == "turn_start" })
	if string(start.Images) != `["guidance.png"]` || len(start.ImagePreviews) != 1 {
		t.Fatal("idle steering dropped images")
	}
	waitAgentMsg(t, rec.msgs, "idle image done", func(m agentMsg) bool { return m.Op == "done" })
	srv.waitAgentWork()
	if calls.Load() != 3 {
		t.Fatalf("idle steering provider calls=%d", calls.Load())
	}
}

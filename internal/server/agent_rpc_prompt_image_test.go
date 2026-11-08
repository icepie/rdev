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
	"testing"
	"time"

	"github.com/lxzan/gws"
)

func TestRPCPromptImageReachesProvider(t *testing.T) {
	for _, message := range []string{"describe", ""} {
		t.Run(fmt.Sprintf("text=%t", message != ""), func(t *testing.T) {
			got := make(chan json.RawMessage, 1)
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Messages []struct {
						Content json.RawMessage `json:"content"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if len(body.Messages) > 0 {
					got <- body.Messages[len(body.Messages)-1].Content
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
			}))
			defer provider.Close()
			srv := newPrefsTestServer(t)
			srv.Agent.Providers[0].BaseURL = provider.URL
			defer srv.waitAgentWork()
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
			for _, raw := range []string{
				`{"type":"prompt","id":"bad","message":" "}`,
				`{"type":"prompt","id":"bad","message":"describe","images":null}`,
				`{"type":"prompt","id":"bad","images":[{"type":"image","mimeType":"image/png","data":"?"}]}`,
			} {
				sendAgentOp(t, socket, raw)
				reply := waitRawMsg(t, rec.msgs, "invalid prompt", func(m map[string]json.RawMessage) bool { return string(m["type"]) == `"response"` })
				if string(reply["success"]) != "false" || string(reply["code"]) != `"invalid_request"` {
					t.Fatalf("invalid request admitted: %s", reply)
				}
			}
			var data bytes.Buffer
			if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
				t.Fatal(err)
			}
			encoded := base64.StdEncoding.EncodeToString(data.Bytes())
			request, _ := json.Marshal(map[string]any{"type": "prompt", "id": "image", "message": message, "images": []map[string]string{{"type": "image", "mimeType": "image/png", "data": encoded}}})
			sendAgentOp(t, socket, string(request))
			start := waitRawMsg(t, rec.msgs, "image preview", func(m map[string]json.RawMessage) bool { return string(m["op"]) == `"turn_start"` })
			if !strings.Contains(string(start["imagePreviews"]), "data:image/png;base64,") {
				t.Fatalf("missing preview: %s", start)
			}
			reply := waitRawMsg(t, rec.msgs, "prompt completion", func(m map[string]json.RawMessage) bool { return string(m["type"]) == `"response"` })
			if string(reply["success"]) != "true" || string(reply["id"]) != `"image"` {
				t.Fatalf("prompt failed: %s", reply)
			}
			select {
			case content := <-got:
				var parts []struct {
					Type     string `json:"type"`
					Text     string `json:"text"`
					ImageURL struct {
						URL string `json:"url"`
					} `json:"image_url"`
				}
				if err := json.Unmarshal(content, &parts); err != nil {
					t.Fatal(err)
				}
				imageFound, textFound := false, message == ""
				for _, part := range parts {
					if part.Type == "image_url" && part.ImageURL.URL == "data:image/png;base64,"+encoded {
						imageFound = true
					}
					if part.Type == "text" && part.Text == message {
						textFound = true
					}
				}
				if !imageFound || !textFound {
					t.Fatalf("provider lost image/text: %s", content)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("provider request missing")
			}
		})
	}
}

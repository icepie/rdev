package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lxzan/gws"
	"rdev/internal/agent"
)

// Gate the provider so admission cannot be mistaken for successful completion.
// Two real sockets also prove that correlated results stay with their caller.
func TestAgentRPCCompactLifecycle(t *testing.T) {
	for _, outcome := range []string{"success", "provider_error", "reset"} {
		t.Run(outcome, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Messages []agent.Message `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if len(body.Messages) == 0 || !strings.Contains(body.Messages[len(body.Messages)-1].Content, "retain paths") {
					t.Error("missing custom instructions")
				}
				close(entered)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if outcome == "provider_error" {
					fmt.Fprint(w, "data: [DONE]\n\n")
					return
				}
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Paths retained\"}}]}\n\ndata: [DONE]\n\n")
			}))
			defer provider.Close()
			defer unblock()
			srv := newPrefsTestServer(t)
			srv.Agent.Providers[0].BaseURL = provider.URL
			entry, err := srv.agentSessionFor(agentServerDeviceID, &ClientConn{ID: agentServerDeviceID})
			if err != nil {
				t.Fatal(err)
			}
			th := entry.threads[entry.current]
			original := []agent.Message{{Role: "user", Content: "old"}, {Role: "assistant", Content: "answer"}, {Role: "user", Content: "latest"}}
			th.session.SetHistory(original)
			server := httptest.NewServer(http.HandlerFunc(srv.HandleAgentWS))
			defer server.Close()
			connect := func() (*gws.Conn, *rawWSRecorder) {
				t.Helper()
				rec := &rawWSRecorder{msgs: make(chan map[string]json.RawMessage, 128)}
				socket, _, err := gws.NewClient(rec, &gws.ClientOption{Addr: "ws" + strings.TrimPrefix(server.URL, "http") + "/agent?device=%40server", HandshakeTimeout: time.Second})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { socket.WriteClose(1000, nil) })
				go socket.ReadLoop()
				waitRawMsg(t, rec.msgs, "auth", func(m map[string]json.RawMessage) bool { return string(m["op"]) == `"auth_ok"` })
				return socket, rec
			}
			socket, rec := connect()
			observer, other := connect()
			response := func(m map[string]json.RawMessage) bool { return string(m["type"]) == `"response"` }
			for _, raw := range []string{
				`{"type":"compact","id":"bad","customInstructions":42}`,
				`{"type":"compact","id":"bad","thread":"missing"}`,
				`{"type":"compact","id":"bad","op":"reset"}`,
			} {
				sendAgentOp(t, socket, raw)
				reply := waitRawMsg(t, rec.msgs, "invalid request", response)
				if string(reply["success"]) != "false" || string(reply["id"]) != `"bad"` {
					t.Fatalf("invalid request: %s", reply)
				}
			}
			sendAgentOp(t, socket, `{"type":"compact","id":"compact-1","customInstructions":"retain paths"}`)
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("provider not called")
			}
			sendAgentOp(t, socket, `{"type":"compact","id":"busy"}`)
			busy := waitRawMsg(t, rec.msgs, "busy rejection", response)
			if string(busy["id"]) != `"busy"` || string(busy["success"]) != "false" {
				t.Fatalf("premature completion or missing busy rejection: %s", busy)
			}
			if outcome == "reset" {
				sendAgentOp(t, socket, `{"op":"reset"}`)
			} else {
				unblock()
			}
			reply := waitRawMsg(t, rec.msgs, "compact completion", response)
			if string(reply["id"]) != `"compact-1"` || string(reply["command"]) != `"compact"` {
				t.Fatalf("uncorrelated result: %s", reply)
			}
			if outcome == "success" {
				if string(reply["success"]) != "true" || string(reply["data"]) != `{"summary":"Paths retained"}` {
					t.Fatalf("wrong result: %s", reply)
				}
				saved, _, _, _ := srv.agentStore().load(entry.deviceID, th.id)
				if len(saved) != 3 || !strings.Contains(saved[0].Content, "Paths retained") {
					t.Fatalf("response preceded persistence: %+v", saved)
				}
			} else if string(reply["success"]) != "false" || len(reply["error"]) == 0 {
				t.Fatalf("failure reported success: %s", reply)
			}
			// Ordered socket barriers catch duplicate or broadcast RPC responses.
			for _, c := range []struct {
				s *gws.Conn
				r *rawWSRecorder
			}{{socket, rec}, {observer, other}} {
				sendAgentOp(t, c.s, `{"type":"get_subagents","id":"barrier"}`)
				reply := waitRawMsg(t, c.r.msgs, "response barrier", response)
				if string(reply["id"]) != `"barrier"` {
					t.Fatalf("duplicate or leaked response: %s", reply)
				}
			}
		})
	}
}

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lxzan/gws"
)

// Use literal OMP command frames and inspect wire fields, rather than letting
// agentMsg silently discard or substitute incompatible response fields.
func TestSubagentRPCWireContract(t *testing.T) {
	srv := newPrefsTestServer(t)
	entry, err := srv.agentSessionFor(agentServerDeviceID, &ClientConn{ID: agentServerDeviceID})
	if err != nil {
		t.Fatal(err)
	}
	th := entry.threads[entry.current]
	jobs := newAgentJobs(nil)
	th.jobs.Close()
	th.jobs = jobs
	defer jobs.Close()
	release := make(chan struct{})
	defer close(release)
	work := func(ctx context.Context, _ func(string)) (string, error) {
		<-ctx.Done()
		<-release
		return "", ctx.Err()
	}
	guidance := &agentJobGuidance{}
	child, err := jobs.start("subagent", "inspect files", work, guidance)
	if err != nil {
		t.Fatal(err)
	}
	shell, err := jobs.Start("shell", "private shell", work)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(srv.HandleAgentWS))
	defer server.Close()
	rec := &rawWSRecorder{msgs: make(chan map[string]json.RawMessage, 64)}
	socket, _, err := gws.NewClient(rec, &gws.ClientOption{Addr: "ws" + strings.TrimPrefix(server.URL, "http") + "/agent?device=%40server", HandshakeTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.WriteClose(1000, nil)
	go socket.ReadLoop()
	waitRawMsg(t, rec.msgs, "auth", func(m map[string]json.RawMessage) bool { return string(m["op"]) == `"auth_ok"` })
	call := func(command, thread, target, message string, success bool) map[string]json.RawMessage {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"type": command, "id": "wire-1", "thread": thread, "subagentId": target, "message": message})
		sendAgentOp(t, socket, string(raw))
		reply := waitRawMsg(t, rec.msgs, "RPC response", func(m map[string]json.RawMessage) bool { return string(m["type"]) == `"response"` })
		wantCommand, _ := json.Marshal(command)
		wantSuccess, _ := json.Marshal(success)
		if string(reply["command"]) != string(wantCommand) || string(reply["id"]) != `"wire-1"` || string(reply["success"]) != string(wantSuccess) {
			t.Fatalf("wrong envelope: %s", reply)
		}
		if !success && len(reply["error"]) == 0 {
			t.Fatal("missing error")
		}
		return reply
	}
	reply := call("get_subagents", th.id, "", "", true)
	var data struct {
		Subagents []map[string]json.RawMessage `json:"subagents"`
	}
	if err := json.Unmarshal(reply["data"], &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Subagents) != 1 {
		t.Fatalf("leaked shell or missing child: %s", reply["data"])
	}
	for _, key := range []string{"id", "index", "agent", "agentSource", "status", "lastUpdate"} {
		if len(data.Subagents[0][key]) == 0 {
			t.Fatalf("missing OMP field %s", key)
		}
	}
	if string(data.Subagents[0]["status"]) != `"running"` {
		t.Fatal("wrong status")
	}
	call("get_subagents", "missing", "", "", false)
	call("steer_subagent", th.id, child.ID, "inspect tests", true)
	if got := guidance.snapshot(); len(got) != 1 || got[0] != "inspect tests" {
		t.Fatal(got)
	}
	call("steer_subagent", "missing", child.ID, "foreign", false)
	call("steer_subagent", th.id, shell.ID, "wrong kind", false)
	call("cancel_subagent", th.id, shell.ID, "", false)
	for i, want := range []string{`{"cancelled":true}`, `{"cancelled":false}`} {
		reply = call("cancel_subagent", th.id, child.ID, "", true)
		if string(reply["data"]) != want {
			t.Fatalf("cancel %d: %s", i, reply["data"])
		}
		if len(reply["items"]) != 0 {
			t.Fatal("RPC result placed in browser items")
		}
	}
	call("steer_subagent", th.id, child.ID, "too late", false)
	reply = call("cancel_subagent", th.id, "vanished", "", true)
	if string(reply["data"]) != `{"cancelled":false}` {
		t.Fatal(string(reply["data"]))
	}
	reply = call("get_subagents", th.id, "", "", true)
	if string(reply["data"]) != `{"subagents":[]}` {
		t.Fatalf("expected empty array after cancel: %s", reply["data"])
	}
	alive, _ := jobs.Read(shell.ID)
	if alive.CancelRequested {
		t.Fatal("subagent cancellation affected shell")
	}
}

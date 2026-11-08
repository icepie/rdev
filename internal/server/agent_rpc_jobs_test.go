package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lxzan/gws"
)

func TestJobsRPCDuringForegroundTurn(t *testing.T) {
	srv := newPrefsTestServer(t)
	entry, err := srv.agentSessionFor(agentServerDeviceID, &ClientConn{ID: agentServerDeviceID})
	if err != nil {
		t.Fatal(err)
	}
	th := entry.threads[entry.current]
	th.jobs.Close()
	th.jobs = newAgentJobs(nil)
	defer th.jobs.Close()
	foreign := newAgentJobs(nil)
	defer foreign.Close()
	release := make(chan struct{})
	defer close(release)
	worker := func(ctx context.Context, output func(string)) (string, error) {
		output("shell output")
		<-ctx.Done()
		<-release
		return "", ctx.Err()
	}
	job, err := th.jobs.Start("shell", "local shell", worker)
	if err != nil {
		t.Fatal(err)
	}
	other, err := foreign.Start("shell", "other thread", worker)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "shell output", func() bool { snapshot, _ := th.jobs.Read(job.ID); return snapshot.Output != "" })
	// Foreground admission remains occupied throughout all job operations.
	th.running = true
	before := th.session.History()
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
	observerSocket, observer := connect()
	call := func(command, id, thread string, ok bool) map[string]json.RawMessage {
		t.Helper()
		frame := map[string]any{"type": command, "id": "request-1", "thread": thread}
		if id != "" {
			frame["jobId"] = id
		}
		raw, _ := json.Marshal(frame)
		sendAgentOp(t, socket, string(raw))
		reply := waitRawMsg(t, rec.msgs, "job RPC", func(m map[string]json.RawMessage) bool { return string(m["type"]) == `"response"` })
		expected, _ := json.Marshal(ok)
		if string(reply["id"]) != `"request-1"` || string(reply["success"]) != string(expected) || string(reply["command"]) != `"`+command+`"` {
			t.Fatalf("invalid reply: %s", reply)
		}
		if !ok && string(reply["code"]) != `"invalid_request"` {
			t.Fatalf("missing error code: %s", reply)
		}
		return reply
	}
	reply := call("get_jobs", "", th.id, true)
	if !strings.Contains(string(reply["data"]), job.ID) || strings.Contains(string(reply["data"]), other.ID) {
		t.Fatalf("wrong thread scope: %s", reply)
	}
	reply = call("get_job", job.ID, th.id, true)
	if !strings.Contains(string(reply["data"]), "shell output") {
		t.Fatalf("missing output: %s", reply)
	}
	call("get_job", other.ID, th.id, false)
	call("get_jobs", "", "missing-thread", false)
	call("cancel_job", "", th.id, false)
	call("cancel_job", other.ID, th.id, false)
	reply = call("cancel_job", job.ID, th.id, true)
	if string(reply["data"]) != `{"cancellationRequested":true}` {
		t.Fatalf("wrong cancellation acknowledgement: %s", reply)
	}
	snapshot, _ := th.jobs.Read(job.ID)
	if snapshot.State != "running" || !snapshot.CancelRequested {
		t.Fatalf("reported cancellation complete before exit: %+v", snapshot)
	}
	call("cancel_job", "all", th.id, true)
	untouched, _ := foreign.Read(other.ID)
	if untouched.CancelRequested {
		t.Fatal("canceled another thread's job")
	}
	srv.agentSessionsMu.Lock()
	running, after := th.running, th.session.History()
	srv.agentSessionsMu.Unlock()
	if !running || !reflect.DeepEqual(before, after) {
		t.Fatal("job RPC modified foreground turn")
	}
	// A subsequent state response on the observer is a same-connection barrier:
	// all earlier frames are inspected, and none may contain the caller's reply.
	sendAgentOp(t, observerSocket, `{"type":"get_jobs","id":"observer-barrier"}`)
	waitRawMsg(t, observer.msgs, "observer barrier", func(frame map[string]json.RawMessage) bool {
		if string(frame["type"]) != `"response"` {
			return false
		}
		if string(frame["id"]) != `"observer-barrier"` {
			t.Fatalf("response leaked: %s", frame)
		}
		return true
	})
}

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lxzan/gws"
)

func TestRPCStateWaitsForBackgroundDelivery(t *testing.T) {
	srv := newPrefsTestServer(t)
	entry, err := srv.agentSessionFor(agentServerDeviceID, &ClientConn{ID: agentServerDeviceID})
	if err != nil {
		t.Fatal(err)
	}
	th := entry.threads[entry.current]
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	jobs := newAgentJobs(func(agentJobSnapshot) { close(entered); <-release })
	th.jobs.Close()
	th.jobs = jobs
	defer jobs.Close()
	job, err := jobs.Start("subagent", "inspect", func(context.Context, func(string)) (string, error) { return "done", nil })
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("delivery did not start")
	}
	// Read/Wait report worker completion, while the session still has work.
	result, err := jobs.Read(job.ID)
	if err != nil || result.State != "completed" {
		t.Fatalf("worker status: %+v %v", result, err)
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
	check := func(pending bool) {
		t.Helper()
		sendAgentOp(t, socket, `{"type":"get_state","id":"state"}`)
		reply := waitRawMsg(t, rec.msgs, "state", func(m map[string]json.RawMessage) bool { return string(m["type"]) == `"response"` })
		var state struct {
			Pending bool `json:"hasPendingAsyncWork"`
			Settled bool `json:"isSettled"`
		}
		if err := json.Unmarshal(reply["data"], &state); err != nil {
			t.Fatal(err)
		}
		if state.Pending != pending || state.Settled == pending {
			t.Fatalf("wrong state during handoff: %+v", state)
		}
	}
	check(true)
	unblock()
	waitFor(t, "completion callback returned", func() bool { return !jobs.HasPendingWork() })
	check(false)
}

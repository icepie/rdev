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

func TestJobGuidanceSocketAcknowledgesTargetAndRejectsStaleJobs(t *testing.T) {
	srv := newPrefsTestServer(t)
	entry, err := srv.agentSessionFor(agentServerDeviceID, &ClientConn{ID: agentServerDeviceID})
	if err != nil {
		t.Fatal(err)
	}
	thread := entry.threads[entry.current]
	jobs := newAgentJobs(nil)
	thread.jobs.Close()
	thread.jobs = jobs
	defer jobs.Close()
	release := make(chan struct{})
	defer close(release)
	guidance := &agentJobGuidance{}
	job, err := jobs.start("subagent", "inspect", func(ctx context.Context, _ func(string)) (string, error) {
		<-ctx.Done()
		<-release
		return "", ctx.Err()
	}, guidance)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(srv.HandleAgentWS))
	defer server.Close()
	rec := &agentWSRecorder{msgs: make(chan agentMsg, 64)}
	socket, _, err := gws.NewClient(rec, &gws.ClientOption{Addr: "ws" + strings.TrimPrefix(server.URL, "http") + "/agent?device=%40server", HandshakeTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.WriteClose(1000, nil)
	go socket.ReadLoop()
	waitAgentMsg(t, rec.msgs, "auth", func(m agentMsg) bool { return m.Op == "auth_ok" })
	send := func(request agentMsg, reject bool) {
		t.Helper()
		raw, _ := json.Marshal(request)
		sendAgentOp(t, socket, string(raw))
		reply := waitAgentMsg(t, rec.msgs, "guidance receipt", func(m agentMsg) bool { return m.Op == "job_steered" })
		expectedJob := request.JobID
		if expectedJob == "" {
			expectedJob = request.SubagentID
		}
		if reply.ID != request.ID || reply.JobID != expectedJob || reply.Thread != request.Thread || reply.IsError != reject || (reject && reply.Message == "") {
			t.Fatalf("unattributable receipt: %+v for %+v", reply, request)
		}
	}
	request := agentMsg{Op: "steer_job", ID: "request-1", Thread: thread.id, JobID: job.ID, Text: "check paths"}
	send(request, false)
	if got := guidance.snapshot(); len(got) != 1 || got[0] != "check paths" {
		t.Fatalf("guidance not queued: %v", got)
	}
	request.Op = "steer_subagent"
	request.ID = "request-omp-alias"
	request.JobID = ""
	request.SubagentID = job.ID
	request.Text = ""
	request.Message = "inspect tests"
	send(request, false)
	if got := guidance.snapshot(); len(got) != 2 || got[1] != "inspect tests" {
		t.Fatalf("OMP alias not queued: %v", got)
	}
	request.Op = "steer_job"
	request.SubagentID = ""
	request.Message = ""
	request.ID = "request-2"
	request.Thread = "other-thread"
	send(request, true)
	request.Thread = thread.id
	request.ID = "request-3"
	request.JobID = "unknown-job"
	send(request, true)
	request.JobID = job.ID
	request.ID = ""
	send(request, true)
	cancelRequest, _ := json.Marshal(agentMsg{Op: "cancel_job", Thread: thread.id, ID: job.ID})
	sendAgentOp(t, socket, string(cancelRequest))
	canceled := waitAgentMsg(t, rec.msgs, "cancellation pending", func(m agentMsg) bool { return m.Op == "job" })
	var snapshot agentJobSnapshot
	if err := json.Unmarshal(canceled.Items, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.ID != job.ID || !snapshot.CancelRequested || snapshot.State != "running" {
		t.Fatalf("cancellation receipt did not expose cleanup: %+v", snapshot)
	}
	request.ID = "request-4"
	send(request, true)
	if got := guidance.snapshot(); len(got) != 2 {
		t.Fatalf("rejected request mutated queue: %v", got)
	}
	srv.agentSessionsMu.Lock()
	running := thread.running
	srv.agentSessionsMu.Unlock()
	if running {
		t.Fatal("guidance started a foreground turn")
	}
}

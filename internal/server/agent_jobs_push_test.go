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

func TestJobLifecycleInvalidatesBothSubscribersAndIgnoresOldManager(t *testing.T) {
	srv := newPrefsTestServer(t)
	entry, err := srv.agentSessionFor(agentServerDeviceID, &ClientConn{ID: agentServerDeviceID})
	if err != nil {
		t.Fatal(err)
	}
	thread := entry.threads[entry.current]
	jobs := thread.jobs
	jobs.onComplete = nil // this fixture verifies transport, not model continuation
	defer jobs.Close()
	server := httptest.NewServer(http.HandlerFunc(srv.HandleAgentWS))
	defer server.Close()
	var sockets []*gws.Conn
	var receivers []*agentWSRecorder
	for range 2 {
		rec := &agentWSRecorder{msgs: make(chan agentMsg, 64)}
		socket, _, err := gws.NewClient(rec, &gws.ClientOption{Addr: "ws" + strings.TrimPrefix(server.URL, "http") + "/agent?device=%40server", HandshakeTimeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		defer socket.WriteClose(1000, nil)
		go socket.ReadLoop()
		waitAgentMsg(t, rec.msgs, "auth", func(m agentMsg) bool { return m.Op == "auth_ok" })
		sockets = append(sockets, socket)
		receivers = append(receivers, rec)
	}
	changed := func(stage string) {
		t.Helper()
		for _, rec := range receivers {
			msg := waitAgentMsg(t, rec.msgs, stage, func(m agentMsg) bool { return m.Op == "jobs_changed" })
			if msg.Thread != thread.id || len(msg.Items) != 0 {
				t.Fatalf("invalidation carried wrong owner or stale data: %+v", msg)
			}
		}
	}
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	job, err := jobs.Start("shell", "inspect", func(ctx context.Context, _ func(string)) (string, error) {
		<-ctx.Done()
		<-release
		return "", ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	changed("started")
	if err := jobs.Cancel(job.ID); err != nil {
		t.Fatal(err)
	}
	changed("cancel requested")
	close(release)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := jobs.Wait(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	changed("finished")
	srv.agentSessionsMu.Lock()
	thread.jobs = newAgentJobs(nil)
	srv.agentSessionsMu.Unlock()
	defer thread.jobs.Close()
	// Simulate a completion callback arriving after reset replaced its owner.
	srv.broadcastAgentJobsChanged(entry, thread, jobs)
	for index, socket := range sockets {
		raw, _ := json.Marshal(agentMsg{Op: "get_jobs", Thread: thread.id})
		sendAgentOp(t, socket, string(raw))
		msg := waitAgentMsg(t, receivers[index].msgs, "fresh manager", func(m agentMsg) bool {
			if m.Op == "jobs_changed" {
				t.Error("old manager published an invalidation")
			}
			return m.Op == "jobs"
		})
		if string(msg.Items) != "[]" {
			t.Fatalf("old jobs restored after reset: %s", msg.Items)
		}
	}
}

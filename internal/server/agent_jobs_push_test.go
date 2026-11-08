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

func TestJobLifecycleInvalidatesBothSubscribersAndIgnoresOldManager(t *testing.T) {
	srv := newPrefsTestServer(t)
	t.Cleanup(srv.waitAgentWork)
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

func TestSlowSubscriberDoesNotBlockJobAdmissionOrCancellation(t *testing.T) {
	srv := newPrefsTestServer(t)
	t.Cleanup(srv.waitAgentWork)
	entry, err := srv.agentSessionFor(agentServerDeviceID, &ClientConn{ID: agentServerDeviceID})
	if err != nil {
		t.Fatal(err)
	}
	thread := entry.threads[entry.current]
	jobs := thread.jobs
	finishedCallback := make(chan struct{})
	jobs.onComplete = func(agentJobSnapshot) { close(finishedCallback) }
	defer jobs.Close()
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
	var subscriber *agentConn
	srv.agentSessionsMu.Lock()
	for connection := range entry.subs {
		raw, _ := connection.Session().Load("agentConn")
		subscriber = raw.(*agentConn)
	}
	srv.agentSessionsMu.Unlock()
	if subscriber == nil {
		t.Fatal("no subscriber")
	}
	// Hold the actual connection write lock to model a browser write that
	// cannot finish. Keep it held until both lifecycle calls have returned.
	subscriber.writeMu.Lock()
	var unlockOnce sync.Once
	unlock := func() { unlockOnce.Do(subscriber.writeMu.Unlock) }
	defer unlock()
	type admission struct {
		job agentJobSnapshot
		err error
	}
	admitted := make(chan admission, 1)
	go func() {
		job, err := jobs.Start("shell", "slow subscriber", func(ctx context.Context, _ func(string)) (string, error) { <-ctx.Done(); return "", ctx.Err() })
		admitted <- admission{job, err}
	}()
	var job agentJobSnapshot
	select {
	case result := <-admitted:
		if result.err != nil {
			t.Fatal(result.err)
		}
		job = result.job
	case <-time.After(time.Second):
		t.Fatal("slow subscriber blocked job admission")
	}
	canceled := make(chan error, 1)
	go func() { canceled <- jobs.Cancel(job.ID) }()
	select {
	case err := <-canceled:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("slow subscriber blocked cancellation")
	}
	select {
	case <-finishedCallback:
	case <-time.After(time.Second):
		t.Fatal("worker completion blocked by subscriber")
	}
	unlock()
	srv.waitAgentWork()
	raw, _ := json.Marshal(agentMsg{Op: "get_jobs", Thread: thread.id, ID: job.ID})
	sendAgentOp(t, socket, string(raw))
	reply := waitAgentMsg(t, rec.msgs, "final job", func(m agentMsg) bool { return m.Op == "job" })
	var snapshot agentJobSnapshot
	if err := json.Unmarshal(reply.Items, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.State != "canceled" {
		t.Fatalf("latest state lost after blocked writes: %+v", snapshot)
	}
}

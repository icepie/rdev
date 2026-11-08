package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lxzan/gws"
)

func TestJobRecordsRestoreWithoutRestartingExecution(t *testing.T) {
	srv := newPrefsTestServer(t)
	entry, err := srv.agentSessionFor(agentServerDeviceID, &ClientConn{ID: agentServerDeviceID})
	if err != nil {
		t.Fatal(err)
	}
	th := entry.threads[entry.current]
	th.jobs.onComplete = nil
	job, err := th.jobs.Start("shell", "build", func(_ context.Context, write func(string)) (string, error) {
		write("compiled\n")
		return "exit code: 0\nverified", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := th.jobs.Wait(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "job snapshot durable", func() bool { return !th.jobs.pendingPersistence() })
	srv.waitAgentWork()
	store := srv.agentStore()
	records := store.loadJobs(entry.deviceID, th.id)
	if len(records) != 1 || records[0].Output != "compiled\n" || records[0].State != "completed" {
		t.Fatal(records)
	}
	// Simulate a crash checkpoint for another worker; no executable closure is stored.
	records = append(records, agentJobSnapshot{ID: "interrupted", Kind: "subagent", Label: "inspect", State: "running", Started: time.Now(), Output: "last checkpoint", PendingGuidance: []string{"check logs"}, CancelRequested: true})
	if err := store.saveJobs(entry.deviceID, th.id, th.model, records); err != nil {
		t.Fatal(err)
	}
	restarted := newPrefsTestServer(t)
	restarted.DataDir = srv.DataDir
	restored, err := restarted.agentSessionFor(agentServerDeviceID, &ClientConn{ID: agentServerDeviceID})
	if err != nil {
		t.Fatal(err)
	}
	current := restored.threads[th.id]
	if current == nil || current.running || len(current.queue) != 0 || current.jobs.RunningCount() != 0 {
		t.Fatal("restore launched work")
	}
	finished, err := current.jobs.Wait(ctx, job.ID)
	if err != nil || finished.State != "completed" || finished.Result != "exit code: 0\nverified" {
		t.Fatal(finished, err)
	}
	stopped, err := current.jobs.Wait(ctx, "interrupted")
	if err != nil || stopped.State != "failed" || stopped.CancelRequested || stopped.Finished == nil || !strings.Contains(stopped.Error, "not resumed") || stopped.Output != "last checkpoint" {
		t.Fatal(stopped, err)
	}
	stopped.PendingGuidance[0] = "mutated"
	again, _ := current.jobs.Read("interrupted")
	if again.PendingGuidance[0] != "check logs" {
		t.Fatal("restored guidance aliases snapshots")
	}
	if err := current.jobs.Cancel("interrupted"); err != nil {
		t.Fatal(err)
	}
	if err := current.jobs.Steer("interrupted", "late"); err == nil {
		t.Fatal("restored task accepts guidance")
	}
	waitFor(t, "interrupted state rewritten", func() bool { return !current.jobs.pendingPersistence() })
	restarted.waitAgentWork()
	for _, record := range restarted.agentStore().loadJobs(restored.deviceID, th.id) {
		if record.State == "running" {
			t.Fatal("interrupted state not durable")
		}
	}
	// The public jobs protocol sees restored records immediately after auth.
	server := httptest.NewServer(http.HandlerFunc(restarted.HandleAgentWS))
	defer server.Close()
	// dialAgentWS selects dev1, so use an explicit server-device socket fixture.
	conn, rec := dialJobStoreWS(t, server)
	auth := waitAgentMsg(t, rec.msgs, "restored auth", func(m agentMsg) bool { return m.Op == "auth_ok" })
	raw, _ := json.Marshal(agentMsg{Op: "get_jobs", Thread: auth.Thread, ID: job.ID})
	sendAgentOp(t, conn, string(raw))
	detail := waitAgentMsg(t, rec.msgs, "restored job detail", func(m agentMsg) bool { return m.Op == "job" })
	if !strings.Contains(string(detail.Items), "verified") {
		t.Fatal(string(detail.Items))
	}
}

func TestJobPersistenceDoesNotBlockAdmissionAndCannotResurrectRemovedState(t *testing.T) {
	for _, operation := range []string{"reset", "delete"} {
		t.Run(operation, func(t *testing.T) {
			srv := newPrefsTestServer(t)
			entry, err := srv.agentSessionFor(agentServerDeviceID, &ClientConn{ID: agentServerDeviceID})
			if err != nil {
				t.Fatal(err)
			}
			th := entry.threads[entry.current]
			owner := th.jobs
			owner.onComplete = nil
			store := srv.agentStore()
			store.mu.Lock()
			locked := true
			defer func() {
				if locked {
					store.mu.Unlock()
				}
			}()
			job, err := owner.Start("shell", "wait", func(ctx context.Context, _ func(string)) (string, error) { <-ctx.Done(); return "late", ctx.Err() })
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, "writer reached disk", func() bool {
				if th.persistMu.TryLock() {
					th.persistMu.Unlock()
					return false
				}
				return true
			})
			acquired := make(chan struct{})
			go func() { srv.agentSessionsMu.Lock(); srv.agentSessionsMu.Unlock(); close(acquired) }()
			select {
			case <-acquired:
			case <-time.After(time.Second):
				t.Fatal("job disk write held global lock")
			}
			if !owner.pendingPersistence() {
				t.Fatal("undurable job eligible for eviction")
			}
			store.mu.Unlock()
			locked = false
			waitFor(t, "start persisted", func() bool { return !owner.pendingPersistence() })
			handler := &agentWSHandler{srv: srv}
			conn := &agentConn{entry: entry, current: th.id, deviceID: entry.deviceID}
			if operation == "reset" {
				handler.handleReset(conn, th.id)
			} else {
				server := httptest.NewServer(http.HandlerFunc(srv.HandleAgentWS))
				defer server.Close()
				socket, rec := dialJobStoreWS(t, server)
				waitAgentMsg(t, rec.msgs, "delete auth", func(m agentMsg) bool { return m.Op == "auth_ok" })
				raw, _ := json.Marshal(agentMsg{Op: "delete_thread", Thread: th.id})
				sendAgentOp(t, socket, string(raw))
				waitAgentMsg(t, rec.msgs, "deleted", func(m agentMsg) bool { return m.Op == "thread_deleted" })
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err := owner.Wait(ctx, job.ID); err != nil {
				t.Fatal(err)
			}
			srv.persistAgentJobs(entry, th, owner)
			srv.waitAgentWork()
			if _, err := os.Stat(store.jobsPath(entry.deviceID, th.id)); !os.IsNotExist(err) {
				t.Fatal("old manager restored reset records", err)
			}
			if operation == "reset" && len(th.jobs.List()) != 0 {
				t.Fatal("reset restored jobs")
			}
		})
	}
}

func TestJobRecordRetentionCorruptionAndTTLCleanup(t *testing.T) {
	store := newAgentSessionStore(t.TempDir(), time.Hour)
	now := time.Now()
	records := []agentJobSnapshot{{ID: "one", Kind: "shell", State: "completed", Started: now, Finished: &now, Result: "kept"}}
	if err := store.saveJobs("device", "thread", "model", records); err != nil {
		t.Fatal(err)
	}
	stale := now.Add(-2 * time.Hour)
	if err := os.Chtimes(store.path("device", "thread"), stale, stale); err != nil {
		t.Fatal(err)
	}
	store.cleanup()
	if _, err := os.Stat(store.jobsPath("device", "thread")); !os.IsNotExist(err) {
		t.Fatal("TTL kept job sidecar")
	}
	if err := store.saveJobs("device", "thread", "model", records); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.jobsPath("device", "thread"), []byte(`{"version":1,"jobs":[`), 0600); err != nil {
		t.Fatal(err)
	}
	if got := store.loadJobs("device", "thread"); len(got) != 0 {
		t.Fatal("corrupt records accepted")
	}
	manager := newAgentJobs(nil)
	many := make([]agentJobSnapshot, agentMaxRetainedJobs+10)
	for i := range many {
		many[i] = agentJobSnapshot{ID: strings.Repeat("x", i+1), Kind: "shell", State: "completed", Started: now.Add(time.Duration(i) * time.Second), Finished: &now, Output: strings.Repeat("a", agentJobOutputLimit+100)}
	}
	manager.restore(many)
	if len(manager.List()) != agentMaxRetainedJobs {
		t.Fatal("retention limit lost")
	}
	for _, record := range manager.List() {
		detail, _ := manager.Read(record.ID)
		if len(detail.Output) > agentJobOutputLimit {
			t.Fatal("unbounded restored output")
		}
	}
}

func dialJobStoreWS(t *testing.T, server *httptest.Server) (*gws.Conn, *agentWSRecorder) {
	t.Helper()
	rec := &agentWSRecorder{msgs: make(chan agentMsg, 32)}
	conn, _, err := gws.NewClient(rec, &gws.ClientOption{Addr: "ws" + strings.TrimPrefix(server.URL, "http") + "/agent?device=%40server", HandshakeTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.WriteClose(1000, nil) })
	go conn.ReadLoop()
	return conn, rec
}

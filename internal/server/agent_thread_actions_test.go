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
	"rdev/internal/agent"
)

func TestDeleteRunningConversationCancelsWorkAndCannotResurrectHistory(t *testing.T) {
	srv := newPrefsTestServer(t)
	entry, err := srv.agentSessionFor(agentServerDeviceID, &ClientConn{ID: agentServerDeviceID})
	if err != nil {
		t.Fatal(err)
	}
	th := entry.threads[entry.current]
	th.session.SetHistory([]agent.Message{{Role: "user", Content: "old history"}})
	srv.agentStore().save(entry.deviceID, th.id, th.model, th.session.History())
	job, err := th.jobs.Start("shell", "watch", func(ctx context.Context, _ func(string)) (string, error) { <-ctx.Done(); return "", ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	owner := th.jobs
	srv.agentSessionsMu.Lock()
	srv.startAgentTurnLocked(entry, th, "/compact", nil)
	turn := snapshotAgentTurnLocked(th)
	th.queue = []agentQueuedChat{{text: "must never run"}}
	srv.agentSessionsMu.Unlock()
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
	if err := socket.WriteMessage(gws.OpcodeText, []byte(`{"op":"delete_thread","thread":"`+th.id+`"}`)); err != nil {
		t.Fatal(err)
	}
	waitAgentMsg(t, rec.msgs, "deleted", func(m agentMsg) bool { return m.Op == "thread_deleted" && m.Thread == th.id })
	switched := waitAgentMsg(t, rec.msgs, "replacement thread", func(m agentMsg) bool { return m.Op == "thread_switched" })
	if switched.Thread == th.id || switched.Thread == "" {
		t.Fatal("deleted selection retained")
	}
	if turn.ctx.Err() == nil {
		t.Fatal("foreground turn not canceled")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := owner.Wait(ctx, job.ID)
	if err != nil || result.State != "canceled" {
		t.Fatalf("job not canceled: %+v %v", result, err)
	}
	srv.runAgentTurn(entry, th, turn, "/compact", nil)
	history, _, _, _ := srv.agentStore().load(entry.deviceID, th.id)
	if len(history) != 0 {
		t.Fatal("late turn restored deleted history")
	}
	srv.agentSessionsMu.Lock()
	defer srv.agentSessionsMu.Unlock()
	if entry.threads[th.id] != nil || len(th.queue) != 0 {
		t.Fatal("deleted thread or queue still present")
	}
}

func TestCrossDeviceThreadManagementRequiresAuthority(t *testing.T) {
	srv := newPrefsTestServer(t)
	srv.AdminToken = "test-only"
	ac := &agentConn{deviceID: agentServerDeviceID}
	h := &agentWSHandler{srv: srv}
	if _, err := h.threadActionEntry(ac, "offline"); err == nil {
		t.Fatal("cross-device management bypassed admin")
	}
	h.adminBypass = true
	entry, err := h.threadActionEntry(ac, "offline")
	if err != nil || entry.deviceID != "offline" {
		t.Fatalf("admin cannot manage offline history: %v", err)
	}
	if ac.deviceID != agentServerDeviceID {
		t.Fatal("management switched the active conversation")
	}
}

// Management failures must identify the requested conversation, even when
// another conversation is selected or the target device is offline.
func TestManagementErrorsIdentifyTarget(t *testing.T) {
	srv := newPrefsTestServer(t)
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
	for _, tc := range []agentMsg{
		{Op: "delete_thread", Device: agentServerDeviceID, Thread: "missing-delete"},
		{Op: "rename_thread", Device: agentServerDeviceID, Thread: "missing-rename", Title: "name"},
		{Op: "rename_thread", Device: agentServerDeviceID, Thread: auth.Thread, Title: " "},
		{Op: "delete_thread", Device: "offline-device", Thread: "offline-history"},
		{Op: "get_jobs", Thread: auth.Thread, ID: "expired-job"},
		{Op: "cancel_job", Thread: auth.Thread, ID: "expired-job"},
	} {
		payload, err := json.Marshal(tc)
		if err != nil {
			t.Fatal(err)
		}
		if err := socket.WriteMessage(gws.OpcodeText, payload); err != nil {
			t.Fatal(err)
		}
		expectedOp := "thread_action_error"
		if tc.Op == "get_jobs" || tc.Op == "cancel_job" {
			expectedOp = "job_error"
		}
		got := waitAgentMsg(t, rec.msgs, "management failure", func(m agentMsg) bool { return m.Op == expectedOp })
		if got.Device != tc.Device || got.Thread != tc.Thread || got.Message == "" {
			t.Fatalf("unattributable failure for %+v: %+v", tc, got)
		}
	}
}

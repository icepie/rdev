package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lxzan/gws"
)

func TestQueueEditLeaseOwnershipRenewalAndRelease(t *testing.T) {
	s := newTurnTestServer(t, "http://127.0.0.1:1")
	entry, err := s.agentSessionFor("dev1", s.clients["dev1"])
	if err != nil {
		t.Fatal(err)
	}
	th := entry.threads[entry.current]
	th.running = true
	th.queue = []agentQueuedChat{{id: "row", text: "original"}}
	server := httptest.NewServer(http.HandlerFunc(s.HandleAgentWS))
	defer server.Close()
	first, one := dialAgentWS(t, server)
	waitAgentMsg(t, one.msgs, "first", func(m agentMsg) bool { return m.Op == "auth_ok" })
	second, two := dialAgentWS(t, server)
	waitAgentMsg(t, two.msgs, "second", func(m agentMsg) bool { return m.Op == "auth_ok" })
	request := func(socket *gws.Conn, rec *agentWSRecorder, op, token string) agentMsg {
		original := "original"
		raw, _ := json.Marshal(agentMsg{Op: op, Thread: th.id, QueueID: "row", EditToken: token, ExpectedText: &original, Text: "edited"})
		sendAgentOp(t, socket, string(raw))
		return waitAgentMsg(t, rec.msgs, "mutation", func(m agentMsg) bool { return m.Op == "queue_updated" })
	}
	if request(first, one, "hold_queue", "lease-one").IsError {
		t.Fatal("hold failed")
	}
	s.agentSessionsMu.Lock()
	until := th.queue[0].editUntil
	blocked := !queueHeadReady(entry, th, time.Now())
	s.agentSessionsMu.Unlock()
	if !blocked {
		t.Fatal("editing head was eligible for admission")
	}
	if !request(second, two, "hold_queue", "lease-two").IsError {
		t.Fatal("concurrent editor stole hold")
	}
	if !request(second, two, "update_queue", "").IsError {
		t.Fatal("unowned update changed held item")
	}
	if request(first, one, "hold_queue", "lease-one").IsError {
		t.Fatal("renewal failed")
	}
	s.expireQueueEdit(entry, th, "row", "lease-one", until) // stale timer must not release renewal
	s.agentSessionsMu.Lock()
	held := !th.queue[0].editUntil.IsZero()
	s.agentSessionsMu.Unlock()
	if !held {
		t.Fatal("stale timer released active editor")
	}
	if request(first, one, "release_queue", "lease-one").IsError {
		t.Fatal("release failed")
	}
	if request(second, two, "hold_queue", "lease-two").IsError {
		t.Fatal("released hold not available")
	}
	second.WriteClose(1000, nil)
	waitFor(t, "disconnect releases lease", func() bool {
		s.agentSessionsMu.Lock()
		defer s.agentSessionsMu.Unlock()
		return th.queue[0].editToken == ""
	})
	s.agentSessionsMu.Lock()
	ready := queueHeadReady(entry, th, time.Now())
	s.agentSessionsMu.Unlock()
	if !ready {
		t.Fatal("released queue remained blocked")
	}
}

func TestQueueLeaseExpiredOrDeletedThreadDoesNotStall(t *testing.T) {
	s := newTurnTestServer(t, "http://127.0.0.1:1")
	entry, err := s.agentSessionFor("dev1", s.clients["dev1"])
	if err != nil {
		t.Fatal(err)
	}
	th := entry.threads[entry.current]
	th.running = true
	until := time.Now().Add(-time.Second)
	th.queue = []agentQueuedChat{{id: "row", text: "original", editToken: "expired", editUntil: until}}
	s.expireQueueEdit(entry, th, "row", "expired", until)
	if th.queue[0].editToken != "" {
		t.Fatal("expired lease retained")
	}
	delete(entry.threads, th.id)
	s.resumeAgentQueue(entry, th)
	if len(th.queue) != 1 {
		t.Fatal("deleted thread admitted a queued message")
	}
}

func TestQueueReleaseResumesExactlyOnce(t *testing.T) {
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer provider.Close()
	s := newTurnTestServer(t, provider.URL)
	entry, err := s.agentSessionFor("dev1", s.clients["dev1"])
	if err != nil {
		t.Fatal(err)
	}
	th := entry.threads[entry.current]
	th.queue = []agentQueuedChat{{id: "row", text: "question", editToken: "lease", editUntil: time.Now().Add(time.Minute)}}
	s.resumeAgentQueue(entry, th)
	if calls.Load() != 0 || th.running {
		t.Fatal("held item started")
	}
	s.agentSessionsMu.Lock()
	th.queue[0].editUntil = time.Time{}
	s.agentSessionsMu.Unlock()
	for i := 0; i < 8; i++ {
		go s.resumeAgentQueue(entry, th)
	}
	waitFor(t, "one completed turn", func() bool {
		s.agentSessionsMu.Lock()
		defer s.agentSessionsMu.Unlock()
		return len(th.queue) == 0 && !th.running && th.lastOutcome == "done"
	})
	if calls.Load() != 1 {
		t.Fatalf("duplicate admission: %d", calls.Load())
	}
}

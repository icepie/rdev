package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"rdev/internal/agent"
	"strings"
	"testing"
	"time"

	"github.com/lxzan/gws"
)

type agentCloseRecorder struct {
	gws.BuiltinEventHandler
	closed chan error
}

func (r *agentCloseRecorder) OnClose(_ *gws.Conn, err error) { r.closed <- err }

func TestOfflineAgentConnectionCanRetry(t *testing.T) {
	srv := newPrefsTestServer(t)
	server := httptest.NewServer(http.HandlerFunc(srv.HandleAgentWS))
	defer server.Close()
	rec := &agentCloseRecorder{closed: make(chan error, 1)}
	socket, _, err := gws.NewClient(rec, &gws.ClientOption{
		Addr:             "ws" + strings.TrimPrefix(server.URL, "http") + "/agent?device=offline",
		HandshakeTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.WriteClose(1000, nil)
	go socket.ReadLoop()
	select {
	case err := <-rec.closed:
		var closed *gws.CloseError
		if !errors.As(err, &closed) || closed.Code != 1013 {
			t.Fatalf("offline close must permit retry: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("offline connection did not close")
	}
}

func TestApprovalSurvivesBrowserReconnectWithoutExecuting(t *testing.T) {
	srv := newTurnTestServer(t, "http://unused")
	server := httptest.NewServer(http.HandlerFunc(srv.HandleAgentWS))
	defer server.Close()
	socket, rec := dialAgentWS(t, server)
	auth := waitAgentMsg(t, rec.msgs, "auth", func(m agentMsg) bool { return m.Op == "auth_ok" })
	entry, err := srv.agentSessionFor("dev1", srv.clients["dev1"])
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.agentSessionsMu.Lock()
	th := entry.threads[auth.Thread]
	th.running = true
	th.turnCtx = ctx
	srv.agentSessionsMu.Unlock()
	result := make(chan bool, 1)
	go func() {
		allowed, _ := srv.requestApproval(entry, th, agent.ToolCall{Name: "shell"}, json.RawMessage(`{"command":"test"}`))
		result <- allowed
	}()
	approval := waitAgentMsg(t, rec.msgs, "approval", func(m agentMsg) bool { return m.Op == "approval_request" })
	socket.WriteClose(1000, nil)
	waitFor(t, "browser detached", func() bool {
		srv.agentSessionsMu.Lock()
		defer srv.agentSessionsMu.Unlock()
		return len(entry.subs) == 0
	})
	srv.agentSessionsMu.Lock()
	_, pending := th.approvals[approval.ID]
	srv.agentSessionsMu.Unlock()
	if !pending {
		t.Fatal("disconnect resolved pending approval")
	}
	select {
	case <-result:
		t.Fatal("approval completed without a decision")
	default:
	}
	reopened, rec2 := dialAgentWS(t, server)
	replay := waitAgentMsg(t, rec2.msgs, "reattach", func(m agentMsg) bool { return m.Op == "auth_ok" })
	found := false
	for _, raw := range replay.Live {
		var frame agentMsg
		if json.Unmarshal(raw, &frame) == nil && frame.Op == "approval_request" && frame.ID == approval.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("pending approval missing from reattach replay")
	}
	sendAgentOp(t, reopened, `{"op":"approve","id":"`+approval.ID+`","allow":false}`)
	select {
	case allowed := <-result:
		if allowed {
			t.Fatal("denial executed the tool")
		}
	case <-time.After(time.Second):
		t.Fatal("reattached decision was not delivered")
	}
}

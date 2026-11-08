package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"rdev/internal/agent"
	"testing"
)

func TestOfflineHistoryRequiresConfirmationAndHasNoTools(t *testing.T) {
	requests := make(chan map[string]any, 2)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		requests <- body
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"historical answer\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer provider.Close()
	srv := newTurnTestServer(t, provider.URL)
	entry, err := srv.agentSessionFor("dev1", srv.clients["dev1"])
	if err != nil {
		t.Fatal(err)
	}
	th := entry.threads[entry.current]
	th.session.SetHistory([]agent.Message{{Role: "user", Content: "retained history"}, {Role: "assistant", Content: "retained answer"}})
	srv.mu.Lock()
	delete(srv.clients, "dev1")
	srv.mu.Unlock()
	srv.AdminToken = "test-admin"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("Authorization", "Bearer test-admin")
		srv.HandleAgentWS(w, r)
	}))
	defer server.Close()
	socket, rec := dialAgentWS(t, server)
	auth := waitAgentMsg(t, rec.msgs, "offline history", func(m agentMsg) bool { return m.Op == "auth_ok" })
	if !auth.Offline || len(auth.History) != 2 {
		t.Fatalf("history not restored: %#v", auth)
	}
	sendAgentOp(t, socket, `{"op":"chat","text":"discuss"}`)
	waitAgentMsg(t, rec.msgs, "confirmation required", func(m agentMsg) bool { return m.Op == "error" })
	select {
	case <-requests:
		t.Fatal("unconfirmed request reached model")
	default:
	}
	sendAgentOp(t, socket, `{"op":"chat","text":"discuss","offlineConfirmed":true}`)
	waitAgentMsg(t, rec.msgs, "discussion done", func(m agentMsg) bool { return m.Op == "done" })
	body := <-requests
	if tools, ok := body["tools"].([]any); ok && len(tools) > 0 {
		t.Fatal("offline model received executable tools")
	}
}

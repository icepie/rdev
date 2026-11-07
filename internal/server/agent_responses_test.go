package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAgentResponsesProviderStreamsThroughWebSocketAndRestoresHistory(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("wrong endpoint: %s", r.URL.Path)
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request["store"] != false || request["input"] == nil || request["instructions"] == nil {
			t.Error("Responses request not configured")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"thinking\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"answer\"}\n\n")
		fmt.Fprint(w, `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"reasoning","id":"rs_1","encrypted_content":"private-fixture","summary":[{"type":"summary_text","text":"thinking"}]},{"type":"message","role":"assistant","id":"msg_1","phase":"final_answer","content":[{"type":"output_text","text":"answer"}]}],"usage":{"input_tokens":12,"output_tokens":3}}}`+"\n\n")
	}))
	defer provider.Close()
	srv := newTurnTestServer(t, provider.URL)
	srv.Agent.Providers[0].API = "openai-responses"
	server := httptest.NewServer(http.HandlerFunc(srv.HandleAgentWS))
	defer server.Close()
	socket, rec := dialAgentWS(t, server)
	waitAgentMsg(t, rec.msgs, "auth", func(m agentMsg) bool { return m.Op == "auth_ok" })
	sendAgentOp(t, socket, `{"op":"chat","text":"hello"}`)
	thinking := waitAgentMsg(t, rec.msgs, "reasoning", func(m agentMsg) bool { return m.Op == "reasoning" })
	if thinking.Text != "thinking" {
		t.Fatalf("reasoning: %+v", thinking)
	}
	answer := waitAgentMsg(t, rec.msgs, "answer", func(m agentMsg) bool { return m.Op == "delta" })
	if answer.Text != "answer" {
		t.Fatalf("answer: %+v", answer)
	}
	done := waitAgentMsg(t, rec.msgs, "done", func(m agentMsg) bool { return m.Op == "done" })
	if done.Usage == nil || done.Usage.InputTokens != 12 {
		t.Fatalf("usage: %+v", done)
	}
	socket.WriteClose(1000, nil)
	_, second := dialAgentWS(t, server)
	auth := waitAgentMsg(t, second.msgs, "restored auth", func(m agentMsg) bool { return m.Op == "auth_ok" })
	if len(auth.History) != 2 || auth.History[1].Text != "answer" || auth.History[1].Reasoning != "thinking" {
		t.Fatalf("history: %+v", auth.History)
	}
	raw, _ := json.Marshal(auth)
	if strings.Contains(string(raw), "private-fixture") {
		t.Fatal("opaque provider reasoning leaked to the browser")
	}
}

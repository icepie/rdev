package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"rdev/internal/agent"
)

func TestAgentFailureDiagnosticSurvivesRestartAndThreadSnapshots(t *testing.T) {
	var requests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("x-request-id", "req-restart")
		fmt.Fprint(w, sseTextLine("partial answer")+`data: {"choices":[{"delta":{},"finish_reason":"content_filte","native_finish_reason":"SAFETY"}]}`+"\n\n"+sseDoneLine)
	}))
	defer provider.Close()
	srv := newTurnTestServer(t, provider.URL)
	firstHTTP := httptest.NewServer(http.HandlerFunc(srv.HandleAgentWS))
	defer firstHTTP.Close()
	socket, recorder := dialAgentWS(t, firstHTTP)
	auth := waitAgentMsg(t, recorder.msgs, "first auth", func(m agentMsg) bool { return m.Op == "auth_ok" })
	sendAgentOp(t, socket, `{"op":"chat","text":"hello"}`)
	terminal := waitAgentMsg(t, recorder.msgs, "filtered turn", func(m agentMsg) bool { return m.Op == "error" })
	if terminal.Code != "provider_content_filter" || terminal.Diagnostic == nil || terminal.Diagnostic.RequestID != "req-restart" || terminal.Diagnostic.RawFinishReason != "content_filte" || terminal.Diagnostic.NativeFinishReason != "SAFETY" || !terminal.Diagnostic.TextStarted || requests.Load() != 1 {
		t.Fatalf("missing terminal diagnostic: %+v", terminal)
	}
	srv.waitAgentWork()

	restarted := newTurnTestServer(t, provider.URL)
	restarted.DataDir = srv.DataDir
	secondHTTP := httptest.NewServer(http.HandlerFunc(restarted.HandleAgentWS))
	defer secondHTTP.Close()
	secondSocket, secondRecorder := dialAgentWS(t, secondHTTP)
	restored := waitAgentMsg(t, secondRecorder.msgs, "restored auth", func(m agentMsg) bool { return m.Op == "auth_ok" })
	if restored.Thread != auth.Thread || restored.Running || len(restored.Live) != 0 || !reflect.DeepEqual(restored.Diagnostic, terminal.Diagnostic) {
		t.Fatalf("restart lost terminal diagnostic: %+v", restored)
	}
	if len(restored.History) != 2 || restored.History[1].Text != "partial answer" || len(restored.Threads) != 1 || !reflect.DeepEqual(restored.Threads[0].Diagnostic, terminal.Diagnostic) {
		t.Fatalf("restart changed history/result contract: %+v", restored)
	}
	sendAgentOp(t, secondSocket, `{"op":"new_thread"}`)
	waitAgentMsg(t, secondRecorder.msgs, "new thread", func(m agentMsg) bool { return m.Op == "thread_switched" })
	sendAgentOp(t, secondSocket, fmt.Sprintf(`{"op":"switch_thread","thread":%q}`, auth.Thread))
	switched := waitAgentMsg(t, secondRecorder.msgs, "old thread", func(m agentMsg) bool { return m.Op == "thread_switched" && m.Thread == auth.Thread })
	if !reflect.DeepEqual(switched.Diagnostic, terminal.Diagnostic) {
		t.Fatalf("switch lost diagnostic: %+v", switched)
	}
	sendAgentOp(t, secondSocket, fmt.Sprintf(`{"op":"peek_thread","thread":%q}`, auth.Thread))
	peeked := waitAgentMsg(t, secondRecorder.msgs, "peek", func(m agentMsg) bool { return m.Op == "thread_peeked" })
	if !reflect.DeepEqual(peeked.Diagnostic, terminal.Diagnostic) || requests.Load() != 1 {
		t.Fatalf("peek lost diagnostic or restart retried filtering: %+v", peeked)
	}
}

func TestAgentDiagnosticCheckpointBoundsRewriteAndClearing(t *testing.T) {
	st := newTestStore(t)
	diagnostic := &agent.FailureDiagnostic{
		Code: "provider_content_filter", Provider: "fixture", Model: "model", API: "openai-responses",
		FinishReason: "content_filter", RawFinishReason: "content_filte", RequestID: "req-persist", ToolCallStarted: true,
		Message: strings.Repeat("private HTTP body", 10000),
	}
	st.saveOutcome("device", "thread", "fixture/model", "error", "stamp", diagnostic)
	want := diagnostic.Safe()
	for range 2 {
		reopened := newAgentSessionStore(st.dir, time.Hour)
		metas := reopened.listThreads("device")
		if len(metas) != 1 || !reflect.DeepEqual(metas[0].Diagnostic, want) {
			t.Fatalf("checkpoint lost diagnostic: %+v", metas)
		}
		reopened.replace("device", "thread", "fixture/model", []agent.Message{{Role: "user", Content: "hello"}})
		data, err := os.ReadFile(st.path("device", "thread"))
		if err != nil || strings.Contains(string(data), "private HTTP body") || len(data) > 4096 {
			t.Fatalf("unsafe checkpoint bytes=%d err=%v", len(data), err)
		}
		st = reopened
	}
	for _, outcome := range []string{"done", "running"} {
		st.saveOutcome("device", "thread", "fixture/model", outcome, "next", diagnostic)
		metas := newAgentSessionStore(st.dir, time.Hour).listThreads("device")
		if len(metas) != 1 || metas[0].Diagnostic != nil {
			t.Fatalf("stale diagnostic survived %s: %+v", outcome, metas)
		}
	}
	st.saveOutcome("device", "thread", "fixture/model", "error", "stamp", diagnostic)
	st.clear("device", "thread")
	st.save("device", "thread", "fixture/model", nil)
	for _, meta := range newAgentSessionStore(st.dir, time.Hour).listThreads("device") {
		if meta.Diagnostic != nil {
			t.Fatal("reset retained diagnostic")
		}
	}
	// Old records without the additive field still decode and load normally.
	raw, _ := json.Marshal(agentHistoryLine{T: "outcome", Outcome: "error"})
	var old agentHistoryLine
	if err := json.Unmarshal(raw, &old); err != nil || old.Diagnostic != nil {
		t.Fatalf("legacy record changed: %+v %v", old, err)
	}
}

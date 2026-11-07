package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestAgentReasoningStreamsAndReplaysBeforeCompletion(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	finish := func() { releaseOnce.Do(func() { close(release) }) }
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, text := range []string{"检查设备", "，再执行。"} {
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{
				"delta": map[string]any{"reasoning_content": text},
			}}})
			fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, sseTextLine("ready")+sseDoneLine)
	}))
	t.Cleanup(provider.Close)
	srv := newTurnTestServer(t, provider.URL)
	t.Cleanup(finish)
	server := httptest.NewServer(http.HandlerFunc(srv.HandleAgentWS))
	defer server.Close()
	socket, rec := dialAgentWS(t, server)
	auth := waitAgentMsg(t, rec.msgs, "auth", func(m agentMsg) bool { return m.Op == "auth_ok" })
	sendAgentOp(t, socket, `{"op":"chat","text":"inspect"}`)
	for _, expected := range []string{"检查设备", "，再执行。"} {
		frame := waitAgentMsg(t, rec.msgs, "reasoning delta", func(m agentMsg) bool { return m.Op == "reasoning" })
		if frame.Thread != auth.Thread || frame.Text != expected {
			t.Fatalf("reasoning frame = %+v, want %q on %s", frame, expected, auth.Thread)
		}
	}
	socket.WriteClose(1000, nil)
	_, reconnected := dialAgentWS(t, server)
	replay := waitAgentMsg(t, reconnected.msgs, "reconnected auth", func(m agentMsg) bool { return m.Op == "auth_ok" })
	var thinking string
	for _, raw := range replay.Live {
		var frame agentMsg
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatal(err)
		}
		if frame.Op == "reasoning" {
			thinking += frame.Text
		}
		if frame.Op == "done" {
			t.Fatal("provider completed before release")
		}
	}
	if thinking != "检查设备，再执行。" {
		t.Fatalf("replayed reasoning = %q", thinking)
	}
	finish()
	answer := waitAgentMsg(t, reconnected.msgs, "answer", func(m agentMsg) bool { return m.Op == "delta" })
	if answer.Text != "ready" {
		t.Fatalf("answer delta = %+v", answer)
	}
	done := waitAgentMsg(t, reconnected.msgs, "done", func(m agentMsg) bool { return m.Op == "done" })
	if done.Reasoning != thinking {
		t.Fatalf("completion = %+v, want unchanged reasoning and answer", done)
	}
}

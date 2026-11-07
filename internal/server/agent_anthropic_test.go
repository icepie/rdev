package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"rdev/internal/agent"
)

func TestAgentAnthropicStreamsThroughWebSocketAndRestoresHistory(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" || r.Header.Get("anthropic-beta") != "effort-2025-11-24" {
			t.Error("wrong Anthropic route/effort headers")
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		thinking, _ := request["thinking"].(map[string]any)
		effort, _ := request["output_config"].(map[string]any)
		if request["max_tokens"] != float64(16384) || thinking["type"] != "adaptive" || effort["effort"] != "medium" || request["system"] == nil || request["messages"] == nil {
			t.Errorf("model configuration not forwarded: %v", request)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []string{
			`{"type":"message_start","message":{"usage":{"input_tokens":12,"output_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"planning"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"private-signature"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"answer"}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
			`{"type":"message_stop"}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", event)
		}
	}))
	defer provider.Close()
	srv := newTurnTestServer(t, provider.URL)
	srv.Agent.Providers[0].Models[0].API = "anthropic-messages" // model-level override
	srv.Agent.Providers[0].Models[0].MaxTokens = 16384
	srv.Agent.Providers[0].Models[0].Reasoning = true
	srv.Agent.Providers[0].Models[0].Thinking = agent.AnthropicThinkingConfig{Mode: "anthropic-adaptive"}
	server := httptest.NewServer(http.HandlerFunc(srv.HandleAgentWS))
	defer server.Close()
	socket, rec := dialAgentWS(t, server)
	authStart := waitAgentMsg(t, rec.msgs, "auth", func(m agentMsg) bool { return m.Op == "auth_ok" })
	if len(authStart.Models) != 2 || !reflect.DeepEqual(authStart.Models[0]["reasoningLevels"], []any{"low", "medium", "high"}) || authStart.Models[1]["reasoningLevels"] != nil {
		t.Fatalf("missing adaptive catalog: %+v", authStart.Models)
	}
	sendAgentOp(t, socket, `{"op":"chat","text":"hello"}`)
	thinking := waitAgentMsg(t, rec.msgs, "thinking", func(m agentMsg) bool { return m.Op == "reasoning" })
	answer := waitAgentMsg(t, rec.msgs, "answer", func(m agentMsg) bool { return m.Op == "delta" })
	done := waitAgentMsg(t, rec.msgs, "done", func(m agentMsg) bool { return m.Op == "done" })
	if thinking.Text != "planning" || answer.Text != "answer" || done.Usage == nil || done.Usage.InputTokens != 12 || done.Usage.OutputTokens != 3 {
		t.Fatalf("stream=%+v %+v %+v", thinking, answer, done)
	}
	socket.WriteClose(1000, nil)
	_, second := dialAgentWS(t, server)
	auth := waitAgentMsg(t, second.msgs, "restored auth", func(m agentMsg) bool { return m.Op == "auth_ok" })
	if len(auth.History) != 2 || auth.History[1].Text != "answer" || auth.History[1].Reasoning != "planning" {
		t.Fatalf("history=%+v", auth.History)
	}
	raw, _ := json.Marshal(auth)
	if strings.Contains(string(raw), "private-signature") {
		t.Fatal("opaque signature exposed to browser")
	}
}

func TestAnthropicSubagentsInheritProtocolAndRunConcurrently(t *testing.T) {
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model     string
			MaxTokens int `json:"max_tokens"`
			Thinking  map[string]any
			Messages  []struct{ Content []struct{ Text string } }
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/messages" || request.Model != "fixture" || request.MaxTokens != 4096 || request.Thinking["type"] != "enabled" || len(request.Messages) != 1 {
			t.Error("subagent lost protocol/config isolation")
		}
		arrived <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		text := ""
		if len(request.Messages) == 1 && len(request.Messages[0].Content) == 1 {
			text = request.Messages[0].Content[0].Text
		}
		answer, _ := json.Marshal(text)
		fmt.Fprint(w, `data: {"type":"message_start","message":{"usage":{"input_tokens":1}}}`+"\n\n")
		fmt.Fprintf(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":%s}}`+"\n\n", answer)
		fmt.Fprint(w, `data: {"type":"content_block_stop","index":0}`+"\n\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`+"\n\n"+`data: {"type":"message_stop"}`+"\n\n")
	}))
	defer provider.Close()
	jobs := newAgentJobs(nil)
	defer jobs.Close()
	tool := &subagentTool{jobs: jobs, config: agent.Config{API: "anthropic-messages", BaseURL: provider.URL, Model: "fixture", MaxOutputTokens: 4096, Thinking: agent.AnthropicThinkingConfig{Mode: "anthropic-budget"}, ReasoningEffort: "low"}, tools: func() []agent.Tool { return nil }}
	var ids []string
	for _, task := range []string{"first task", "second task"} {
		args, _ := json.Marshal(map[string]any{"task": task, "tools": []string{}})
		output, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatal(err)
		}
		var job agentJobSnapshot
		if err := json.Unmarshal([]byte(output), &job); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, job.ID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for range ids {
		select {
		case <-arrived:
		case <-ctx.Done():
			t.Fatal("subagents did not make concurrent requests")
		}
	}
	close(release)
	for i, id := range ids {
		result, err := jobs.Wait(ctx, id)
		if err != nil || result.State != "completed" || result.Result != []string{"first task", "second task"}[i] {
			t.Fatalf("crossed/failed subagent result: %+v %v", result, err)
		}
	}
}

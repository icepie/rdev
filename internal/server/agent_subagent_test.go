package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"rdev/internal/agent"
)

func TestSubagentUsesOnlyDelegatedToolsAndReturnsResult(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
			Messages []agent.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if len(request.Tools) != 1 || request.Tools[0].Function.Name != "todo_write" {
			t.Errorf("unexpected delegated tools: %+v", request.Tools)
		}
		if len(request.Messages) != 2 || request.Messages[1].Content != "Inspect the supplied context" {
			t.Errorf("unexpected child context: %+v", request.Messages)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Inspection complete\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer provider.Close()
	jobs := newAgentJobs(nil)
	defer jobs.Close()
	tool := &subagentTool{jobs: jobs, config: agent.Config{BaseURL: provider.URL, Model: "test", SystemPrompt: "device context"}, tools: func() []agent.Tool { return []agent.Tool{&todoWriteTool{}, &webFetchTool{}} }}
	output, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"Inspect the supplied context","tools":["todo_write"]}`))
	if err != nil {
		t.Fatal(err)
	}
	var started agentJobSnapshot
	if err := json.Unmarshal([]byte(output), &started); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := jobs.Wait(ctx, started.ID)
	if err != nil || result.State != "completed" || result.Result != "Inspection complete" {
		t.Fatalf("child result: %+v %v", result, err)
	}
}

func TestSubagentRejectsUnavailableCapabilitiesBeforeStarting(t *testing.T) {
	jobs := newAgentJobs(nil)
	defer jobs.Close()
	tool := &subagentTool{jobs: jobs, tools: func() []agent.Tool { return []agent.Tool{&webFetchTool{}} }}
	for _, name := range []string{"shell", "subagent", "bg"} {
		args, _ := json.Marshal(map[string]any{"task": "test", "tools": []string{name}})
		if _, err := tool.Execute(context.Background(), args); err == nil {
			t.Errorf("accepted unavailable tool %q", name)
		}
	}
	if len(jobs.List()) != 0 {
		t.Fatal("invalid grant launched a job")
	}
}

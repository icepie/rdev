package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"rdev/internal/agent"
)

func TestSubagentGuidanceReachesProviderInOrder(t *testing.T) {
	// MaxTurns=1 also exercises guidance admitted after the last in-turn
	// injection boundary: it must start a subsequent turn in the same child.
	for _, turns := range []int{1, 8} {
		t.Run(fmt.Sprint(turns), func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			calls := 0
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Messages []agent.Message `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				calls++
				want := []string{"inspect", "check errors", "report paths"}
				var users []string
				for _, msg := range request.Messages {
					if msg.Role == "user" {
						users = append(users, msg.Content)
					}
				}
				if calls > 3 || !reflect.DeepEqual(users, want[:min(calls, 3)]) {
					t.Errorf("unexpected child history: %v", users)
				}
				if calls == 1 {
					close(started)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer %d\"}}]}\n\ndata: [DONE]\n\n", calls)
			}))
			defer provider.Close()
			defer unblock()
			jobs := newAgentJobs(nil)
			defer jobs.Close()
			child := &subagentTool{jobs: jobs, config: agent.Config{BaseURL: provider.URL, Model: "test", MaxTurns: turns}, tools: func() []agent.Tool { return nil }}
			raw, err := child.Execute(t.Context(), json.RawMessage(`{"task":"inspect"}`))
			if err != nil {
				t.Fatal(err)
			}
			var job agentJobSnapshot
			if err := json.Unmarshal([]byte(raw), &job); err != nil {
				t.Fatal(err)
			}
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("child did not start")
			}
			tool := &jobsTool{jobs: jobs}
			for _, text := range []string{"check errors", "report paths"} {
				args, _ := json.Marshal(map[string]string{"action": "steer", "id": job.ID, "message": text})
				if _, err := tool.Execute(t.Context(), args); err != nil {
					t.Fatal(err)
				}
			}
			unblock()
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			result, err := jobs.Wait(ctx, job.ID)
			if err != nil || result.State != "completed" || result.Result != "answer 3" || len(result.PendingGuidance) != 0 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if !strings.Contains(result.Output, "[guidance] check errors") || !strings.Contains(result.Output, "[guidance] report paths") {
				t.Fatal("guidance missing from job output")
			}
			if err := jobs.Steer(job.ID, "too late"); err == nil {
				t.Fatal("completed job accepted guidance")
			}
		})
	}
}

func TestJobGuidanceBoundsCancellationAndIsolation(t *testing.T) {
	jobs := newAgentJobs(nil)
	defer jobs.Close()
	guidance := &agentJobGuidance{}
	job, err := jobs.start("subagent", "test", func(ctx context.Context, _ func(string)) (string, error) { <-ctx.Done(); return "", ctx.Err() }, guidance)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"", " \n", strings.Repeat("x", agentMaxGuidanceBytes+1)} {
		if err := jobs.Steer(job.ID, text); err == nil {
			t.Fatal("invalid guidance accepted")
		}
	}
	other := newAgentJobs(nil)
	defer other.Close()
	if err := other.Steer(job.ID, "wrong thread"); err == nil {
		t.Fatal("cross-thread steering accepted")
	}
	for range agentMaxPendingGuidance {
		if err := jobs.Steer(job.ID, "keep"); err != nil {
			t.Fatal(err)
		}
	}
	if err := jobs.Steer(job.ID, "overflow"); err == nil {
		t.Fatal("unbounded guidance")
	}
	if err := jobs.Cancel(job.ID); err != nil {
		t.Fatal(err)
	}
	if err := jobs.Steer(job.ID, "after cancellation"); err == nil {
		t.Fatal("canceled job accepted guidance")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	result, err := jobs.Wait(ctx, job.ID)
	if err != nil || result.State != "canceled" || len(result.PendingGuidance) != agentMaxPendingGuidance {
		t.Fatalf("lost pending guidance: %+v %v", result, err)
	}
	read, err := jobs.Read(job.ID)
	if err != nil || !reflect.DeepEqual(read.PendingGuidance, result.PendingGuidance) {
		t.Fatal("pending guidance unavailable in details")
	}
	read.PendingGuidance[0] = "mutated by reader"
	again, _ := jobs.Read(job.ID)
	if again.PendingGuidance[0] != "keep" {
		t.Fatal("details exposed mutable job state")
	}
	if len(jobs.List()[0].PendingGuidance) != 0 {
		t.Fatal("list exposed detail payload")
	}
	shell, err := jobs.Start("shell", "shell", func(ctx context.Context, _ func(string)) (string, error) { <-ctx.Done(); return "", ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.Steer(shell.ID, "not a child"); err == nil {
		t.Fatal("shell accepted guidance")
	}
}

func TestGuidanceCompletionAdmissionIsAtomic(t *testing.T) {
	for range 100 {
		g := &agentJobGuidance{}
		accepted := make(chan error, 1)
		go func() { accepted <- g.submit("last message") }()
		message := g.next(true)
		err := <-accepted
		if err == nil && (message == nil || message.Content != "last message") {
			t.Fatal("accepted guidance lost at completion")
		}
		if err != nil && message != nil {
			t.Fatal("rejected guidance consumed")
		}
	}
}

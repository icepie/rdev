package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSteeringReleasesOnlyOwningThreadWaitAndPreservesJob(t *testing.T) {
	for _, alreadyQueued := range []bool{false, true} {
		name := "during wait"
		if alreadyQueued {
			name = "before wait"
		}
		t.Run(name, func(t *testing.T) {
			srv := &Server{}
			th := &agentThread{id: "owner", running: true}
			other := &agentThread{id: "other", running: true}
			entry := &agentSessionEntry{threads: map[string]*agentThread{th.id: th, other.id: other}}
			ac := &agentConn{entry: entry, current: th.id}
			handler := &agentWSHandler{srv: srv}
			otherSignal := srv.agentSteeringSignal(other)
			jobs := newAgentJobs(nil)
			defer jobs.Close()
			finish, started := make(chan struct{}), make(chan struct{})
			job, err := jobs.Start("shell", "compile", func(ctx context.Context, output func(string)) (string, error) {
				output("still compiling")
				close(started)
				select {
				case <-finish:
					return "build passed", nil
				case <-ctx.Done():
					return "", ctx.Err()
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			<-started
			if alreadyQueued {
				handler.handleSteer(ac, "new direction", "", nil)
			}
			registered := make(chan struct{})
			tool := &jobsTool{jobs: jobs, steering: func() <-chan struct{} {
				signal := srv.agentSteeringSignal(th)
				close(registered)
				return signal
			}}
			args, _ := json.Marshal(map[string]any{"action": "wait", "id": job.ID, "timeout": 30})
			type result struct {
				text string
				err  error
			}
			resultCh := make(chan result, 1)
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			go func() { text, err := tool.Execute(ctx, args); resultCh <- result{text, err} }()
			<-registered
			if !alreadyQueued {
				handler.handleSteer(ac, "new direction", "", nil)
			}
			select {
			case response := <-resultCh:
				if response.err != nil {
					t.Fatal(response.err)
				}
				var snapshot struct {
					agentJobSnapshot
					WaitInterrupted bool `json:"waitInterrupted"`
				}
				if err := json.Unmarshal([]byte(response.text), &snapshot); err != nil {
					t.Fatal(err)
				}
				if !snapshot.WaitInterrupted || snapshot.State != "running" || snapshot.Output != "still compiling" {
					t.Fatalf("unexpected wait result: %s", response.text)
				}
			case <-ctx.Done():
				t.Fatal("steering did not release the wait")
			}
			select {
			case <-otherSignal:
				t.Fatal("steering interrupted another thread")
			default:
			}
			srv.agentSessionsMu.Lock()
			queued := len(th.queue) == 1 && th.queue[0].text == "new direction"
			srv.agentSessionsMu.Unlock()
			if !queued {
				t.Fatal("wait consumed the guidance before injection")
			}
			handler.handleDequeue(ac, "", 0)
			next := srv.agentSteeringSignal(th)
			select {
			case <-next:
				t.Fatal("removed steering interrupted a later wait")
			default:
			}
			close(finish)
			completed, err := jobs.Wait(ctx, job.ID)
			if err != nil || completed.State != "completed" || completed.Result != "build passed" {
				t.Fatalf("job did not survive steering: %+v %v", completed, err)
			}
			// The wait marker is local to this response, never stored as job state.
			output, err := (&jobsTool{jobs: jobs, steering: func() <-chan struct{} { return next }}).Execute(ctx, args)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			_ = json.Unmarshal([]byte(output), &fields)
			if _, ok := fields["waitInterrupted"]; ok {
				t.Fatal("completion retained a stale wait marker")
			}
		})
	}
}

func TestAgentSteeringContinuesAfterBackgroundWait(t *testing.T) {
	var hits atomic.Int32
	var jobID string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if hits.Add(1) == 1 {
			args, _ := json.Marshal(map[string]any{"action": "wait", "id": jobID, "timeout": 30})
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "waiting", "type": "function", "function": map[string]any{"name": "jobs", "arguments": string(args)}}}}}}})
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
			return
		}
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		n := len(request.Messages)
		if n < 2 || request.Messages[n-1].Content != "change plan" || request.Messages[n-2].Role != "tool" || !strings.Contains(request.Messages[n-2].Content, `"waitInterrupted":true`) {
			t.Errorf("missing interrupted wait before steering: %+v", request.Messages)
		}
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"adjusted"}}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer provider.Close()
	srv := &Server{Agent: &AgentConfig{Providers: []AgentProvider{{ID: "p", BaseURL: provider.URL, APIKey: "fixture", Models: []AgentModelConfig{{ID: "m"}}}}}, AgentMaxTurns: 4}
	entry, err := srv.agentSessionFor(agentServerDeviceID, &ClientConn{ID: agentServerDeviceID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.waitAgentWork)
	th := entry.threads[entry.current]
	defer th.jobs.Close()
	job, err := th.jobs.Start("shell", "long build", func(ctx context.Context, _ func(string)) (string, error) { <-ctx.Done(); return "", ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	jobID = job.ID
	handler := &agentWSHandler{srv: srv}
	ac := &agentConn{entry: entry, current: th.id, deviceID: entry.deviceID}
	handler.handleChat(ac, "wait for build", "", "", nil)
	waitFor(t, "passive wait registered", func() bool {
		srv.agentSessionsMu.Lock()
		defer srv.agentSessionsMu.Unlock()
		return th.steeringWake != nil
	})
	handler.handleSteer(ac, "change plan", "", nil)
	waitFor(t, "steered response finished", func() bool {
		srv.agentSessionsMu.Lock()
		defer srv.agentSessionsMu.Unlock()
		return !th.running && hits.Load() == 2
	})
	if !th.jobs.Running() {
		t.Fatal("guidance canceled the background task")
	}
}

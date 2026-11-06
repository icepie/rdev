package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"rdev/internal/protocol"
)

type jobTestTransport struct {
	recordTransport
	sent chan protocol.Message
}

func (t *jobTestTransport) WriteJSON(raw []byte) error {
	var message protocol.Message
	if err := json.Unmarshal(raw, &message); err != nil {
		return err
	}
	t.sent <- message
	return nil
}

func TestBackgroundCommandRoutesToDeviceAndRetainsTrailingOutput(t *testing.T) {
	for _, exitCode := range []int{0, 7} {
		t.Run(fmt.Sprint(exitCode), func(t *testing.T) {
			wantState := "completed"
			if exitCode != 0 {
				wantState = "failed"
			}

			srv := newPrefsTestServer(t)
			srv.sessions = make(map[string]*ProxySession)
			transport := &jobTestTransport{sent: make(chan protocol.Message, 4)}
			client := &ClientConn{ID: "remote-device", Transport: transport, Sessions: make(map[string]*ProxySession)}
			jobs := newAgentJobs(nil)
			defer jobs.Close()
			tool := &backgroundTool{jobs: jobs, shell: &shellTool{srv: srv, client: client, Cwd: "/project"}}
			result, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"build project"}`))
			if err != nil {
				t.Fatal(err)
			}
			var job agentJobSnapshot
			if err := json.Unmarshal([]byte(result), &job); err != nil {
				t.Fatal(err)
			}
			var request protocol.Message
			select {
			case request = <-transport.sent:
			case <-time.After(2 * time.Second):
				t.Fatal("no device request")
			}
			if request.Type != protocol.MsgNewSession || request.ClientID != client.ID || request.Command != "build project" || request.Cwd != "/project" {
				t.Fatalf("device request: %+v", request)
			}
			srv.mu.RLock()
			session := srv.sessions[request.SessionID]
			srv.mu.RUnlock()
			session.WriteCh <- []byte("build complete\n")
			session.StderrCh <- []byte("final diagnostic\n")
			session.SetExitCode(exitCode)
			session.SignalClose()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			completed, err := jobs.Wait(ctx, job.ID)
			if err != nil || completed.State != wantState {
				t.Fatalf("job: %+v %v", completed, err)
			}
			for _, want := range []string{"build complete", "final diagnostic", fmt.Sprintf("exit code: %d", exitCode)} {
				if !strings.Contains(completed.Result, want) {
					t.Errorf("result missing %q: %s", want, completed.Result)
				}
			}
		})
	}
}

func TestJobDeliveryQueuesOnceAndIgnoresOldThreadGeneration(t *testing.T) {
	srv := newPrefsTestServer(t)
	owner := newAgentJobs(nil)
	defer owner.Close()
	thread := &agentThread{id: "t_1", running: true, jobs: owner}
	entry := &agentSessionEntry{deviceID: "device", threads: map[string]*agentThread{"t_1": thread}}
	srv.deliverAgentJob(entry, thread, owner, agentJobSnapshot{ID: "job1", State: "completed", Result: "verified output"})
	if len(thread.queue) != 1 || !strings.Contains(thread.queue[0].text, "verified output") {
		t.Fatalf("completion not queued: %+v", thread.queue)
	}
	thread.jobs = newAgentJobs(nil)
	defer thread.jobs.Close()
	srv.deliverAgentJob(entry, thread, owner, agentJobSnapshot{ID: "old", State: "completed"})
	if len(thread.queue) != 1 {
		t.Fatal("old generation result was delivered after reset")
	}
}

func TestBackgroundRejectsMismatchedExplicitDevice(t *testing.T) {
	jobs := newAgentJobs(nil)
	defer jobs.Close()
	tool := &backgroundTool{jobs: jobs, shell: &shellTool{client: &ClientConn{ID: "device-a"}}}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"device":"device-b","command":"deploy"}`))
	if err == nil || !strings.Contains(err.Error(), "device-a") {
		t.Fatalf("wrong target accepted: %v", err)
	}
	if len(jobs.List()) != 0 {
		t.Fatal("created a job on the wrong device")
	}
}

func TestServerBackgroundRequiresRemoteDeviceBeforeJobAdmission(t *testing.T) {
	srv := newPrefsTestServer(t)
	srv.clients = map[string]*ClientConn{"locked": {ID: "locked", Password: "test-only"}}
	jobs := newAgentJobs(nil)
	defer jobs.Close()
	tool := &backgroundTool{jobs: jobs, resolveShell: func(device string) (*shellTool, error) {
		resolved, err := srv.resolveDeviceTool(device, "shell")
		if err != nil {
			return nil, err
		}
		return resolved.(*shellTool), nil
	}}
	for _, tc := range []struct{ device, message string }{
		{"", "remote device ID is required"},
		{agentServerDeviceID, "server host tools are unavailable"},
		{"offline", "offline"},
		{"locked", "admin token is required"},
	} {
		raw, err := json.Marshal(map[string]string{"command": "must not execute", "device": tc.device})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tool.Execute(context.Background(), raw); err == nil || !strings.Contains(err.Error(), tc.message) {
			t.Fatalf("device %q: got %v, want %q", tc.device, err, tc.message)
		}
		if len(jobs.List()) != 0 {
			t.Fatal("invalid target admitted a background job")
		}
	}
	for _, tc := range []struct {
		tool *backgroundTool
		want int
	}{{tool, 2}, {&backgroundTool{}, 1}} {
		var schema struct {
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(tc.tool.Schema(), &schema); err != nil {
			t.Fatal(err)
		}
		if len(schema.Required) != tc.want || schema.Required[0] != "command" || (tc.want == 2 && schema.Required[1] != "device") {
			t.Fatalf("wrong required arguments: %+v", schema.Required)
		}
	}
}

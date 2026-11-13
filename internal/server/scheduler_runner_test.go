package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"rdev/internal/agent"
	"rdev/internal/protocol"
)

func scheduledAgentSpec(mode string) ScheduleSpec {
	return ScheduleSpec{Name: "scheduled review", Kind: "agent", Device: agentServerDeviceID, SessionMode: mode, Model: "p/m2", Prompt: "scheduled prompt", Authorized: true, TimeoutSeconds: 5}
}

func TestScheduledAgentModesPreserveHistoryAndActualTarget(t *testing.T) {
	for _, mode := range []string{"new", "existing"} {
		t.Run(mode, func(t *testing.T) {
			requests := make(chan struct {
				Model    string
				Messages []agent.Message
			}, 1)
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model    string
					Messages []agent.Message
					Tools    []json.RawMessage
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if len(body.Tools) != 0 {
					t.Errorf("empty allowlist exposed tools: %s", body.Tools)
				}
				requests <- struct {
					Model    string
					Messages []agent.Message
				}{body.Model, body.Messages}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"scheduled answer\"}}]}\n\ndata: [DONE]\n\n")
			}))
			defer provider.Close()
			srv := newPrefsTestServer(t)
			srv.Agent.Providers[0].BaseURL = provider.URL
			entry, err := srv.agentSessionFor(agentServerDeviceID, nil)
			if err != nil {
				t.Fatal(err)
			}
			original := entry.threads[entry.current]
			original.session.SetHistory([]agent.Message{{Role: "user", Content: "earlier question"}, {Role: "assistant", Content: "earlier answer"}})
			original.cwd = "/existing"
			spec := scheduledAgentSpec(mode)
			spec.Thread, spec.Cwd = original.id, "/requested"
			var actual *ScheduleSpec
			var thread, turn string
			var output strings.Builder
			result := srv.runScheduledTask(t.Context(), ScheduleRun{ID: "run-exact", ScheduleID: "schedule", Snapshot: spec}, func(update ScheduledUpdate) {
				// Every observer callback must be outside the global state lock.
				srv.agentSessionsMu.Lock()
				srv.agentSessionsMu.Unlock()
				if update.Actual != nil {
					actual = update.Actual
					thread, turn = update.Thread, update.Turn
				}
				output.WriteString(update.Output)
			})
			if result.State != "succeeded" || output.String() != "scheduled answer" {
				t.Fatalf("result=%+v output=%q", result, output.String())
			}
			if actual == nil || thread == "" || turn != "run-exact" {
				t.Fatalf("missing exact turn link: %+v %s %s", actual, thread, turn)
			}
			request := <-requests
			history, model, cwd, title := srv.agentStore().load(spec.Device, thread)
			if mode == "existing" {
				if thread != original.id || actual.Cwd != "/existing" || actual.Model != "p/m1" || request.Model != "m1" || model != "p/m1" {
					t.Fatalf("existing target was overridden: %+v request=%+v model=%s", actual, request, model)
				}
				if len(history) != 4 || history[0].Content != "earlier question" {
					t.Fatalf("lost existing history: %+v", history)
				}
			} else {
				if thread == original.id || request.Model != "m2" || cwd != "/requested" || !strings.HasPrefix(title, spec.Name) {
					t.Fatalf("new target metadata incorrect: %+v model=%s cwd=%s title=%s", actual, request.Model, cwd, title)
				}
				if len(history) != 2 || len(original.session.History()) != 2 {
					t.Fatalf("mixed new/existing histories: %+v", history)
				}
			}
			if len(history) == 0 || !strings.Contains(string(history[len(history)-2].Display), "run-exact") {
				t.Fatalf("missing persisted turn identity: %+v", history)
			}
			if entry.threads[thread].scheduled != nil {
				t.Fatal("scheduled capabilities leaked to subsequent user turn")
			}
		})
	}
}

func TestScheduledPermissionsAreNonInteractiveAndIntersectExistingPolicy(t *testing.T) {
	srv := newPrefsTestServer(t)
	entry, err := srv.agentSessionFor(agentServerDeviceID, nil)
	if err != nil {
		t.Fatal(err)
	}
	th := entry.threads[entry.current]
	for _, mode := range []string{"new", "existing"} {
		spec := scheduledAgentSpec(mode)
		spec.AllowedTools = []string{"shell", "read_file", "ask", "subagent", "bg", "schedules", "device_tool"}
		turn := &scheduledAgentTurn{spec: spec, report: func(ScheduledUpdate) {}, cancel: func() {}}
		th.scheduled = turn
		cfg := agent.Config{Tools: []agent.Tool{&shellTool{}, &readFileTool{}, &askTool{}}}
		srv.configureScheduledAgent(entry, th, &cfg)
		if len(cfg.Tools) != 2 {
			t.Fatalf("unsafe tool filtering: %+v", cfg.Tools)
		}
		allow, err := cfg.Approve(agent.ToolCall{Name: "shell"}, nil)
		if err != nil || allow != (mode == "new") {
			t.Fatalf("mode=%s shell allowed=%v err=%v", mode, allow, err)
		}
		entry.permMode = agentPermAsk
		allow, _ = cfg.Approve(agent.ToolCall{Name: "read_file"}, nil)
		if allow != (mode == "new") {
			t.Fatalf("ask-mode widened for %s", mode)
		}
		entry.permMode = agentPermWrite
		if len(th.approvals) != 0 || len(th.questions) != 0 {
			t.Fatal("scheduled turn opened interactive approval")
		}
	}
	for _, command := range []string{"/bg sleep 100", "/subagent task", "/jobs", "/compact", "/goal objective", "/loop prompt"} {
		spec := scheduledAgentSpec("new")
		spec.Prompt = command
		if err := srv.validateScheduledTarget(spec); err == nil {
			t.Fatalf("accepted command %q", command)
		}
	}
}

func TestScheduledAgentUserQueuePriorityAndExactCompletion(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Messages []agent.Message }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		last := body.Messages[len(body.Messages)-1].Content
		if last == "scheduled prompt" {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\ndata: [DONE]\n\n", last+" answer")
	}))
	defer provider.Close()
	defer unblock()
	srv := newPrefsTestServer(t)
	srv.Agent.Providers[0].BaseURL = provider.URL
	entry, err := srv.agentSessionFor(agentServerDeviceID, nil)
	if err != nil {
		t.Fatal(err)
	}
	th := entry.threads[entry.current]
	spec := scheduledAgentSpec("existing")
	spec.Thread = th.id
	srv.agentSessionsMu.Lock()
	th.queue = []agentQueuedChat{{text: "queued user"}}
	srv.agentSessionsMu.Unlock()
	if ready, reason := srv.scheduledTaskReady(spec); ready || reason != "thread_busy" {
		t.Fatalf("queued user bypassed: %v %s", ready, reason)
	}
	if result := srv.runScheduledTask(t.Context(), ScheduleRun{ID: "blocked", Snapshot: spec}, nil); result.State != "skipped" {
		t.Fatalf("queued user bypassed at admission: %+v", result)
	}
	srv.agentSessionsMu.Lock()
	th.queue = nil
	srv.agentSessionsMu.Unlock()
	results := make(chan ScheduledResult, 1)
	var output strings.Builder
	go func() {
		results <- srv.runScheduledTask(t.Context(), ScheduleRun{ID: "scheduled", Snapshot: spec}, func(update ScheduledUpdate) { output.WriteString(update.Output) })
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("scheduled provider request not started")
	}
	srv.agentSessionsMu.Lock()
	th.queue = append(th.queue, agentQueuedChat{text: "queued user", steering: true})
	srv.agentSessionsMu.Unlock()
	unblock()
	select {
	case result := <-results:
		if result.State != "succeeded" || output.String() != "scheduled prompt answer" {
			t.Fatalf("captured wrong turn: %+v %q", result, output.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("scheduled result did not settle")
	}
	srv.waitAgentWork()
	history := th.session.History()
	if len(history) != 4 || history[2].Content != "queued user" {
		t.Fatalf("user queue lost or consumed inside scheduled turn: %+v", history)
	}
}

func TestScheduledAgentContextCancellationAndBudget(t *testing.T) {
	for _, budget := range []bool{false, true} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			entered := make(chan struct{})
			release := make(chan struct{})
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				if !budget {
					select {
					case <-r.Context().Done():
					case <-release:
					}
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"}}],\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":3}}\n\ndata: [DONE]\n\n")
			}))
			defer provider.Close()
			defer close(release)
			srv := newPrefsTestServer(t)
			srv.Agent.Providers[0].BaseURL = provider.URL
			spec := scheduledAgentSpec("new")
			if budget {
				spec.TokenBudget = 1
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			results := make(chan ScheduledResult, 1)
			go func() { results <- srv.runScheduledTask(ctx, ScheduleRun{ID: "cancel", Snapshot: spec}, nil) }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("provider request not started")
			}
			if !budget {
				cancel()
			}
			select {
			case result := <-results:
				if budget {
					if result.State != "failed" || result.Reason != "token budget exhausted" || result.Usage.InputTokens != 8 {
						t.Fatalf("budget not enforced: %+v", result)
					}
				} else if result.State != "canceled" {
					t.Fatalf("cancellation not propagated: %+v", result)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("context did not stop scheduled turn")
			}
		})
	}
}

func TestScheduledShellRoutesOutputExitAndIdentity(t *testing.T) {
	for _, code := range []int{0, 7, -1} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			srv := newPrefsTestServer(t)
			srv.sessions = make(map[string]*ProxySession)
			transport := &jobTestTransport{sent: make(chan protocol.Message, 4)}
			client := &ClientConn{ID: "remote", Transport: transport, Sessions: make(map[string]*ProxySession)}
			srv.clients = map[string]*ClientConn{client.ID: client}
			spec := ScheduleSpec{Kind: "shell", Device: client.ID, Cwd: "/project", Command: "echo scheduled", Authorized: true, TimeoutSeconds: 5}
			results := make(chan ScheduledResult, 1)
			var output strings.Builder
			go func() {
				results <- srv.runScheduledTask(t.Context(), ScheduleRun{ID: "shell-run", ScheduleID: "shell-schedule", Snapshot: spec}, func(update ScheduledUpdate) { output.WriteString(update.Output) })
			}()
			var request protocol.Message
			select {
			case request = <-transport.sent:
			case <-time.After(3 * time.Second):
				t.Fatal("device request missing")
			}
			if request.Type != protocol.MsgNewSession || request.Pty || request.Cwd != spec.Cwd || request.Command != spec.Command {
				t.Fatalf("wrong device execution: %+v", request)
			}
			if strings.Join(request.Env, "\n") != "RDEV_RUN_ID=shell-run\nRDEV_SCHEDULE_ID=shell-schedule" {
				t.Fatalf("run identity missing: %+v", request.Env)
			}
			srv.mu.RLock()
			session := srv.sessions[request.SessionID]
			srv.mu.RUnlock()
			session.WriteCh <- []byte("stdout\n")
			session.StderrCh <- []byte("stderr\n")
			if code >= 0 {
				session.SetExitCode(code)
			}
			session.SignalClose()
			select {
			case result := <-results:
				want := "succeeded"
				if code > 0 {
					want = "failed"
				} else if code < 0 {
					want = "unknown"
				}
				if result.State != want || !strings.Contains(output.String(), "stdout") || !strings.Contains(output.String(), "stderr") {
					t.Fatalf("result=%+v output=%q", result, output.String())
				}
				if code >= 0 && (result.ExitCode == nil || *result.ExitCode != code) {
					t.Fatalf("lost exit code: %+v", result)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("shell result missing")
			}
		})
	}
}

func TestScheduledExistingTargetPausedOnResetAndDelete(t *testing.T) {
	for _, action := range []string{"reset", "delete"} {
		t.Run(action, func(t *testing.T) {
			srv := newPrefsTestServer(t)
			srv.AdminToken = "admin"
			entry, err := srv.agentSessionFor(agentServerDeviceID, nil)
			if err != nil {
				t.Fatal(err)
			}
			th := entry.threads[entry.current]
			manager, err := srv.Scheduler()
			if err != nil {
				t.Fatal(err)
			}
			defer srv.StopScheduler()
			spec := scheduledAgentSpec("existing")
			spec.Thread, spec.Cron, spec.Timezone, spec.Enabled = th.id, "0 0 * * *", "UTC", true
			schedule, err := manager.Create(spec, "test")
			if err != nil {
				t.Fatal(err)
			}
			conn := &agentConn{deviceID: entry.deviceID, entry: entry, current: th.id, reply: func(agentMsg) {}}
			handler := &agentWSHandler{srv: srv}
			if action == "reset" {
				handler.handleReset(conn, th.id)
			} else {
				handler.handleDeleteThread(conn, th.id)
			}
			updated, err := manager.Get(schedule.ID)
			if err != nil || updated.State != "paused" || updated.Enabled {
				t.Fatalf("target invalidation lost: %+v %v", updated, err)
			}
		})
	}
}

func TestScheduledShellCancellationClosesRemoteAndKeepsOutcomeUnknown(t *testing.T) {
	srv := newPrefsTestServer(t)
	srv.sessions = make(map[string]*ProxySession)
	transport := &jobTestTransport{sent: make(chan protocol.Message, 4)}
	client := &ClientConn{ID: "remote", Transport: transport, Sessions: make(map[string]*ProxySession)}
	srv.clients = map[string]*ClientConn{client.ID: client}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	results := make(chan ScheduledResult, 1)
	go func() {
		results <- srv.runScheduledTask(ctx, ScheduleRun{ID: "cancel-shell", Snapshot: ScheduleSpec{Kind: "shell", Device: client.ID, Command: "long-running", Authorized: true}}, nil)
	}()
	var started protocol.Message
	select {
	case started = <-transport.sent:
	case <-time.After(3 * time.Second):
		t.Fatal("shell did not start")
	}
	cancel()
	select {
	case closed := <-transport.sent:
		if closed.Type != protocol.MsgClose || closed.SessionID != started.SessionID {
			t.Fatalf("wrong cancellation: %+v", closed)
		}
		srv.mu.RLock()
		session := srv.sessions[started.SessionID]
		srv.mu.RUnlock()
		session.SignalClose()
	case <-time.After(3 * time.Second):
		t.Fatal("remote cancellation not sent")
	}
	select {
	case result := <-results:
		if result.State != "unknown" || result.ExitCode != nil {
			t.Fatalf("unconfirmed remote cancellation reported success: %+v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shell cancellation did not settle")
	}
}

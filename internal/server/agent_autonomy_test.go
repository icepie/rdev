package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"rdev/internal/agent"
)

func autonomyFixture(t *testing.T, handler http.HandlerFunc) (*Server, *agentSessionEntry, *agentThread) {
	t.Helper()
	provider := httptest.NewServer(handler)
	t.Cleanup(provider.Close)
	s := newPrefsTestServer(t)
	s.Agent.Providers[0].BaseURL = provider.URL
	entry, err := s.agentSessionFor(agentServerDeviceID, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.waitAgentWork)
	return s, entry, entry.threads[entry.current]
}
func autonomyAnswer(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	content, _ := json.Marshal(text)
	fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%s}}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"prompt_tokens_details\":{\"cached_tokens\":4}}}\n\ndata: [DONE]\n\n", content)
}
func createAutonomy(t *testing.T, s *Server, entry *agentSessionEntry, th *agentThread, request agentMsg) {
	t.Helper()
	s.agentSessionsMu.Lock()
	err := mutateAutonomyLocked(th, request)
	s.agentSessionsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	s.persistAutonomy(entry, th)
	s.scheduleAutonomy(entry, th)
}

func TestGoalAutomaticNoProgressAndBudget(t *testing.T) {
	for _, mode := range []string{"no-progress", "budget"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			s, entry, th := autonomyFixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); autonomyAnswer(w, "Still blocked") })
			request := agentMsg{Op: "goal", Action: "create", Objective: "Fix the actual failure"}
			if mode == "budget" {
				budget := int64(8)
				request.TokenBudget = &budget
			}
			createAutonomy(t, s, entry, th, request)
			s.waitAgentWork()
			wantStatus, wantCalls := "paused", int32(2)
			if mode == "budget" {
				wantStatus, wantCalls = "budget-limited", 1
			}
			if th.goal.Status != wantStatus || calls.Load() != wantCalls {
				t.Fatalf("status=%s calls=%d", th.goal.Status, calls.Load())
			}
			if th.goal.TokensUsed != int64(wantCalls)*8 {
				t.Fatalf("cache reads charged: %+v", th.goal)
			}
			if th.running || th.autonomyPending {
				t.Fatal("automatic run did not settle")
			}
			if len(th.session.History()) != int(wantCalls)*2 {
				t.Fatal("history/live boundary lost a turn")
			}
		})
	}
}

func TestGoalModelCompletionAndToolGating(t *testing.T) {
	var calls atomic.Int32
	var sawGoal atomic.Bool
	s, entry, th := autonomyFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		for _, tool := range body.Tools {
			if tool.Function.Name == "goal" {
				sawGoal.Store(true)
			}
		}
		if calls.Add(1) == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"g1\",\"type\":\"function\",\"function\":{\"name\":\"goal\",\"arguments\":\"{\\\"op\\\":\\\"complete\\\"}\"}}]}}]}\n\ndata: [DONE]\n\n")
		} else {
			autonomyAnswer(w, "Goal achieved")
		}
	})
	createAutonomy(t, s, entry, th, agentMsg{Op: "goal", Action: "create", Objective: "Finish"})
	s.waitAgentWork()
	if !sawGoal.Load() || th.goal.Status != "complete" || calls.Load() != 2 {
		t.Fatalf("goal=%+v calls=%d enabled=%v", th.goal, calls.Load(), sawGoal.Load())
	}
	sawGoal.Store(false)
	s.startAgentTurn(entry, th, "ordinary user input", nil)
	s.waitAgentWork()
	if sawGoal.Load() {
		t.Fatal("goal tool remained enabled after completion")
	}
}

func TestLoopAutomaticCountAndUserQueuePriority(t *testing.T) {
	var mu sync.Mutex
	var prompts []string
	s, entry, th := autonomyFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		last := ""
		for _, m := range body.Messages {
			if m.Role == "user" {
				_ = json.Unmarshal(m.Content, &last)
			}
		}
		mu.Lock()
		prompts = append(prompts, last)
		mu.Unlock()
		autonomyAnswer(w, "Done")
	})
	s.agentSessionsMu.Lock()
	if err := mutateAutonomyLocked(th, agentMsg{Op: "loop", Action: "start", Text: "3 repeat me"}); err != nil {
		t.Fatal(err)
	}
	th.queue = []agentQueuedChat{{text: "user first"}}
	s.agentSessionsMu.Unlock()
	s.scheduleAutonomy(entry, th)
	if th.autonomyPending {
		t.Fatal("autonomy jumped ahead of queued user")
	}
	s.resumeAgentQueue(entry, th)
	s.waitAgentWork()
	if th.loop.Status != "complete" || th.loop.Iteration != 3 {
		t.Fatalf("loop=%+v", th.loop)
	}
	if len(prompts) != 4 || prompts[0] != "user first" {
		t.Fatalf("prompts=%v", prompts)
	}
	for _, text := range prompts[1:] {
		if text != "repeat me" {
			t.Fatalf("wrong repeated prompt %q", text)
		}
	}
}

func TestAutonomyStopResetDeleteInvalidateScheduledWork(t *testing.T) {
	for _, action := range []string{"stop", "reset", "delete"} {
		t.Run(action, func(t *testing.T) {
			var calls atomic.Int32
			s, entry, th := autonomyFixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); autonomyAnswer(w, "unexpected") })
			createAutonomy(t, s, entry, th, agentMsg{Op: "goal", Action: "create", Objective: "Do not resurrect"})
			h := &agentWSHandler{srv: s}
			ac := &agentConn{entry: entry, current: th.id, deviceID: entry.deviceID, reply: func(agentMsg) {}}
			switch action {
			case "stop":
				h.handleRPCAbort(ac, agentMsg{Thread: th.id})
			case "reset":
				h.handleReset(ac, th.id)
			case "delete":
				h.handleDeleteThread(ac, th.id)
			}
			s.waitAgentWork()
			if calls.Load() != 0 {
				t.Fatalf("scheduled work resurrected after %s", action)
			}
			if action == "stop" && th.goal.Status != "paused" {
				t.Fatal("stop did not pause")
			}
			if action == "reset" && (th.goal != nil || th.loop != nil || len(th.session.History()) != 0) {
				t.Fatal("reset retained autonomy")
			}
			if action == "delete" && entry.threads[th.id] != nil {
				t.Fatal("deleted thread returned")
			}
		})
	}
}

func TestAutonomyPersistenceRestoresPausedAndClearsTombstones(t *testing.T) {
	st := newTestStore(t)
	budget := int64(500)
	goal := &agentGoal{Objective: "durable", Status: "active", TokenBudget: &budget, TokensUsed: 31}
	loop := &agentLoop{Prompt: "repeat", Status: "active", Iteration: 2, MaxIterations: 5}
	st.saveAutonomy("d", "t", "p/m", goal, loop)
	st.save("d", "t", "p/m", []agent.Message{{Role: "user", Content: "history"}})
	reloaded := newAgentSessionStore(st.dir, st.ttl)
	reloaded.load("d", "t")
	g, l := reloaded.loadAutonomy("d", "t")
	if g == nil || g.Status != "paused" || g.TokensUsed != 31 || *g.TokenBudget != 500 || l.Status != "paused" || l.Iteration != 2 {
		t.Fatalf("restored goal=%+v loop=%+v", g, l)
	}
	reloaded.saveAutonomy("d", "t", "p/m", nil, nil)
	reloaded.replace("d", "t", "p/m", []agent.Message{{Role: "user", Content: "compacted"}})
	fresh := newAgentSessionStore(st.dir, st.ttl)
	fresh.load("d", "t")
	g, l = fresh.loadAutonomy("d", "t")
	if g != nil || l != nil {
		t.Fatal("drop/off resurrected through compaction")
	}
	fresh.saveAutonomy("d", "t", "p/m", goal, loop)
	fresh.clear("d", "t")
	fresh.save("d", "t", "p/m", nil)
	g, l = fresh.loadAutonomy("d", "t")
	if g != nil || l != nil {
		t.Fatal("reset retained metadata")
	}
}

func TestAutonomyThreadIsolationAndStaleCompletion(t *testing.T) {
	s := newPrefsTestServer(t)
	entry, err := s.agentSessionFor(agentServerDeviceID, nil)
	if err != nil {
		t.Fatal(err)
	}
	a := entry.threads[entry.current]
	b := s.newAgentThreadLocked(entry, "other", nil, "", "")
	entry.threads[b.id] = b
	if err := mutateAutonomyLocked(a, agentMsg{Op: "goal", Action: "create", Objective: "only a"}); err != nil {
		t.Fatal(err)
	}
	tool := &goalTool{srv: s, entry: entry, thread: a, goal: a.goal, epoch: a.epoch}
	if b.goal != nil {
		t.Fatal("goal leaked threads")
	}
	if err := mutateAutonomyLocked(a, agentMsg{Op: "goal", Action: "drop"}); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"op":"complete"}`)); err == nil {
		t.Fatal("stale model completed dropped goal")
	}
	if err := mutateAutonomyLocked(a, agentMsg{Op: "goal", Action: "create", Objective: "replacement"}); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"op":"complete"}`)); err == nil || a.goal.Status != "active" {
		t.Fatal("stale model completed replacement goal")
	}
}

func TestLoopParsingLimitsAndConditions(t *testing.T) {
	for _, tc := range []struct {
		text            string
		count, ms       int64
		command, prompt string
		until           bool
	}{
		{"3 --until 'go test ./...' fix tests", 3, 0, "go test ./...", "fix tests", true},
		{"1h30m --while='test -e pending' work", 0, 5400000, "test -e pending", "work", false},
		{"2 minutes\t--while\t'test -f work' repeat", 0, 120000, "test -f work", "repeat", false},
		{"keep going", 0, 0, "", "keep going", false},
	} {
		t.Run(tc.text, func(t *testing.T) {
			l, err := parseAgentLoop(tc.text)
			if err != nil {
				t.Fatal(err)
			}
			if l.MaxIterations != tc.count || l.DurationMs != tc.ms || l.Condition != tc.command || l.Prompt != tc.prompt || l.Until != tc.until {
				t.Fatalf("loop=%+v", l)
			}
		})
	}
	for _, text := range []string{"0 prompt", "-1 prompt", "1.5h prompt", "10x prompt", "--until", "--while 'unterminated", "--while a --until b p", "--oops x"} {
		if _, err := parseAgentLoop(text); err == nil {
			t.Errorf("accepted %q", text)
		}
	}
	for _, until := range []bool{false, true} {
		for _, code := range []int{0, 1, 2, -1} {
			pass, err := loopConditionContinues(code, until)
			if code < 0 || code > 1 {
				if err == nil {
					t.Errorf("accepted exit %d", code)
				}
			} else if err != nil || pass != ((code == 0) != until) {
				t.Fatalf("code=%d until=%v pass=%v err=%v", code, until, pass, err)
			}
		}
	}
}

func TestLoopPauseResumeDurationAndBusySlash(t *testing.T) {
	th := &agentThread{running: true}
	r, ok := autonomySlash("/loop 1m repeat")
	if !ok {
		t.Fatal("not a command")
	}
	if err := mutateAutonomyLocked(th, r); err != nil {
		t.Fatal(err)
	}
	th.loop.deadline = time.Now().Add(20 * time.Second)
	r, _ = autonomySlash("/loop pause")
	if err := mutateAutonomyLocked(th, r); err != nil {
		t.Fatal(err)
	}
	if th.loop.Status != "paused" || !th.running || th.loop.remaining > 20*time.Second {
		t.Fatal("pause changed active turn or lost budget")
	}
	r, _ = autonomySlash("/loop resume")
	if err := mutateAutonomyLocked(th, r); err != nil {
		t.Fatal(err)
	}
	if th.loop.Status != "active" || time.Until(th.loop.deadline) > 20*time.Second {
		t.Fatal("resume reset duration")
	}
	r, _ = autonomySlash("/loop off")
	_ = mutateAutonomyLocked(th, r)
	if th.loop != nil || !th.running {
		t.Fatal("off did not immediately remove loop")
	}
	if _, ok := autonomySlash("/goalkeeper"); ok {
		t.Fatal("accepted unrelated slash")
	}
	if r, ok := autonomySlash("/goal set finish exactly this"); !ok || r.Objective != "finish exactly this" || r.Action != "create" {
		t.Fatal("goal set parsing failed")
	}
}

func TestGoalErrorPausesWithoutDroppingQueue(t *testing.T) {
	th := &agentThread{goal: &agentGoal{Status: "active"}, loop: &agentLoop{Status: "active"}, queue: []agentQueuedChat{{text: "retain"}}}
	finishAutonomyLocked(th, agentTurnSnapshot{}, "error", "")
	if th.goal.Status != "paused" || th.loop.Status != "paused" || len(th.queue) != 1 {
		t.Fatal("error continuation/queue policy failed")
	}
	if strings.TrimSpace(goalTurnActivity([]agent.Message{{Role: "user", Content: "synthetic continuation"}})) != "" {
		t.Fatal("synthetic input counted as progress")
	}
}

func TestAutonomyNativeRPCAndBusySlash(t *testing.T) {
	s := newPrefsTestServer(t)
	entry, err := s.agentSessionFor(agentServerDeviceID, nil)
	if err != nil {
		t.Fatal(err)
	}
	th := entry.threads[entry.current]
	th.running = true // controls must not wait behind a foreground turn
	var replies []agentMsg
	ac := &agentConn{authed: true, deviceID: entry.deviceID, entry: entry, current: th.id, reply: func(m agentMsg) { replies = append(replies, m) }}
	h := &agentWSHandler{srv: s}
	for _, raw := range []string{
		`{"type":"goal","op":"create","objective":"RPC objective","token_budget":100,"id":"create"}`,
		`{"type":"prompt","message":"/goal pause","id":"pause"}`,
		`{"type":"goal","op":"get","id":"get"}`,
		`{"type":"goal","op":"resume","id":"resume"}`,
		`{"type":"follow_up","message":"/goal drop","id":"drop"}`,
	} {
		replies = nil
		if !h.handleAgentRPCFrame(ac, []byte(raw)) {
			t.Fatalf("RPC not handled: %s", raw)
		}
		found := false
		for _, m := range replies {
			if m.Type == "response" {
				found = true
				if m.Success == nil || !*m.Success {
					t.Fatalf("RPC failed: %+v", m)
				}
			}
		}
		if !found {
			t.Fatalf("missing correlated response: %s", raw)
		}
	}
	if th.goal != nil || len(th.queue) != 0 || !th.running {
		t.Fatal("busy control mutated conversation queue or retained dropped goal")
	}
	th.running = false
	if activity := goalTurnActivity([]agent.Message{{Role: "assistant", ToolCalls: []agent.ToolCall{{ID: "g", Name: "goal", Arguments: `{"op":"get"}`}}}, {Role: "tool", ToolCallID: "g", Content: `{"tokensUsed":99}`}}); activity != "" {
		t.Fatal("goal bookkeeping bypassed no-progress suppression")
	}
}

func TestGoalRunningStopDoesNotContinue(t *testing.T) {
	started := make(chan struct{})
	var calls atomic.Int32
	s, entry, th := autonomyFixture(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		if calls.Add(1) == 1 {
			close(started)
		}
		<-r.Context().Done()
	})
	createAutonomy(t, s, entry, th, agentMsg{Op: "goal", Action: "create", Objective: "Wait until interrupted"})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("goal never started")
	}
	ac := &agentConn{entry: entry, current: th.id, deviceID: entry.deviceID, reply: func(agentMsg) {}}
	(&agentWSHandler{srv: s}).handleRPCAbort(ac, agentMsg{Thread: th.id})
	s.waitAgentWork()
	if calls.Load() != 1 || th.goal.Status != "paused" || th.running || th.autonomyPending {
		t.Fatalf("stopped goal continued: calls=%d goal=%+v", calls.Load(), th.goal)
	}
}

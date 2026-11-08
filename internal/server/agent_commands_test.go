package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"rdev/internal/agent"
	"reflect"
	"strings"
	"testing"
)

func TestSplitAgentCommandAcceptsComposerWhitespace(t *testing.T) {
	for _, tc := range []struct{ input, command, argument string }{
		{"/compact", "/compact", ""},
		{" /COMPACT\tkeep device IDs\n ", "/compact", "keep device IDs"},
		{"/compact\nkeep errors", "/compact", "keep errors"},
		{"/compactly explain", "/compactly", "explain"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			command, argument := splitAgentCommand(tc.input)
			if command != tc.command || argument != tc.argument {
				t.Fatalf("got (%q, %q), want (%q, %q)", command, argument, tc.command, tc.argument)
			}
		})
	}
}

// Exercise the actual turn boundary: the compact command must replace the
// persisted context and publish the same history that reconnect will replay.
func TestCompactCommandPersistsAndPublishesReplacement(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Tools    []json.RawMessage `json:"tools"`
			Messages []agent.Message   `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if len(request.Tools) != 0 {
			t.Error("compaction exposed executable tools")
		}
		if len(request.Messages) == 0 || !strings.Contains(request.Messages[len(request.Messages)-1].Content, "retain paths") {
			t.Error("compaction focus was not sent")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Goal and file paths retained\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer provider.Close()
	srv := newPrefsTestServer(t)
	srv.Agent.Providers[0].BaseURL = provider.URL
	entry, err := srv.agentSessionFor(agentServerDeviceID, &ClientConn{ID: agentServerDeviceID})
	if err != nil {
		t.Fatal(err)
	}
	thread := entry.threads[entry.current]
	original := []agent.Message{{Role: "user", Content: "first task"}, {Role: "assistant", Content: "first result"}, {Role: "user", Content: "latest request"}, {Role: "assistant", Content: "latest result"}}
	thread.session.SetHistory(original)
	srv.agentStore().save(entry.deviceID, thread.id, thread.model, original)
	handler := &agentWSHandler{srv: srv}
	handler.handleChat(&agentConn{deviceID: entry.deviceID, entry: entry, current: thread.id}, "/compact retain paths", "", "", nil)
	waitFor(t, "compaction turn finished", func() bool { srv.agentSessionsMu.Lock(); defer srv.agentSessionsMu.Unlock(); return !thread.running })
	srv.agentSessionsMu.Lock()
	var terminal agentMsg
	live := thread.live.Snapshot()
	err = json.Unmarshal(live[len(live)-1], &terminal)
	expected := thread.session.History()
	srv.agentSessionsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Op != "done" || !terminal.Compacted {
		t.Fatalf("terminal: %+v", terminal)
	}
	loaded, _, _, _ := srv.agentStore().load(entry.deviceID, thread.id)
	if !reflect.DeepEqual(loaded, expected) {
		t.Fatal("reconnect would load stale history")
	}
	if !reflect.DeepEqual(terminal.History, renderAgentHistory(loaded)) {
		t.Fatal("live client and reconnect histories differ")
	}
}

func TestExplicitCommandArguments(t *testing.T) {
	for _, tc := range []struct {
		command, argument, name, expected string
	}{
		{"/bg", "printf 'a b'\necho done", "bg", `{"command":"printf 'a b'\necho done"}`},
		{"/bg", "--device machine-a echo hi", "bg", `{"command":"echo hi","device":"machine-a"}`},
		{"/bg", "--device\nmachine-a\ncat log\nprintf done", "bg", `{"command":"cat log\nprintf done","device":"machine-a"}`},
		{"/bg", "--device\u00a0machine-a\u2003echo hi", "bg", `{"command":"echo hi","device":"machine-a"}`},
		{"/bg", "--device-name echo hi", "bg", `{"command":"--device-name echo hi"}`},
		{"/subagent", "inspect\nthen report", "subagent", `{"task":"inspect\nthen report"}`},
		{"/jobs", "", "jobs", `{"action":"list"}`},
		{"/jobs", "full", "jobs", `{"action":"list","full":true}`},
		{"/jobs", "kill\nall", "jobs", `{"action":"cancel","id":"all"}`},
	} {
		name, raw, err := agentCommandTool(tc.command, tc.argument)
		if err != nil || name != tc.name || string(raw) != tc.expected {
			t.Fatalf("got %s %s %v", name, raw, err)
		}
	}
	for _, argument := range []string{"", "--device", "--device machine-a", "--device\n", "--device\nmachine-a\n"} {
		if _, _, err := agentCommandTool("/bg", argument); err == nil {
			t.Fatalf("accepted invalid argument %q", argument)
		}
	}
}

type commandDispatchTool struct {
	calls int
	args  json.RawMessage
}

func (*commandDispatchTool) Name() string            { return "bg" }
func (*commandDispatchTool) Description() string     { return "test" }
func (*commandDispatchTool) Schema() json.RawMessage { return json.RawMessage(`{}`) }
func (t *commandDispatchTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	t.calls++
	t.args = append(json.RawMessage(nil), args...)
	return "started", nil
}

func TestConversationDispatchesExplicitCommandWithoutModel(t *testing.T) {
	tool := &commandDispatchTool{}
	session := agent.New(agent.Config{Tools: []agent.Tool{tool}})
	replaced, err := runAgentConversation(t.Context(), session, "/bg echo hi", nil, nil)
	if err != nil || replaced || tool.calls != 1 || string(tool.args) != `{"command":"echo hi"}` {
		t.Fatalf("dispatch: replaced=%v calls=%d args=%s err=%v", replaced, tool.calls, tool.args, err)
	}
}

func TestExplicitCommandsRejectImagesWithoutMutatingHistory(t *testing.T) {
	for _, command := range []string{" /COMPACT\nretain paths", "/bg echo hi", "/subagent inspect", "/jobs full"} {
		t.Run(command, func(t *testing.T) {
			tool := &commandDispatchTool{}
			session := agent.New(agent.Config{Tools: []agent.Tool{tool}})
			original := []agent.Message{{Role: "user", Content: "keep this"}}
			session.SetHistory(original)
			replaced, err := runAgentConversation(t.Context(), session, command, []agent.MessageImage{{}}, func(agent.Event) { t.Error("rejected command emitted an event") })
			if err == nil || !strings.Contains(err.Error(), "does not accept images") || replaced {
				t.Fatalf("replaced=%v err=%v", replaced, err)
			}
			if tool.calls != 0 || !reflect.DeepEqual(session.History(), original) {
				t.Fatal("rejected command changed history or ran a tool")
			}
		})
	}
}

// Use real job managers and no provider: these commands must execute locally,
// and "all" must never cross the conversation's manager boundary.
func TestJobsCommandsWithoutModelAndThreadIsolation(t *testing.T) {
	local, other := newAgentJobs(nil), newAgentJobs(nil)
	defer local.Close()
	defer other.Close()
	worker := func(ctx context.Context, output func(string)) (string, error) {
		output("visible output")
		<-ctx.Done()
		return "", ctx.Err()
	}
	job, err := local.Start("shell", "local task", worker)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := other.Start("shell", "other task", worker)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "job output", func() bool { snapshot, _ := local.Read(job.ID); return snapshot.Output != "" })
	session := agent.New(agent.Config{Tools: []agent.Tool{&jobsTool{jobs: local}}})
	for _, command := range []string{"/jobs", "/jobs full"} {
		if replaced, err := runAgentConversation(t.Context(), session, command, nil, nil); err != nil || replaced {
			t.Fatalf("%s: replaced=%v err=%v", command, replaced, err)
		}
	}
	history := session.History()
	raw, _ := json.Marshal(history)
	if !strings.Contains(string(raw), "visible output") || strings.Contains(string(raw), foreign.ID) {
		t.Fatalf("missing output or cross-thread leak: %s", raw)
	}
	before := session.History()
	if _, err := runAgentConversation(t.Context(), session, "/jobs kill all extra", nil, nil); err == nil {
		t.Fatal("invalid jobs command accepted")
	}
	if !reflect.DeepEqual(before, session.History()) {
		t.Fatal("invalid command changed history")
	}
	if _, err := runAgentConversation(t.Context(), session, "/jobs kill all", nil, nil); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := local.Read(job.ID)
	untouched, _ := other.Read(foreign.ID)
	if !snapshot.CancelRequested || untouched.CancelRequested || untouched.State != "running" {
		t.Fatalf("wrong cancellation scope: local=%+v other=%+v", snapshot, untouched)
	}
}

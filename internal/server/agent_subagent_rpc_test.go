package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lxzan/gws"
)

// Use literal OMP command frames and inspect wire fields, rather than letting
// agentMsg silently discard or substitute incompatible response fields.
func TestSubagentRPCWireContract(t *testing.T) {
	srv := newPrefsTestServer(t)
	entry, err := srv.agentSessionFor(agentServerDeviceID, &ClientConn{ID: agentServerDeviceID})
	if err != nil {
		t.Fatal(err)
	}
	th := entry.threads[entry.current]
	jobs := newAgentJobs(nil)
	th.jobs.Close()
	th.jobs = jobs
	defer jobs.Close()
	release := make(chan struct{})
	defer close(release)
	work := func(ctx context.Context, _ func(string)) (string, error) {
		<-ctx.Done()
		<-release
		return "", ctx.Err()
	}
	guidance := &agentJobGuidance{}
	child, err := jobs.start("subagent", "inspect files", work, guidance)
	if err != nil {
		t.Fatal(err)
	}
	shell, err := jobs.Start("shell", "private shell", work)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(srv.HandleAgentWS))
	defer server.Close()
	rec := &rawWSRecorder{msgs: make(chan map[string]json.RawMessage, 64)}
	socket, _, err := gws.NewClient(rec, &gws.ClientOption{Addr: "ws" + strings.TrimPrefix(server.URL, "http") + "/agent?device=%40server", HandshakeTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.WriteClose(1000, nil)
	go socket.ReadLoop()
	waitRawMsg(t, rec.msgs, "auth", func(m map[string]json.RawMessage) bool { return string(m["op"]) == `"auth_ok"` })
	call := func(command, thread, target, message string, success bool) map[string]json.RawMessage {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"type": command, "id": "wire-1", "thread": thread, "subagentId": target, "message": message})
		sendAgentOp(t, socket, string(raw))
		reply := waitRawMsg(t, rec.msgs, "RPC response", func(m map[string]json.RawMessage) bool { return string(m["type"]) == `"response"` })
		wantCommand, _ := json.Marshal(command)
		wantSuccess, _ := json.Marshal(success)
		if string(reply["command"]) != string(wantCommand) || string(reply["id"]) != `"wire-1"` || string(reply["success"]) != string(wantSuccess) {
			t.Fatalf("wrong envelope: %s", reply)
		}
		if !success && len(reply["error"]) == 0 {
			t.Fatal("missing error")
		}
		return reply
	}
	reply := call("get_subagents", th.id, "", "", true)
	var data struct {
		Subagents []map[string]json.RawMessage `json:"subagents"`
	}
	if err := json.Unmarshal(reply["data"], &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Subagents) != 1 {
		t.Fatalf("leaked shell or missing child: %s", reply["data"])
	}
	for _, key := range []string{"id", "index", "agent", "agentSource", "status", "lastUpdate"} {
		if len(data.Subagents[0][key]) == 0 {
			t.Fatalf("missing OMP field %s", key)
		}
	}
	if string(data.Subagents[0]["status"]) != `"running"` {
		t.Fatal("wrong status")
	}
	for _, command := range []string{"negotiate_protocol", "get_available_commands"} {
		raw, _ := json.Marshal(map[string]any{"type": command, "id": command})
		sendAgentOp(t, socket, string(raw))
		protocol := waitRawMsg(t, rec.msgs, "protocol response", func(m map[string]json.RawMessage) bool {
			return string(m["type"]) == `"response"` && string(m["id"]) == `"`+command+`"`
		})
		if string(protocol["success"]) != "true" || len(protocol["data"]) == 0 {
			t.Fatalf("invalid %s response: %s", command, protocol)
		}
	}
	raw, _ := json.Marshal(map[string]any{"type": "get_state", "id": "state-1", "thread": th.id})
	sendAgentOp(t, socket, string(raw))
	state := waitRawMsg(t, rec.msgs, "state response", func(m map[string]json.RawMessage) bool {
		return string(m["type"]) == `"response"` && string(m["id"]) == `"state-1"`
	})
	if string(state["success"]) != "true" || !strings.Contains(string(state["data"]), `"thread":"`+th.id+`"`) {
		t.Fatalf("invalid state response: %s", state)
	}
	for _, command := range []string{"get_available_models"} {
		raw, _ := json.Marshal(map[string]any{"type": command, "id": command})
		sendAgentOp(t, socket, string(raw))
		models := waitRawMsg(t, rec.msgs, "models response", func(m map[string]json.RawMessage) bool {
			return string(m["type"]) == `"response"` && string(m["id"]) == `"`+command+`"`
		})
		if string(models["success"]) != "true" || !strings.Contains(string(models["data"]), `"p/m1"`) {
			t.Fatalf("invalid models response: %s", models)
		}
	}
	raw, _ = json.Marshal(map[string]any{"type": "set_model", "id": "model-1", "thread": th.id, "model": "p/m2"})
	sendAgentOp(t, socket, string(raw))
	modelReply := waitRawMsg(t, rec.msgs, "set model response", func(m map[string]json.RawMessage) bool {
		return string(m["type"]) == `"response"` && string(m["id"]) == `"model-1"`
	})
	if string(modelReply["success"]) != "true" || string(modelReply["data"]) != `{"model":"p/m2"}` {
		t.Fatalf("invalid set model response: %s", modelReply)
	}
	thinkingRaw, _ := json.Marshal(map[string]any{"type": "set_thinking_level", "id": "thinking-1", "level": "low"})
	sendAgentOp(t, socket, string(thinkingRaw))
	thinkingReply := waitRawMsg(t, rec.msgs, "thinking response", func(m map[string]json.RawMessage) bool {
		return string(m["type"]) == `"response"` && string(m["id"]) == `"thinking-1"`
	})
	if string(thinkingReply["success"]) != "true" || len(thinkingReply["data"]) != 0 {
		t.Fatalf("invalid thinking response: %s", thinkingReply)
	}
	todosRaw := `{"type":"set_todos","id":"todos-1","phases":[{"name":"Release","tasks":[{"content":"run tests","status":"in_progress"}]}]}`
	sendAgentOp(t, socket, todosRaw)
	todosReply := waitRawMsg(t, rec.msgs, "todos response", func(m map[string]json.RawMessage) bool {
		return string(m["type"]) == `"response"` && string(m["id"]) == `"todos-1"`
	})
	if string(todosReply["success"]) != "true" || !strings.Contains(string(todosReply["data"]), `"todoPhases"`) {
		t.Fatalf("invalid todo response: %s", todosReply)
	}
	stateRaw, _ := json.Marshal(map[string]any{"type": "get_state", "id": "state-todos"})
	sendAgentOp(t, socket, string(stateRaw))
	stateTodos := waitRawMsg(t, rec.msgs, "todo state", func(m map[string]json.RawMessage) bool {
		return string(m["type"]) == `"response"` && string(m["id"]) == `"state-todos"`
	})
	if !strings.Contains(string(stateTodos["data"]), `"todoPhases":[{"name":"Release"`) {
		t.Fatalf("state lost phase shape: %s", stateTodos)
	}
	raw, _ = json.Marshal(map[string]any{"type": "abort", "id": "abort-1", "thread": th.id})
	sendAgentOp(t, socket, string(raw))
	abortReply := waitRawMsg(t, rec.msgs, "abort response", func(m map[string]json.RawMessage) bool {
		return string(m["type"]) == `"response"` && string(m["id"]) == `"abort-1"`
	})
	if string(abortReply["success"]) != "true" || string(abortReply["data"]) != `{"aborted":false}` {
		t.Fatalf("invalid idle abort response: %s", abortReply)
	}
	promptRaw, _ := json.Marshal(map[string]any{"type": "prompt", "id": "prompt-1", "thread": th.id, "message": "hello"})
	sendAgentOp(t, socket, string(promptRaw))
	promptReply := waitRawMsg(t, rec.msgs, "prompt response", func(m map[string]json.RawMessage) bool {
		return string(m["type"]) == `"response"` && string(m["id"]) == `"prompt-1"`
	})
	if string(promptReply["success"]) != "false" || string(promptReply["code"]) != `"execution_error"` {
		t.Fatalf("prompt should report provider failure: %s", promptReply)
	}
	for _, command := range []string{"steer", "follow_up"} {
		queuedRaw, _ := json.Marshal(map[string]any{"type": command, "id": command + "-1", "thread": th.id, "message": "queued"})
		sendAgentOp(t, socket, string(queuedRaw))
		queuedReply := waitRawMsg(t, rec.msgs, "queued input response", func(m map[string]json.RawMessage) bool {
			return string(m["type"]) == `"response"` && string(m["id"]) == `"`+command+`-1"`
		})
		if string(queuedReply["success"]) != "true" {
			t.Fatalf("invalid %s response: %s", command, queuedReply)
		}
	}
	badQueue, _ := json.Marshal(map[string]any{"type": "follow_up", "id": "bad-follow", "thread": "missing", "message": "must reject"})
	sendAgentOp(t, socket, string(badQueue))
	badQueueReply := waitRawMsg(t, rec.msgs, "unknown queue thread", func(m map[string]json.RawMessage) bool {
		return string(m["type"]) == `"response"` && string(m["id"]) == `"bad-follow"`
	})
	if string(badQueueReply["success"]) != "false" || string(badQueueReply["code"]) != `"invalid_request"` {
		t.Fatalf("invalid queue accepted: %s", badQueueReply)
	}
	call("get_subagents", "missing", "", "", false)
	// Invalid RPC frames must return a correlated failure, not silently fall
	// into the browser protocol or wait forever for a reply.
	for _, raw := range []string{
		`{"type":"does_not_exist","id":"bad-1"}`,
		`{"type":"set_thinking_level","id":"bad-1","level":"ultra"}`,
		`{"type":"set_thinking_level","id":"bad-1","level":null}`,
		`{"type":"set_thinking_level","id":"bad-1"}`,
		`{"type":"set_thinking_level","id":"bad-1","level":"high","thread":"missing"}`,
		`{"type":"get_subagent_messages","id":"bad-1"}`,
		`{"type":"request","command":"reset","id":"bad-1"}`,
		`{"type":null,"id":"bad-1"}`,
		`{"type":"get_subagents","thread":42,"id":"bad-1"}`,
		`{"type":"steer_subagent","id":"bad-1","subagentId":42,"message":"test"}`,
		`{"type":"cancel_subagent","id":"bad-1","subagentId":null}`,
		`{"type":"get_subagents","id":"bad-1","op":"reset"}`,
	} {
		sendAgentOp(t, socket, raw)
		bad := waitRawMsg(t, rec.msgs, "invalid RPC response", func(m map[string]json.RawMessage) bool { return string(m["type"]) == `"response"` })
		if string(bad["id"]) != `"bad-1"` || string(bad["success"]) != "false" || len(bad["error"]) == 0 {
			t.Fatalf("invalid RPC was not rejected: %s", bad)
		}
	}
	srv.agentSessionsMu.Lock()
	level := entry.reasoningLevel
	srv.agentSessionsMu.Unlock()
	if level != "low" {
		t.Fatalf("invalid RPC changed thinking preference: %s", level)
	}
	call("steer_subagent", th.id, child.ID, "inspect tests", true)
	if got := guidance.snapshot(); len(got) != 1 || got[0] != "inspect tests" {
		t.Fatal(got)
	}
	// Transcript reads use the bounded rdev output window and remain a normal
	// correlated RPC response even before the subagent has produced output.
	transcriptRaw, _ := json.Marshal(map[string]any{"type": "get_subagent_messages", "id": "messages-1", "thread": th.id, "subagentId": child.ID, "fromByte": 0})
	sendAgentOp(t, socket, string(transcriptRaw))
	messages := waitRawMsg(t, rec.msgs, "subagent messages", func(m map[string]json.RawMessage) bool { return string(m["type"]) == `"response"` })
	if string(messages["success"]) != "true" || !strings.Contains(string(messages["data"]), `"sessionFile":"rdev://subagent/`) {
		t.Fatalf("invalid transcript response: %s", messages)
	}
	call("steer_subagent", "missing", child.ID, "foreign", false)
	call("steer_subagent", th.id, shell.ID, "wrong kind", false)
	call("cancel_subagent", th.id, shell.ID, "", false)
	for i, want := range []string{`{"cancelled":true}`, `{"cancelled":false}`} {
		reply = call("cancel_subagent", th.id, child.ID, "", true)
		if string(reply["data"]) != want {
			t.Fatalf("cancel %d: %s", i, reply["data"])
		}
		if len(reply["items"]) != 0 {
			t.Fatal("RPC result placed in browser items")
		}
	}
	call("steer_subagent", th.id, child.ID, "too late", false)
	reply = call("cancel_subagent", th.id, "vanished", "", true)
	if string(reply["data"]) != `{"cancelled":false}` {
		t.Fatal(string(reply["data"]))
	}
	reply = call("get_subagents", th.id, "", "", true)
	if string(reply["data"]) != `{"subagents":[]}` {
		t.Fatalf("expected empty array after cancel: %s", reply["data"])
	}
	alive, _ := jobs.Read(shell.ID)
	if alive.CancelRequested {
		t.Fatal("subagent cancellation affected shell")
	}
}

func TestAgentRPCRequiresAuthentication(t *testing.T) {
	handler := &agentWSHandler{}
	for _, frame := range []string{
		`{"type":"get_subagents"}`,
		`{"type":"compact","customInstructions":"keep paths"}`,
		`{"type":"set_thinking_level","level":"high"}`,
		`{"type":"remove_queued_message","queue":"steering","message":"x"}`,
		`{"type":"cancel_subagent","subagentId":"child"}`,
		`{"type":"request","command":"reset"}`,
	} {
		// No socket or server is supplied: an unauthenticated RPC must neither
		// dispatch an operation nor send a privileged response.
		if !handler.handleAgentRPCFrame(&agentConn{}, []byte(frame)) {
			t.Fatalf("RPC fell through auth gate: %s", frame)
		}
	}
	if handler.handleAgentRPCFrame(&agentConn{}, []byte(`{"op":"auth","password":"fixture"}`)) {
		t.Fatal("native authentication frame intercepted")
	}
}

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestRPCTodosRoundTripAndInvalidInput(t *testing.T) {
	srv := newTurnTestServer(t, "http://127.0.0.1:1")
	entry, err := srv.agentSessionFor("dev1", srv.clients["dev1"])
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(srv.HandleAgentWS))
	defer server.Close()
	socket, rec := dialAgentWS(t, server)
	waitAgentMsg(t, rec.msgs, "auth", func(m agentMsg) bool { return m.Op == "auth_ok" })
	call := func(command, extra string, ok bool) agentMsg {
		t.Helper()
		sendAgentOp(t, socket, `{"type":"`+command+`","id":"test"`+extra+`}`)
		reply := waitAgentMsg(t, rec.msgs, "response", func(m agentMsg) bool { return m.Type == "response" })
		if reply.Success == nil || *reply.Success != ok || reply.ID != "test" {
			t.Fatalf("unexpected reply: %+v", reply)
		}
		return reply
	}
	const phases = `[{"name":"Deploy","tasks":[{"content":"check","status":"blocked","blocker":"network","details":"retain details","notes":["first","second"]}]}]`
	call("set_todos", `,"phases":`+phases, true)
	check := func(want string) {
		t.Helper()
		result := call("get_state", "", true)
		var data map[string]json.RawMessage
		if err := json.Unmarshal(result.Data, &data); err != nil {
			t.Fatal(err)
		}
		var gotValue, wantValue any
		json.Unmarshal(data["todoPhases"], &gotValue)
		json.Unmarshal([]byte(want), &wantValue)
		if !reflect.DeepEqual(gotValue, wantValue) {
			t.Fatalf("phase round trip: %s", data["todoPhases"])
		}
	}
	check(phases)
	srv.agentSessionsMu.Lock()
	browserTodo := entry.threads[entry.current].todos[0]
	srv.agentSessionsMu.Unlock()
	if browserTodo.Blocker != "network" || browserTodo.Details != "retain details" || !reflect.DeepEqual(browserTodo.Notes, []string{"first", "second"}) {
		t.Fatalf("browser projection lost task metadata: %+v", browserTodo)
	}
	for _, bad := range []string{`null`, `{}`, `[null]`, `[{"name":"Deploy"}]`, `[{"name":"Deploy","tasks":null}]`, `[{"name":"Deploy","tasks":[{"content":"bad","status":"unknown"}]}]`, `[{"name":"Deploy","tasks":[{"content":"ok","status":"pending","notes":42}]}]`} {
		call("set_todos", `,"phases":`+bad, false)
		check(phases)
	}
	call("set_todos", `,"phases":`+phases+`,"thread":"missing"`, false)
	check(phases)
	call("set_todos", `,"phases":[]`, true)
	check(`[]`)
	srv.agentSessionsMu.Lock()
	defer srv.agentSessionsMu.Unlock()
	if len(entry.threads[entry.current].todos) != 0 {
		t.Fatal("explicit clear did not clear browser todos")
	}
}

package server

import (
	"encoding/json"
	"fmt"
	"github.com/lxzan/gws"
	"net/http"
	"net/http/httptest"
	"rdev/internal/agent"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRPCThinkingLevelsRespectCurrentModel(t *testing.T) {
	srv := newPrefsTestServer(t)
	srv.Agent.Providers[0].Models = append(srv.Agent.Providers[0].Models, AgentModelConfig{
		ID: "adaptive", API: "anthropic-messages", Thinking: agent.AnthropicThinkingConfig{Mode: "anthropic-adaptive"},
	})
	entry, err := srv.agentSessionFor(agentServerDeviceID, &ClientConn{ID: agentServerDeviceID})
	if err != nil {
		t.Fatal(err)
	}
	ac := &agentConn{entry: entry, current: entry.current}
	handler := &agentWSHandler{srv: srv}
	srv.agentSessionsMu.Lock()
	defer srv.agentSessionsMu.Unlock()
	thread := entry.threads[entry.current]
	thread.model = "p/adaptive"
	if got := handler.rpcThinkingLevels(ac); !reflect.DeepEqual(got, []string{"low", "medium", "high"}) {
		t.Fatalf("adaptive levels: %v", got)
	}
	thread.model = "p/m1"
	if got := handler.rpcThinkingLevels(ac); !reflect.DeepEqual(got, []string{"off", "low", "medium", "high"}) {
		t.Fatalf("default levels: %v", got)
	}
	ac.current = "missing"
	if got := handler.rpcThinkingLevels(ac); got != nil {
		t.Fatalf("unknown thread: %v", got)
	}
}

func TestRPCConcurrentThinkingCycles(t *testing.T) {
	srv := newPrefsTestServer(t)
	entry, err := srv.agentSessionFor(agentServerDeviceID, &ClientConn{ID: agentServerDeviceID})
	if err != nil {
		t.Fatal(err)
	}
	thread := entry.threads[entry.current]
	server := httptest.NewServer(http.HandlerFunc(srv.HandleAgentWS))
	defer server.Close()
	sockets := make([]*gws.Conn, 2)
	recorders := make([]*rawWSRecorder, 2)
	for i := range sockets {
		recorders[i] = &rawWSRecorder{msgs: make(chan map[string]json.RawMessage, 128)}
		sockets[i], _, err = gws.NewClient(recorders[i], &gws.ClientOption{Addr: "ws" + strings.TrimPrefix(server.URL, "http") + "/agent?device=%40server", HandshakeTimeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		defer sockets[i].WriteClose(1000, nil)
		go sockets[i].ReadLoop()
		waitRawMsg(t, recorders[i].msgs, "auth", func(m map[string]json.RawMessage) bool { return string(m["op"]) == `"auth_ok"` })
	}
	srv.agentSessionsMu.Lock()
	thread.running = true
	original := thread.session
	entry.reasoningLevel = "medium"
	srv.agentSessionsMu.Unlock()
	defer func() { srv.agentSessionsMu.Lock(); thread.running = false; srv.agentSessionsMu.Unlock() }()
	var workers sync.WaitGroup
	errors := make(chan error, 2)
	for i := range sockets {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			for n := 0; n < 8; n++ {
				raw := []byte(fmt.Sprintf(`{"type":"cycle_thinking_level","id":"cycle-%d-%d"}`, i, n))
				if err := sockets[i].WriteMessage(gws.OpcodeText, raw); err != nil {
					errors <- err
					return
				}
			}
		}(i)
	}
	workers.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for i := range recorders {
		for n := 0; n < 8; n++ {
			id := fmt.Sprintf(`"cycle-%d-%d"`, i, n)
			reply := waitRawMsg(t, recorders[i].msgs, "cycle response", func(m map[string]json.RawMessage) bool { return string(m["id"]) == id })
			var data struct {
				Level string `json:"level"`
			}
			if string(reply["success"]) != "true" || json.Unmarshal(reply["data"], &data) != nil {
				t.Fatalf("cycle failed: %s", reply)
			}
			counts[data.Level]++
		}
	}
	for _, level := range []string{"off", "low", "medium", "high"} {
		if counts[level] != 4 {
			t.Fatalf("lost cycle updates: %v", counts)
		}
	}
	srv.agentSessionsMu.Lock()
	defer srv.agentSessionsMu.Unlock()
	if entry.reasoningLevel != "medium" || !thread.pendingReasoning || thread.session != original {
		t.Fatal("cycles lost final state or replaced running session")
	}
}

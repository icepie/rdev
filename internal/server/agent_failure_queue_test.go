package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"rdev/internal/agent"
)

type panicAgentTransport struct{}

func (panicAgentTransport) RoundTrip(*http.Request) (*http.Response, error) {
	panic("fixture provider failure")
}

type panicOnceCommandTool struct{ calls int }

func (*panicOnceCommandTool) Name() string            { return "bg" }
func (*panicOnceCommandTool) Description() string     { return "fixture" }
func (*panicOnceCommandTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (tool *panicOnceCommandTool) Execute(context.Context, json.RawMessage) (string, error) {
	tool.calls++
	if tool.calls == 1 {
		panic("fixture tool failure")
	}
	return "queued command completed", nil
}

func TestUnexpectedConversationFailureStillAdvancesQueue(t *testing.T) {
	for _, source := range []string{"provider", "tool"} {
		t.Run(source, func(t *testing.T) {
			srv := newPrefsTestServer(t)
			entry, err := srv.agentSessionFor(agentServerDeviceID, &ClientConn{ID: agentServerDeviceID})
			if err != nil {
				t.Fatal(err)
			}
			th := entry.threads[entry.current]
			tool := &panicOnceCommandTool{}
			first := "/bg first"
			if source == "provider" {
				first = "provider request"
				tool.calls = 1 // The queued command is the successful execution.
			}
			th.session = agent.New(agent.Config{
				BaseURL: "http://fixture.invalid", Model: "fixture", Tools: []agent.Tool{tool},
				HTTPClient: &http.Client{Transport: panicAgentTransport{}},
			})
			srv.agentSessionsMu.Lock()
			srv.startAgentTurnLocked(entry, th, first, nil)
			turn := snapshotAgentTurnLocked(th)
			th.queue = []agentQueuedChat{{text: "/bg queued"}}
			srv.agentSessionsMu.Unlock()
			srv.runAgentTurn(entry, th, turn, first, nil)
			srv.waitAgentWork()
			srv.agentSessionsMu.Lock()
			idle := !th.running && len(th.queue) == 0 && th.lastOutcome == "done"
			srv.agentSessionsMu.Unlock()
			if !idle || tool.calls != 2 {
				t.Fatalf("queue stalled: idle=%v calls=%d", idle, tool.calls)
			}
			history, _, _, _ := srv.agentStore().load(entry.deviceID, th.id)
			if len(history) < 3 || history[len(history)-1].Content != "queued command completed" {
				t.Fatalf("queued completion not persisted: %+v", history)
			}
			if source == "tool" && (len(history) != 6 || !strings.Contains(history[2].Content, "internal tool failure")) {
				t.Fatalf("failed command history invalid: %+v", history)
			}
		})
	}
}

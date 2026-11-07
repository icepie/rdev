package server

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"rdev/internal/agent"
)

type resetGateTool struct {
	started     chan struct{}
	release     chan struct{}
	panicOnExit bool
	once        sync.Once
}

func (tool *resetGateTool) unblock() { tool.once.Do(func() { close(tool.release) }) }

func (*resetGateTool) Name() string            { return "bg" }
func (*resetGateTool) Description() string     { return "gated test tool" }
func (*resetGateTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (tool *resetGateTool) Execute(context.Context, json.RawMessage) (string, error) {
	close(tool.started)
	<-tool.release // Simulates a device cleanup that has not observed cancellation yet.
	if tool.panicOnExit {
		panic("old device cleanup failed")
	}
	return "finished", nil
}

func TestResetLateTurnCannotClearReplacementTurn(t *testing.T) {
	for _, panicOnExit := range []bool{false, true} {
		name := "normal"
		if panicOnExit {
			name = "panic"
		}
		t.Run(name, func(t *testing.T) {
			srv := newPrefsTestServer(t)
			entry, err := srv.agentSessionFor(agentServerDeviceID, &ClientConn{ID: agentServerDeviceID})
			if err != nil {
				t.Fatal(err)
			}
			th := entry.threads[entry.current]
			start := func(tool *resetGateTool) (agentTurnSnapshot, <-chan struct{}) {
				srv.agentSessionsMu.Lock()
				th.session = agent.New(agent.Config{Tools: []agent.Tool{tool}})
				srv.startAgentTurnLocked(entry, th, "/bg inspect", nil)
				turn := snapshotAgentTurnLocked(th)
				srv.agentSessionsMu.Unlock()
				done := make(chan struct{})
				go func() { defer close(done); srv.runAgentTurn(entry, th, turn, "/bg inspect", nil) }()
				t.Cleanup(func() { tool.unblock(); <-done })
				select {
				case <-tool.started:
				case <-time.After(time.Second):
					t.Fatal("turn did not reach tool")
				}
				return turn, done
			}
			old := &resetGateTool{started: make(chan struct{}), release: make(chan struct{}), panicOnExit: panicOnExit}
			_, oldDone := start(old)
			(&agentWSHandler{srv: srv}).handleReset(&agentConn{entry: entry, current: th.id, deviceID: entry.deviceID}, th.id)
			srv.agentSessionsMu.Lock()
			idle := !th.running && th.cancel == nil && th.turnCtx == nil
			srv.agentSessionsMu.Unlock()
			if !idle {
				t.Fatal("reset left old turn occupying the thread")
			}
			fresh := &resetGateTool{started: make(chan struct{}), release: make(chan struct{})}
			newTurn, newDone := start(fresh)
			pendingApproval := make(chan approvalDecision, 1)
			srv.agentSessionsMu.Lock()
			th.approvals["replacement"] = pendingApproval
			th.queue = []agentQueuedChat{{text: "queued for new turn"}}
			srv.agentSessionsMu.Unlock()
			old.unblock()
			select {
			case <-oldDone:
			case <-time.After(time.Second):
				t.Fatal("old turn did not finish")
			}
			srv.agentSessionsMu.Lock()
			intact := th.running && th.turnCtx == newTurn.ctx && th.cancel != nil && th.workingTool == "bg" &&
				len(th.queue) == 1 && th.queue[0].text == "queued for new turn" &&
				th.approvals["replacement"] == pendingApproval && len(pendingApproval) == 0
			th.queue = nil // The assertion checks ownership; avoid launching another fixture turn.
			srv.agentSessionsMu.Unlock()
			if !intact {
				t.Fatal("old completion modified replacement state or consumed its queue")
			}
			if newTurn.ctx.Err() != nil {
				t.Fatal("old completion canceled the replacement")
			}
			fresh.unblock()
			select {
			case <-newDone:
			case <-time.After(time.Second):
				t.Fatal("new turn did not finish")
			}
			history, _, _, _ := srv.agentStore().load(entry.deviceID, th.id)
			if len(history) != 3 || history[0].Content != "/bg inspect" || history[2].Content != "finished" {
				t.Fatalf("replacement history=%+v", history)
			}
		})
	}
}

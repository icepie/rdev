package server

import (
	"context"
	"testing"

	"rdev/internal/agent"
)

func TestLoopConditionUsesDeviceAndApproval(t *testing.T) {
	for _, until := range []bool{false, true} {
		for _, code := range []int{0, 1, 2} {
			s, tr := newProbeTestServer(t, func(command string) (string, int) {
				if command != "condition-command" {
					t.Errorf("unexpected command %q", command)
				}
				return "result", code
			})
			th := &agentThread{id: "condition-thread", session: agent.New(agent.Config{}), cwd: "/authorized/work", approvals: make(map[string]chan approvalDecision)}
			entry := &agentSessionEntry{deviceID: "dev1", permMode: agentPermFull, threads: map[string]*agentThread{th.id: th}}
			pass, err := s.evaluateLoopCondition(context.Background(), entry, th, &agentLoop{Condition: "condition-command", Until: until})
			if code > 1 {
				if err == nil {
					t.Fatal("condition execution error treated as false")
				}
			} else if err != nil || pass != ((code == 0) != until) {
				t.Fatalf("code=%d until=%v pass=%v error=%v", code, until, pass, err)
			}
			if commands := tr.commands(); len(commands) != 1 || commands[0] != "condition-command" {
				t.Fatalf("device commands=%v", commands)
			}
		}
	}
	s, tr := newProbeTestServer(t, func(string) (string, int) { t.Error("denied condition executed"); return "", 0 })
	th := &agentThread{id: "denied", session: agent.New(agent.Config{}), approvals: make(map[string]chan approvalDecision)}
	entry := &agentSessionEntry{deviceID: "dev1", permMode: agentPermAsk, threads: map[string]*agentThread{th.id: th}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if pass, err := s.evaluateLoopCondition(ctx, entry, th, &agentLoop{Condition: "never execute"}); err == nil || pass {
		t.Fatal("canceled approval ran condition")
	}
	if len(tr.commands()) != 0 {
		t.Fatal("condition bypassed approval")
	}
	entry.deviceID = agentServerDeviceID
	if _, err := s.evaluateLoopCondition(context.Background(), entry, th, &agentLoop{Condition: "never run on server"}); err == nil {
		t.Fatal("condition escaped into server shell")
	}
}

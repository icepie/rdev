package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"rdev/internal/agent"
)

func TestApprovalAndQuestionRepliesStayInTheirThread(t *testing.T) {
	for _, kind := range []string{"approval", "question"} {
		t.Run(kind, func(t *testing.T) {
			srv := &Server{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			makeThread := func(id string) *agentThread {
				return &agentThread{id: id, turnCtx: ctx, approvals: make(map[string]chan approvalDecision), questions: make(map[string]chan string)}
			}
			first, second := makeThread("first"), makeThread("second")
			entry := &agentSessionEntry{permMode: agentPermAsk, threads: map[string]*agentThread{"first": first, "second": second}}
			handler := &agentWSHandler{srv: srv}
			conn := &agentConn{entry: entry}
			results := []chan string{make(chan string, 1), make(chan string, 1)}
			for i, thread := range []*agentThread{first, second} {
				go func(i int, thread *agentThread) {
					if kind == "question" {
						results[i] <- srv.requestQuestion(entry, thread, "Which option?", json.RawMessage(`[]`))
						return
					}
					allowed, _ := srv.requestApproval(entry, thread, agent.ToolCall{Name: "shell"}, json.RawMessage(`{"command":"test"}`))
					if allowed {
						results[i] <- "allowed"
					} else {
						results[i] <- "denied"
					}
				}(i, thread)
			}
			var ids [2]string
			waitFor(t, "both requests pending", func() bool {
				srv.agentSessionsMu.Lock()
				defer srv.agentSessionsMu.Unlock()
				for i, thread := range []*agentThread{first, second} {
					if kind == "question" {
						for id := range thread.questions {
							ids[i] = id
						}
					} else {
						for id := range thread.approvals {
							ids[i] = id
						}
					}
				}
				return ids[0] != "" && ids[1] != ""
			})
			if ids[0] == ids[1] {
				t.Fatal("two threads received the same reply address")
			}
			expected := []string{"allowed", "denied"}
			if kind == "question" {
				expected = []string{"first answer", "second answer"}
				handler.handleAskResponse(conn, ids[1], expected[1])
				handler.handleAskResponse(conn, ids[0], expected[0])
			} else {
				handler.handleApprove(conn, ids[1], false, false)
				handler.handleApprove(conn, ids[0], true, false)
			}
			for i, result := range results {
				select {
				case got := <-result:
					if got != expected[i] {
						t.Errorf("thread %d got %q, want %q", i, got, expected[i])
					}
				case <-time.After(time.Second):
					t.Fatal("reply did not reach its thread")
				}
			}
		})
	}
}

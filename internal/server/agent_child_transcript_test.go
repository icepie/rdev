package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/lxzan/gws"
	"rdev/internal/agent"
)

func TestJobOutputAbsoluteOffsetsOverflowAndFinalResult(t *testing.T) {
	jobs := newAgentJobs(nil)
	defer jobs.Close()
	streamed, finish := make(chan struct{}), make(chan struct{})
	job, err := jobs.Start("shell", "cursor", func(_ context.Context, output func(string)) (string, error) {
		output(strings.Repeat("a", agentJobOutputLimit))
		output("界")
		close(streamed)
		<-finish
		return strings.Repeat("b", agentJobOutputLimit), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-streamed
	live, err := jobs.Read(job.ID)
	if err != nil || live.OutputNext != agentJobOutputLimit+3 || live.OutputBase != 3 || !strings.HasSuffix(live.Output, "界") {
		t.Fatalf("saturated cursor failed: %+v %v", live, err)
	}
	close(finish)
	final, err := jobs.Wait(t.Context(), job.ID)
	if err != nil || final.OutputNext <= live.OutputNext || final.OutputBase <= live.OutputBase || final.Output != strings.Repeat("b", agentJobOutputLimit) {
		t.Fatalf("same-sized result did not advance absolute cursor: %+v %v", final, err)
	}
	if final.OutputNext-final.OutputBase != int64(len(final.Output)) {
		t.Fatal("output window does not match absolute offsets")
	}
}

func TestJobOutputSplitUTF8DoesNotConsumeIncompleteRune(t *testing.T) {
	var output agentJobOutput
	output.append(strings.Repeat("a", agentJobOutputLimit-1))
	output.append("\xe7")
	text, base, next := output.window()
	if !utf8.ValidString(text) || base != 0 || next != agentJobOutputLimit-1 {
		t.Fatalf("incomplete rune published: bytes=%d base=%d next=%d", len(text), base, next)
	}
	output.append("\x95\x8c")
	text, base, next = output.window()
	if !utf8.ValidString(text) || !strings.HasSuffix(text, "界") || base != 2 || next != agentJobOutputLimit+2 {
		t.Fatalf("completed rune lost: bytes=%d base=%d next=%d", len(text), base, next)
	}
	var initial agentJobOutput
	initial.append("\xf0\x9f")
	text, base, next = initial.window()
	if text != "" || base != 0 || next != 0 {
		t.Fatalf("initial partial rune consumed: %q %d %d", text, base, next)
	}
	initial.append("\x8c\x88")
	text, base, next = initial.window()
	if text != "🌈" || base != 0 || next != 4 {
		t.Fatalf("split rune not restored: %q %d %d", text, base, next)
	}
}

func newChildTranscriptFixture(t *testing.T, durable bool) (*agentJobs, string) {
	t.Helper()
	jobs := newAgentJobs(nil)
	if durable {
		jobs.childTranscriptDir = filepath.Join(t.TempDir(), "children")
	}
	t.Cleanup(jobs.Close)
	id := "child-fixture"
	jobs.mu.Lock()
	err := jobs.createChildTranscriptLocked(id)
	jobs.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	return jobs, id
}

func childTranscriptMessages(t *testing.T, page ompSubagentMessages) []agentHistoryLine {
	t.Helper()
	messages := make([]agentHistoryLine, len(page.Messages))
	for i, raw := range page.Messages {
		if err := json.Unmarshal(raw, &messages[i]); err != nil {
			t.Fatal(err)
		}
	}
	return messages
}

func TestChildTranscriptStructuredHistoryCompactionAndCursor(t *testing.T) {
	for _, durable := range []bool{false, true} {
		t.Run(fmt.Sprint(durable), func(t *testing.T) {
			jobs, id := newChildTranscriptFixture(t, durable)
			fullResult := strings.Repeat("successful tool output 界\n", 6000)
			history := []agent.Message{
				{MessageID: "user", Role: "user", Content: "inspect"},
				{MessageID: "call", Role: "assistant", Reasoning: "reasoned plan", ToolCalls: []agent.ToolCall{{ID: "tool-1", Name: "read_file", Arguments: `{"path":"a"}`, Summary: "a"}}},
				{MessageID: "result", Role: "tool", ToolCallID: "tool-1", Content: "model-context shortened", ArchiveContent: fullResult},
			}
			if err := jobs.captureChildHistory(id, history); err != nil {
				t.Fatal(err)
			}
			first, err := jobs.readChildTranscript(id, 0)
			if err != nil || first.Reset || first.HasMore {
				t.Fatalf("first page: %+v %v", first, err)
			}
			messages := childTranscriptMessages(t, first)
			if len(messages) != 3 || messages[1].Reasoning != "reasoned plan" || messages[2].Content != fullResult || messages[2].ToolCallID != "tool-1" {
				t.Fatal("real reasoning/tool transcript lost or fabricated")
			}
			if err := jobs.captureChildHistory(id, history); err != nil {
				t.Fatal(err)
			}
			empty, err := jobs.readChildTranscript(id, first.NextByte)
			if err != nil || empty.NextByte != first.NextByte || len(empty.Entries) != 0 {
				t.Fatalf("repeated context duplicated records: %+v %v", empty, err)
			}
			// Compaction can replace context, not the archive. Same-sized edits
			// to a real message append a revision identified by the same ID.
			compacted := []agent.Message{{MessageID: "summary", ContextOnly: true, Role: "user", Content: "summary"}, {MessageID: "answer", Role: "assistant", Content: "aaaa"}}
			if err := jobs.captureChildHistory(id, compacted); err != nil {
				t.Fatal(err)
			}
			answer, err := jobs.readChildTranscript(id, first.NextByte)
			if err != nil || len(answer.Messages) != 1 {
				t.Fatalf("compaction records: %+v %v", answer, err)
			}
			compacted[1].Content = "bbbb"
			if err := jobs.captureChildHistory(id, compacted); err != nil {
				t.Fatal(err)
			}
			revised, err := jobs.readChildTranscript(id, answer.NextByte)
			if err != nil || revised.NextByte <= answer.NextByte || childTranscriptMessages(t, revised)[0].Content != "bbbb" {
				t.Fatalf("same-size revision invisible: %+v %v", revised, err)
			}
			repeated, err := jobs.readChildTranscript(id, answer.NextByte)
			if err != nil || !reflect.DeepEqual(revised, repeated) {
				t.Fatal("same cursor is not idempotent")
			}
			for _, cursor := range []int64{-1, 1, first.NextByte - 1, revised.NextByte + 1} {
				page, err := jobs.readChildTranscript(id, cursor)
				if err != nil || !page.Reset || page.FromByte != 0 {
					t.Fatalf("invalid record cursor %d: %+v %v", cursor, page, err)
				}
			}
			if durable {
				reloaded := newAgentJobs(nil)
				reloaded.childTranscriptDir = jobs.childTranscriptDir
				defer reloaded.Close()
				page, err := reloaded.readChildTranscript(id, answer.NextByte)
				if err != nil || !reflect.DeepEqual(page, revised) {
					t.Fatal("restart changed cursor or transcript", err)
				}
			}
		})
	}
}

func TestChildTranscriptPaginationPreservesEveryMessage(t *testing.T) {
	jobs, id := newChildTranscriptFixture(t, true)
	history := make([]agent.Message, agentChildPageEntries*2+7)
	for i := range history {
		history[i] = agent.Message{MessageID: fmt.Sprint(i), Role: "user", Content: "message"}
	}
	if err := jobs.captureChildHistory(id, history); err != nil {
		t.Fatal(err)
	}
	cursor, count := int64(0), 0
	for {
		page, err := jobs.readChildTranscript(id, cursor)
		if err != nil || page.Reset || page.FromByte != cursor {
			t.Fatalf("page failed: %+v %v", page, err)
		}
		for _, message := range childTranscriptMessages(t, page) {
			if message.MessageID != fmt.Sprint(count) {
				t.Fatalf("message skipped/repeated at %d: %s", count, message.MessageID)
			}
			count++
		}
		cursor = page.NextByte
		if !page.HasMore {
			break
		}
	}
	if count != len(history) {
		t.Fatalf("read %d of %d messages", count, len(history))
	}
}

func TestChildTranscriptQuotaAndClosedOwnerAreExplicit(t *testing.T) {
	jobs, id := newChildTranscriptFixture(t, false)
	before, _ := jobs.readChildTranscript(id, 0)
	jobs.children[id].size = agentChildTranscriptLimit - agentChildQuotaReserve
	err := jobs.captureChildHistory(id, []agent.Message{{MessageID: "too-large", Role: "tool", Content: "not silently discarded"}})
	if err == nil || !strings.Contains(err.Error(), "quota") {
		t.Fatal("quota overflow was not explicit", err)
	}
	jobs.children[id].size = before.NextByte
	after, err := jobs.readChildTranscript(id, 0)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("quota failure mutated existing transcript", err)
	}
	durable, durableID := newChildTranscriptFixture(t, true)
	durable.Close()
	if err := os.RemoveAll(durable.childTranscriptDir); err != nil {
		t.Fatal(err)
	}
	if err := durable.captureChildHistory(durableID, []agent.Message{{MessageID: "late", Role: "assistant", Content: "late"}}); err == nil {
		t.Fatal("closed owner accepted late capture")
	}
	if _, err := os.Stat(durable.childTranscriptDir); !os.IsNotExist(err) {
		t.Fatal("late callback resurrected deleted archive", err)
	}
}

type childTranscriptFixtureTool struct{ result string }

func (*childTranscriptFixtureTool) Name() string        { return "read_file" }
func (*childTranscriptFixtureTool) Description() string { return "Read fixture" }
func (*childTranscriptFixtureTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (*childTranscriptFixtureTool) Dangerous() bool                { return false }
func (*childTranscriptFixtureTool) Summary(json.RawMessage) string { return "fixture" }
func (tool *childTranscriptFixtureTool) Execute(context.Context, json.RawMessage) (string, error) {
	return tool.result, nil
}

func TestSubagentCapturesSuccessfulToolsAndReasoningEndToEnd(t *testing.T) {
	var calls atomic.Int32
	answer := strings.Repeat("final 界", 9000)
	toolResult := strings.Repeat("full successful output\n", 9000)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var delta map[string]any
		if calls.Add(1) == 1 {
			delta = map[string]any{"reasoning_content": "inspect before answering", "tool_calls": []any{map[string]any{"index": 0, "id": "successful-tool", "type": "function", "function": map[string]any{"name": "read_file", "arguments": "{}"}}}}
		} else {
			delta = map[string]any{"content": answer}
		}
		wire, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta}}})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", wire)
	}))
	defer provider.Close()
	jobs := newAgentJobs(nil)
	jobs.childTranscriptDir = filepath.Join(t.TempDir(), "children")
	defer jobs.Close()
	tool := &subagentTool{jobs: jobs, config: agent.Config{BaseURL: provider.URL, Model: "fixture", MaxTurns: 4}, tools: func() []agent.Tool { return []agent.Tool{&childTranscriptFixtureTool{result: toolResult}} }}
	raw, err := tool.Execute(t.Context(), json.RawMessage(`{"task":"inspect and answer"}`))
	if err != nil {
		t.Fatal(err)
	}
	var job agentJobSnapshot
	if err := json.Unmarshal([]byte(raw), &job); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	finished, err := jobs.Wait(ctx, job.ID)
	if err != nil || finished.State != "completed" || len(finished.Output) > agentJobOutputLimit {
		t.Fatalf("child failed: %+v %v", finished, err)
	}
	page, err := jobs.readChildTranscript(job.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var foundTool, foundReasoning, foundAnswer bool
	for _, message := range childTranscriptMessages(t, page) {
		if message.MessageID == "" {
			t.Fatal("unstable child message identity")
		}
		foundTool = foundTool || message.Role == "tool" && message.Content == toolResult
		foundReasoning = foundReasoning || message.Reasoning == "inspect before answering"
		foundAnswer = foundAnswer || message.Role == "assistant" && message.Content == answer
	}
	if !foundTool || !foundReasoning || !foundAnswer {
		t.Fatalf("incomplete child archive: tool=%v reasoning=%v answer=%v", foundTool, foundReasoning, foundAnswer)
	}
	for range agentMaxRetainedJobs {
		next, err := jobs.Start("shell", "evict summary", func(context.Context, func(string)) (string, error) { return "", nil })
		if err != nil {
			t.Fatal(err)
		}
		if _, err := jobs.Wait(ctx, next.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := jobs.Read(job.ID); err == nil {
		t.Fatal("fixture did not evict old job summary")
	}
	archived, err := jobs.readChildTranscript(job.ID, 0)
	if err != nil || !reflect.DeepEqual(archived, page) {
		t.Fatal("summary eviction discarded child transcript", err)
	}
	restarted := newAgentJobs(nil)
	restarted.childTranscriptDir = jobs.childTranscriptDir
	defer restarted.Close()
	reloaded, err := restarted.readChildTranscript(job.ID, 0)
	if err != nil || !reflect.DeepEqual(reloaded, page) {
		t.Fatal("archive was not durable independently of retained jobs", err)
	}
}

func TestChildTranscriptRestartPreservesOffsetsAndTerminalState(t *testing.T) {
	store := newAgentSessionStore(t.TempDir(), time.Hour)
	jobs := newAgentJobs(nil)
	jobs.childTranscriptDir = store.childTranscriptsPath("device", "thread")
	defer jobs.Close()
	jobs.mu.Lock()
	err := jobs.createChildTranscriptLocked("interrupted")
	jobs.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.captureChildHistory("interrupted", []agent.Message{{MessageID: "user", Role: "user", Content: "task"}}); err != nil {
		t.Fatal(err)
	}
	before, _ := jobs.readChildTranscript("interrupted", 0)
	live := agentJobSnapshot{ID: "interrupted", Kind: "subagent", State: "running", Started: time.Now(), Output: "界tail", OutputBase: 100000, OutputNext: 100007}
	if err := store.saveJobs("device", "thread", "model", []agentJobSnapshot{live}); err != nil {
		t.Fatal(err)
	}
	restored := newAgentJobs(nil)
	restored.childTranscriptDir = jobs.childTranscriptDir
	defer restored.Close()
	if !restored.restore(store.loadJobs("device", "thread")) {
		t.Fatal("running record not interrupted")
	}
	got, err := restored.Read(live.ID)
	if err != nil || got.State != "failed" || got.OutputBase != live.OutputBase || got.OutputNext != live.OutputNext || got.Output != live.Output {
		t.Fatalf("restart lost absolute offsets: %+v %v", got, err)
	}
	page, err := restored.readChildTranscript(live.ID, before.NextByte)
	if err != nil || len(page.Entries) != 1 || len(page.Messages) != 0 || page.Reset {
		t.Fatalf("restart did not append real interruption status: %+v %v", page, err)
	}
	// An unsaved running sidecar must not append the interruption twice.
	again := newAgentJobs(nil)
	again.childTranscriptDir = jobs.childTranscriptDir
	defer again.Close()
	again.restore([]agentJobSnapshot{live})
	repeated, err := again.readChildTranscript(live.ID, before.NextByte)
	if err != nil || !reflect.DeepEqual(page, repeated) {
		t.Fatal("restart duplicated completion", err)
	}
}

func TestChildTranscriptRPCSurvivesEvictionAndRejectsForeignScope(t *testing.T) {
	srv := newPrefsTestServer(t)
	entry, err := srv.agentSessionFor(agentServerDeviceID, &ClientConn{ID: agentServerDeviceID})
	if err != nil {
		t.Fatal(err)
	}
	thread := entry.threads[entry.current]
	thread.jobs.Close()
	jobs, id := newChildTranscriptFixture(t, true)
	thread.jobs = jobs
	if err := jobs.captureChildHistory(id, []agent.Message{{MessageID: "actual-user", Role: "user", Content: "actual task"}}); err != nil {
		t.Fatal(err)
	}
	foreign, foreignID := newChildTranscriptFixture(t, true)
	foreignID = "foreign-child"
	foreign.mu.Lock()
	err = foreign.createChildTranscriptLocked(foreignID)
	foreign.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(srv.HandleAgentWS))
	defer server.Close()
	recorder := &rawWSRecorder{msgs: make(chan map[string]json.RawMessage, 128)}
	socket, _, err := gws.NewClient(recorder, &gws.ClientOption{Addr: "ws" + strings.TrimPrefix(server.URL, "http") + "/agent?device=%40server", HandshakeTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.WriteClose(1000, nil)
	go socket.ReadLoop()
	waitRawMsg(t, recorder.msgs, "auth", func(m map[string]json.RawMessage) bool { return string(m["op"]) == `"auth_ok"` })
	call := func(target, session string, cursor int64, success bool) map[string]json.RawMessage {
		t.Helper()
		request, _ := json.Marshal(map[string]any{"type": "get_subagent_messages", "id": "child-request", "thread": thread.id, "subagentId": target, "sessionFile": session, "fromByte": cursor})
		sendAgentOp(t, socket, string(request))
		reply := waitRawMsg(t, recorder.msgs, "child transcript", func(m map[string]json.RawMessage) bool { return string(m["type"]) == `"response"` })
		if string(reply["success"]) != fmt.Sprint(success) || string(reply["id"]) != `"child-request"` || string(reply["thread"]) != `"`+thread.id+`"` {
			t.Fatalf("uncorrelated/wrong reply: %s", reply)
		}
		return reply
	}
	response := call("", "rdev://subagent/"+id, 0, true)
	var page ompSubagentMessages
	if err := json.Unmarshal(response["data"], &page); err != nil {
		t.Fatal(err)
	}
	messages := childTranscriptMessages(t, page)
	if len(messages) != 1 || messages[0].Role != "user" || messages[0].Content != "actual task" {
		t.Fatal("RPC fabricated assistant output")
	}
	call(id, "rdev://subagent/"+foreignID, 0, false)
	call(foreignID, "", 0, false)
	call("", "../../outside", 0, false)
	response = call(id, "", 1, true)
	if err := json.Unmarshal(response["data"], &page); err != nil || !page.Reset {
		t.Fatal("interior cursor not reset", err)
	}
}

func TestChildTranscriptDurableCompletionWinsStaleRunningSnapshot(t *testing.T) {
	jobs := newAgentJobs(nil)
	jobs.childTranscriptDir = filepath.Join(t.TempDir(), "children")
	defer jobs.Close()
	job, err := jobs.Start("subagent", "complete", func(_ context.Context, write func(string)) (string, error) {
		write("aaaa")
		return "bbbb", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	finished, err := jobs.Wait(t.Context(), job.ID)
	if err != nil || finished.State != "completed" {
		t.Fatalf("completion: %+v %v", finished, err)
	}
	before, err := jobs.readChildTranscript(job.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	restored := newAgentJobs(nil)
	restored.childTranscriptDir = jobs.childTranscriptDir
	defer restored.Close()
	restored.restore([]agentJobSnapshot{job})
	got, err := restored.Read(job.ID)
	if err != nil || got.State != "completed" || got.Result != "bbbb" || got.OutputNext != finished.OutputNext || got.Output != finished.Output {
		t.Fatalf("durable completion lost to stale snapshot: %+v %v", got, err)
	}
	after, err := restored.readChildTranscript(job.ID, 0)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("restart altered completed transcript", err)
	}
}

func TestChildTranscriptThreadQuotaAndCorruptionAreExplicit(t *testing.T) {
	jobs, id := newChildTranscriptFixture(t, true)
	// Sparse files exercise the real disk accounting without allocating a
	// quarter-gigabyte fixture. They cannot be mistaken for child messages.
	quotaFile := filepath.Join(jobs.childTranscriptDir, "quota.jsonl")
	file, err := os.Create(quotaFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(agentChildThreadLimit); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	before, _ := jobs.readChildTranscript(id, 0)
	err = jobs.captureChildHistory(id, []agent.Message{{MessageID: "quota", Role: "user", Content: "task"}})
	if err == nil || !strings.Contains(err.Error(), "thread child transcript quota") {
		t.Fatal("thread quota not enforced", err)
	}
	after, err := jobs.readChildTranscript(id, 0)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("thread quota damaged retained records", err)
	}
	if err := os.Remove(quotaFile); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(jobs.childTranscriptDir, id+".jsonl")
	broken, err := os.OpenFile(archive, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := broken.WriteString(`{"type":"message"`); err != nil {
		broken.Close()
		t.Fatal(err)
	}
	if err := broken.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.readChildTranscript(id, before.NextByte); err == nil || !strings.Contains(err.Error(), "incomplete child transcript") {
		t.Fatal("partial record silently discarded", err)
	}
	if _, err := jobs.readChildTranscript("legacy-without-archive", 0); err == nil {
		t.Fatal("missing transcript fabricated")
	}
}

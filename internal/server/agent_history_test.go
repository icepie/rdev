package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"rdev/internal/agent"
)

func newTestStore(t *testing.T) *agentSessionStore {
	t.Helper()
	return newAgentSessionStore(filepath.Join(t.TempDir(), "sessions"), 30*24*time.Hour)
}

func TestAgentHistoryRoundtripKeepsReasoningSummaryDiff(t *testing.T) {
	st := newTestStore(t)
	msgs := []agent.Message{
		{Role: "user", Content: "change the file"},
		{Role: "assistant", Content: "done", Reasoning: "thinking about it", ToolCalls: []agent.ToolCall{
			{ID: "c1", Name: "edit", Arguments: `{"path":"/a"}`, Summary: "/a", Diff: "--- a/a\n+++ b/a\n"},
		}},
		{Role: "tool", ToolCallID: "c1", Content: "edited /a"},
	}
	st.save("dev1", "t_1", "p/m", msgs)
	got, model, _, _ := st.load("dev1", "t_1")
	if model != "p/m" {
		t.Fatalf("model = %q", model)
	}
	if len(got) != 3 {
		t.Fatalf("messages = %d", len(got))
	}
	if got[1].Reasoning != "thinking about it" {
		t.Fatalf("reasoning = %q", got[1].Reasoning)
	}
	if got[1].ToolCalls[0].Summary != "/a" || got[1].ToolCalls[0].Diff != "--- a/a\n+++ b/a\n" {
		t.Fatalf("tool call = %+v", got[1].ToolCalls[0])
	}

	entries := renderAgentHistory(got)
	if len(entries) != 2 {
		t.Fatalf("entries = %d", len(entries))
	}
	if entries[1].Reasoning != "thinking about it" {
		t.Fatalf("entry reasoning = %q", entries[1].Reasoning)
	}
	tool := entries[1].Tools[0]
	if tool.Summary != "/a" || tool.Diff == "" || tool.Result != "edited /a" {
		t.Fatalf("rendered tool = %+v", tool)
	}
}

func TestAgentHistoryAppendThenSave(t *testing.T) {
	st := newTestStore(t)
	st.save("dev1", "t_1", "m", []agent.Message{{Role: "user", Content: "one"}})
	st.save("dev1", "t_1", "m", []agent.Message{
		{Role: "user", Content: "one"},
		{Role: "assistant", Content: "two"},
	})
	got, _, _, _ := st.load("dev1", "t_1")
	if len(got) != 2 || got[1].Content != "two" {
		t.Fatalf("messages = %+v", got)
	}
}

func TestAgentHistoryMigratesLegacyFlatFile(t *testing.T) {
	st := newTestStore(t)
	legacy := filepath.Join(st.dir, "dev1.jsonl")
	line := `{"t":"meta","v":1,"device":"dev1","model":"m","updated":"2024-01-01T00:00:00Z"}` + "\n" +
		`{"t":"msg","role":"user","content":"legacy hello"}` + "\n"
	if err := os.WriteFile(legacy, []byte(line), 0600); err != nil {
		t.Fatal(err)
	}
	st.migrate("dev1")
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatal("legacy file not moved")
	}
	msgs, model, _, _ := st.load("dev1", "t_1")
	if model != "m" || len(msgs) != 1 || msgs[0].Content != "legacy hello" {
		t.Fatalf("migrated = %q, %+v", model, msgs)
	}
	metas := st.listThreads("dev1")
	if len(metas) != 1 || metas[0].ID != "t_1" || metas[0].Title != "legacy hello" {
		t.Fatalf("metas = %+v", metas)
	}
}

func TestAgentHistoryListThreadsOrderingAndTitles(t *testing.T) {
	st := newTestStore(t)
	st.save("dev1", "t_1", "m", []agent.Message{{Role: "user", Content: "first thread"}})
	st.save("dev1", "t_2", "m", []agent.Message{{Role: "user", Content: strings.Repeat("长", 40)}})

	metas := st.listThreads("dev1")
	if len(metas) != 2 {
		t.Fatalf("metas = %+v", metas)
	}
	if metas[0].ID != "t_2" || metas[1].ID != "t_1" {
		t.Fatalf("order = %s, %s (want t_2, t_1)", metas[0].ID, metas[1].ID)
	}
	if got := []rune(metas[0].Title); len(got) != 30 {
		t.Fatalf("title runes = %d, want 30", len(got))
	}
	if metas[1].Title != "first thread" {
		t.Fatalf("title = %q", metas[1].Title)
	}

	// A thread without user messages gets the fallback title.
	st.save("dev1", "t_3", "m", []agent.Message{{Role: "assistant", Content: "hi"}})
	for _, m := range st.listThreads("dev1") {
		if m.ID == "t_3" && m.Title != "新对话" {
			t.Fatalf("fallback title = %q", m.Title)
		}
	}
}

func TestAgentHistoryClearRemovesThread(t *testing.T) {
	st := newTestStore(t)
	st.save("dev1", "t_1", "m", []agent.Message{{Role: "user", Content: "x"}})
	st.clear("dev1", "t_1")
	if metas := st.listThreads("dev1"); len(metas) != 0 {
		t.Fatalf("metas after clear = %+v", metas)
	}
}

func TestAgentHistoryCleanupRemovesStaleThreads(t *testing.T) {
	st := newAgentSessionStore(filepath.Join(t.TempDir(), "sessions"), time.Millisecond)
	st.save("dev1", "t_1", "m", []agent.Message{{Role: "user", Content: "old"}})
	path := st.path("dev1", "t_1")
	stale := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, stale, stale); err != nil {
		t.Fatal(err)
	}
	st.cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("stale thread file not removed")
	}
	// The emptied device directory is dropped too.
	if _, err := os.Stat(filepath.Join(st.dir, "dev1")); !os.IsNotExist(err) {
		t.Fatal("empty device dir not removed")
	}
}

// TestAgentHistoryCleanupRemovesStaleTmp: rewrite temp files orphaned by a
// crash are collected after 1h, in both the per-device dirs and the legacy
// flat layout; fresh ones survive.
func TestAgentHistoryCleanupRemovesStaleTmp(t *testing.T) {
	st := newAgentSessionStore(filepath.Join(t.TempDir(), "sessions"), time.Hour)
	// Keep one live thread so the device dir survives cleanup.
	st.save("dev1", "t_1", "m", []agent.Message{{Role: "user", Content: "x"}})
	old := time.Now().Add(-2 * time.Hour)
	writeTmp := func(path string, mtime time.Time) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("partial"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	staleDevTmp := st.path("dev1", "t_2") + ".tmp"
	staleFlatTmp := filepath.Join(st.dir, "legacy.jsonl.tmp")
	freshTmp := st.path("dev1", "t_3") + ".tmp"
	writeTmp(staleDevTmp, old)
	writeTmp(staleFlatTmp, old)
	writeTmp(freshTmp, time.Now())

	st.cleanup()
	for _, p := range []string{staleDevTmp, staleFlatTmp} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("stale tmp not removed: %s", p)
		}
	}
	if _, err := os.Stat(freshTmp); err != nil {
		t.Fatalf("fresh tmp removed: %v", err)
	}
}

// TestAgentHistorySaveTrimDropsOrphanTool: when the keep-window cut lands
// on a tool result, the orphaned result is dropped so the rewritten file
// never starts with role "tool" (its assistant call is gone).
func TestAgentHistorySaveTrimDropsOrphanTool(t *testing.T) {
	st := newTestStore(t)
	history := make([]agent.Message, 0, agentHistoryKeepMessages+2)
	history = append(history,
		agent.Message{Role: "user", Content: "old"},
		agent.Message{Role: "user", Content: "older"},
		agent.Message{Role: "assistant", ToolCalls: []agent.ToolCall{{ID: "c1", Name: "run", Arguments: "{}"}}},
		// The keep-window cut lands exactly on this tool result (index 3 =
		// len-keepMessages); its assistant call (index 2) is cut away.
		agent.Message{Role: "tool", ToolCallID: "c1", Content: "out"},
	)
	for len(history) < agentHistoryKeepMessages+3 {
		history = append(history, agent.Message{Role: "user", Content: "filler"})
	}
	st.save("dev1", "t_1", "m", history)

	msgs, _, _, _ := st.load("dev1", "t_1")
	if len(msgs) != agentHistoryKeepMessages-1 {
		t.Fatalf("kept = %d, want %d (cut + orphan tool drop)", len(msgs), agentHistoryKeepMessages-1)
	}
	if msgs[0].Role == "tool" {
		t.Fatal("first kept message is an orphaned tool result")
	}
}

// TestAgentHistoryListThreadsPrefersMtime: append-only saves do not
// rewrite the meta line, so ordering must fall back to the file mtime or
// recently-active threads sink below untouched ones.
func TestAgentHistoryListThreadsPrefersMtime(t *testing.T) {
	st := newTestStore(t)
	st.save("dev1", "t_1", "m", []agent.Message{{Role: "user", Content: "one"}})
	st.save("dev1", "t_2", "m", []agent.Message{{Role: "user", Content: "two"}})
	// Append to t_1 without touching its meta line: it becomes the most
	// recently active thread by mtime, not by meta.
	st.save("dev1", "t_1", "m", []agent.Message{
		{Role: "user", Content: "one"},
		{Role: "assistant", Content: "reply"},
	})
	// Filesystem mtimes and the wall clock may straddle a whole-second boundary.
	// Set the ordering explicitly instead of depending on a sleep or clock skew.
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(st.path("dev1", "t_1"), later, later); err != nil {
		t.Fatal(err)
	}
	metas := st.listThreads("dev1")
	if len(metas) != 2 || metas[0].ID != "t_1" {
		t.Fatalf("order = %+v, want t_1 first (mtime beats stale meta)", metas)
	}
}

func TestAgentHistoryReplacementPersistsEqualLengthSummary(t *testing.T) {
	st := newTestStore(t)
	original := []agent.Message{{Role: "user", Content: "old request"}, {Role: "assistant", Content: "old response"}}
	replacement := []agent.Message{{Role: "user", Content: "summary of earlier context"}, {Role: "assistant", Content: "continue from summary"}}
	st.save("device", "thread", "model", original)
	st.replace("device", "thread", "model", replacement)
	// Reload through a new store to exercise disk state, not the writer's cache.
	reopened := newAgentSessionStore(st.dir, 30*24*time.Hour)
	got, _, _, _ := reopened.load("device", "thread")
	if !reflect.DeepEqual(got, replacement) {
		t.Fatalf("reload lost summary: %+v", got)
	}
	next := append(replacement, agent.Message{Role: "user", Content: "next request"})
	st.save("device", "thread", "model", next)
	got, _, _, _ = reopened.load("device", "thread")
	if !reflect.DeepEqual(got, next) {
		t.Fatalf("append after compaction corrupted history: %+v", got)
	}
}

func TestHistoryCleanupDoesNotRaceNewDeviceWrites(t *testing.T) {
	st := newTestStore(t)
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				if worker == 0 {
					st.cleanup()
					continue
				}
				device := fmt.Sprintf("device-%d-%d", worker, i)
				history := []agent.Message{{Role: "user", Content: device}}
				st.save(device, "thread", "model", history)
				got, _, _, _ := st.load(device, "thread")
				if !reflect.DeepEqual(got, history) {
					t.Errorf("history lost during cleanup: %s", device)
				}
			}
		}(worker)
	}
	wg.Wait()
}

func TestAgentOutcomeSurvivesReopenRewriteAndReset(t *testing.T) {
	st := newTestStore(t)
	for _, outcome := range []string{"error", "stopped", "done", "running"} {
		st.saveOutcome("device", "thread", "fixture", outcome, "2026-11-08T01:00:00.123456789Z")
		reopened := newAgentSessionStore(st.dir, time.Hour)
		metas := reopened.listThreads("device")
		if len(metas) != 1 || metas[0].Outcome != outcome || metas[0].OutcomeAt != "2026-11-08T01:00:00.123456789Z" {
			t.Fatalf("outcome lost on reopen: %+v", metas)
		}
		reopened.replace("device", "thread", "fixture", []agent.Message{{Role: "user", Content: "retained"}})
		again := newAgentSessionStore(st.dir, time.Hour).listThreads("device")
		if len(again) != 1 || again[0].Outcome != outcome || again[0].OutcomeAt != metas[0].OutcomeAt {
			t.Fatalf("outcome lost on rewrite: %+v", again)
		}
	}
	if restoredAgentOutcome("running") != "stopped" {
		t.Fatal("interrupted execution restored as running")
	}
	st.clear("device", "thread")
	if metas := newAgentSessionStore(st.dir, time.Hour).listThreads("device"); len(metas) != 0 {
		t.Fatalf("reset retained outcome: %+v", metas)
	}
	st.save("device", "thread", "fixture", []agent.Message{{Role: "user", Content: "new"}})
	if metas := newAgentSessionStore(st.dir, time.Hour).listThreads("device"); len(metas) != 1 || metas[0].Outcome != "" {
		t.Fatalf("reset inherited old outcome: %+v", metas)
	}

}

func TestAgentHistoryKeepsResponseScopedResultsAndFailures(t *testing.T) {
	st := newTestStore(t)
	messages := []agent.Message{
		{Role: "user", Content: "try twice"},
		{Role: "assistant", ToolCalls: []agent.ToolCall{{ID: "same", Name: "read_file", IsError: true}}},
		{Role: "tool", ToolCallID: "same", Content: "permission denied"},
		{Role: "assistant", ToolCalls: []agent.ToolCall{{ID: "same", Name: "read_file"}}},
		{Role: "tool", ToolCallID: "same", Content: "file contents"},
		{Role: "assistant", Reasoning: "Only thinking before cancellation"},
	}
	st.save("device", "thread", "model", messages)
	loaded, _, _, _ := st.load("device", "thread")
	for _, source := range [][]agent.Message{messages, loaded} {
		entries := renderAgentHistory(source)
		if len(entries) != 4 {
			t.Fatalf("entries = %+v", entries)
		}
		first, second := entries[1].Tools[0], entries[2].Tools[0]
		if first.Result != "permission denied" || !first.IsError || second.Result != "file contents" || second.IsError {
			t.Fatalf("results crossed responses or lost failure: %+v / %+v", first, second)
		}
		if entries[3].Reasoning != messages[5].Reasoning {
			t.Fatal("reasoning-only response lost")
		}
	}
}

func TestAgentHistoryPersistsOpaqueResponsesWithoutUIExposure(t *testing.T) {
	store := newTestStore(t)
	replay := &agent.ResponseReplay{Scope: `["provider","url","model"]`, Items: []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"opaque"}`), json.RawMessage(`{"type":"message","phase":"final_answer","content":[{"type":"output_text","text":"answer"}]}`)}}
	messages := []agent.Message{{Role: "user", Content: "question"}, {Role: "assistant", Content: "answer", Responses: replay}}
	store.save("device", "thread", "provider/model", messages)
	restored, _, _, _ := store.load("device", "thread")
	if len(restored) != 2 || !reflect.DeepEqual(restored[1].Responses, replay) {
		t.Fatalf("lost Responses continuation: %+v", restored)
	}
	data, err := json.Marshal(renderAgentHistory(restored))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "opaque") || strings.Contains(string(data), "encrypted_content") {
		t.Fatal("opaque model state leaked into UI")
	}
}

func TestAgentHistoryPersistsAnthropicReplayWithoutUIExposure(t *testing.T) {
	store := newTestStore(t)
	replay := &agent.ProviderReplay{Scope: "fixture-scope", Items: []json.RawMessage{json.RawMessage(`{"type":"thinking","thinking":"plan","signature":"private-signature"}`), json.RawMessage(`{"type":"redacted_thinking","data":"private-redacted"}`), json.RawMessage(`{"type":"text","text":"answer"}`)}}
	messages := []agent.Message{{Role: "user", Content: "question"}, {Role: "assistant", Content: "answer", Reasoning: "plan", Anthropic: replay}}
	store.save("device", "thread", "provider/model", messages)
	restored, _, _, _ := newAgentSessionStore(store.dir, time.Hour).load("device", "thread")
	if len(restored) != 2 || !reflect.DeepEqual(restored[1].Anthropic, replay) {
		t.Fatalf("lost Anthropic continuation: %+v", restored)
	}
	raw, err := json.Marshal(renderAgentHistory(restored))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private-") || strings.Contains(string(raw), "signature") || strings.Contains(string(raw), "redacted_thinking") {
		t.Fatal("opaque state leaked into UI")
	}
}

func TestAgentHistoryRoundTripOrdinaryAndLargeMessages(t *testing.T) {
	for _, size := range []int{64, 3 << 20} {
		t.Run(fmt.Sprintf("%d-bytes", size), func(t *testing.T) {
			st := newTestStore(t)
			user := strings.Repeat("u", size)
			assistant := strings.Repeat("a", size)
			st.save("large", "t_1", "model", []agent.Message{{Role: "user", Content: user}, {Role: "assistant", Content: assistant}})
			got, _, _, _ := st.load("large", "t_1")
			if len(got) != 2 || got[0].Content != user || got[1].Content != assistant {
				t.Fatalf("history was truncated: messages=%d", len(got))
			}
		})
	}
}

func TestAgentHistoryFaultIsReportedAndRecoveryPreservesOriginal(t *testing.T) {
	st := newTestStore(t)
	path := st.path("damaged", "t_1")
	original := []byte("{\"t\":\"meta\",\"v\":1,\"model\":\"m\"}\n{\"t\":\"msg\",\"role\":\"user\",\"content\":\"kept\"}\n{broken\n")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	got, _, _, _ := st.load("damaged", "t_1")
	if len(got) != 1 || got[0].Content != "kept" {
		t.Fatalf("valid prefix unavailable: %+v", got)
	}
	if health := st.historyHealth(); health.Faulted != 1 || len(health.Faults) != 1 {
		t.Fatalf("missing corruption diagnostic: %+v", health)
	}
	st.save("damaged", "t_1", "m", append(got, agent.Message{Role: "assistant", Content: "must not append"}))
	if current, _ := os.ReadFile(path); string(current) != string(original) {
		t.Fatal("save overwrote damaged original before recovery")
	}
	count, backup, err := st.recover("damaged", "t_1")
	if err != nil || count != 1 {
		t.Fatalf("recover count=%d err=%v", count, err)
	}
	backupBytes, err := os.ReadFile(backup)
	if err != nil || string(backupBytes) != string(original) {
		t.Fatalf("recovery backup did not preserve original: %v", err)
	}
	recovered, _, _, _ := st.load("damaged", "t_1")
	if len(recovered) != 1 || recovered[0].Content != "kept" || st.historyHealth().Faulted != 0 {
		t.Fatalf("unsafe recovered history: %+v health=%+v", recovered, st.historyHealth())
	}
}

func TestAgentHistoryAllWritersPreserveUnloadedCorruption(t *testing.T) {
	writers := map[string]func(*agentSessionStore){
		"save": func(st *agentSessionStore) {
			st.save("d", "t", "m", []agent.Message{{Role: "user", Content: "replacement"}})
		},
		"replace":  func(st *agentSessionStore) { st.replace("d", "t", "m", nil) },
		"title":    func(st *agentSessionStore) { st.setTitle("d", "t", "new", nil) },
		"cwd":      func(st *agentSessionStore) { st.setCwd("d", "t", "/new", nil) },
		"outcome":  func(st *agentSessionStore) { st.saveOutcome("d", "t", "m", "done", "now") },
		"autonomy": func(st *agentSessionStore) { st.saveAutonomy("d", "t", "m", nil, nil) },
		"clear":    func(st *agentSessionStore) { st.clear("d", "t") },
		"cleanup":  func(st *agentSessionStore) { st.cleanup() },
	}
	for name, mutate := range writers {
		t.Run(name, func(t *testing.T) {
			st := newTestStore(t)
			path := st.path("d", "t")
			original := []byte("{\"t\":\"meta\",\"v\":1}\n{broken\n")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			old := time.Now().Add(-365 * 24 * time.Hour)
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}
			mutate(st)
			got, err := os.ReadFile(path)
			if err != nil || string(got) != string(original) {
				t.Fatalf("damaged original changed: %v", err)
			}
			if st.historyFault("d", "t") == "" {
				t.Fatal("missing fault")
			}
		})
	}
}

func TestAgentHistoryCheckpointCompactionPreservesTranscriptAndCursor(t *testing.T) {
	for _, kind := range []string{"outcome", "autonomy"} {
		t.Run(kind, func(t *testing.T) {
			st := newTestStore(t)
			history := []agent.Message{{Role: "user", Content: "retained"}}
			st.save("d", "t", "m", history)
			st.setTitle("d", "t", "title", history)
			path := st.path("d", "t")
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			padding := strings.Repeat("{\"t\":\"outcome\",\"outcome\":\"done\"}\n", agentHistoryMaxBytes/16)
			if _, err := f.WriteString(padding); err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			if kind == "outcome" {
				st.saveOutcome("d", "t", "m", "stopped", "stamp")
			} else {
				st.saveAutonomy("d", "t", "m", &agentGoal{Status: "paused"}, nil)
			}
			info, err := os.Stat(path)
			if err != nil || info.Size() >= agentHistoryMaxBytes {
				t.Fatalf("checkpoint did not compact: %v", err)
			}
			if st.saved["d/t"] != len(history) {
				t.Fatal("checkpoint changed memory cursor")
			}
			history = append(history, agent.Message{Role: "assistant", Content: "next"})
			st.save("d", "t", "m", history)
			reopened := newAgentSessionStore(st.dir, time.Hour)
			got, model, _, title := reopened.load("d", "t")
			if !reflect.DeepEqual(got, history) || model != "m" || title != "title" {
				t.Fatalf("checkpoint lost transcript or metadata: %+v %q %q", got, model, title)
			}
			if kind == "outcome" && reopened.extras["d/t"].Outcome != "stopped" {
				t.Fatal("outcome lost")
			}
			if kind == "autonomy" && reopened.extras["d/t"].Goal == nil {
				t.Fatal("goal lost")
			}
		})
	}
}

func TestAgentHistoryLimitsAndExactRecoveryBackup(t *testing.T) {
	for _, kind := range []string{"line", "total"} {
		t.Run(kind, func(t *testing.T) {
			st := newTestStore(t)
			path := st.path("d", "t")
			original := "{\"t\":\"meta\",\"v\":1}\n{\"t\":\"msg\",\"role\":\"user\",\"content\":\"kept\"}\n"
			if kind == "line" {
				original += string(marshalAgentHistoryMsg(agent.Message{Role: "assistant", Content: strings.Repeat("x", agentHistoryMaxLineBytes)})) + "\n"
			} else {
				line := string(marshalAgentHistoryMsg(agent.Message{Role: "assistant", Content: strings.Repeat("x", 1<<20)})) + "\n"
				original += strings.Repeat(line, 17)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			st.load("d", "t")
			if st.historyHealth().Faulted != 1 {
				t.Fatal("oversize history accepted")
			}
			count, backup, err := st.recover("d", "t")
			if err != nil || count < 1 {
				t.Fatalf("recover: %d %v", count, err)
			}
			got, err := os.ReadFile(backup)
			if err != nil || string(got) != original {
				t.Fatalf("backup was truncated: %v", err)
			}
			if st.historyHealth().Faulted != 0 {
				t.Fatal("successful recovery left fault")
			}
		})
	}
}

func TestAgentHistoryFailedRecoveryKeepsFaultAndOriginal(t *testing.T) {
	st := newTestStore(t)
	st.save("d", "t", "m", []agent.Message{{Role: "user", Content: "kept"}})
	path := st.path("d", "t")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	st.recordFaultLocked("d", "t", "failed checkpoint")
	st.mu.Unlock()
	if err := os.Mkdir(path+".tmp", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path+".tmp", "block"), []byte("block"), 0600); err != nil {
		t.Fatal(err)
	}
	_, backup, err := st.recover("d", "t")
	if err == nil || backup == "" {
		t.Fatal("expected failed rewrite after backup")
	}
	st.load("d", "t")
	if st.historyFault("d", "t") == "" {
		t.Fatal("reload cleared unrecovered write fault")
	}
	for _, p := range []string{path, backup} {
		got, err := os.ReadFile(p)
		if err != nil || string(got) != string(original) {
			t.Fatalf("original/backup changed: %v", err)
		}
	}
}

func TestAgentHistoryClearAndCleanupInvalidateAppendCursor(t *testing.T) {
	for _, remove := range []string{"clear", "cleanup"} {
		t.Run(remove, func(t *testing.T) {
			st := newTestStore(t)
			history := []agent.Message{{Role: "user", Content: "old"}, {Role: "assistant", Content: "old reply"}}
			st.save("d", "t", "m", history)
			if remove == "clear" {
				st.clear("d", "t")
			} else {
				old := time.Now().Add(-365 * 24 * time.Hour)
				if err := os.Chtimes(st.path("d", "t"), old, old); err != nil {
					t.Fatal(err)
				}
				st.cleanup()
			}
			if _, ok := st.saved["d/t"]; ok {
				t.Fatal("stale append cursor")
			}
			st.saveOutcome("d", "t", "m", "running", "")
			st.save("d", "t", "m", history)
			got, _, _, _ := st.load("d", "t")
			if !reflect.DeepEqual(got, history) {
				t.Fatalf("stale cursor lost transcript: %+v", got)
			}
		})
	}
}

func TestAgentHistoryRecoveryHandlerReplacesHistoryWithoutDeadlock(t *testing.T) {
	srv := newPrefsTestServer(t)
	entry, err := srv.agentSessionFor(agentServerDeviceID, &ClientConn{ID: agentServerDeviceID})
	if err != nil {
		t.Fatal(err)
	}
	th := entry.threads[entry.current]
	store := srv.agentStore()
	store.save(entry.deviceID, th.id, th.model, []agent.Message{{Role: "user", Content: "recovered"}})
	path := store.path(entry.deviceID, th.id)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{broken\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	store.load(entry.deviceID, th.id)
	th.session.SetHistory([]agent.Message{{Role: "user", Content: "stale"}})
	var replies []agentMsg
	ac := &agentConn{entry: entry, current: th.id, deviceID: entry.deviceID, reply: func(msg agentMsg) { replies = append(replies, msg) }}
	done := make(chan struct{})
	go func() {
		(&agentWSHandler{srv: srv}).handleRecoverHistory(ac, th.id)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("history recovery deadlocked")
	}
	if len(replies) != 2 || replies[0].Op != "history_recovered" || replies[0].Recovered != 1 || replies[1].Op != "thread_peeked" || replies[1].HistoryError != "" {
		t.Fatalf("recovery replies: %+v", replies)
	}
	got := th.session.History()
	if len(got) != 1 || got[0].Content != "recovered" {
		t.Fatalf("stale session history: %+v", got)
	}
}

func TestAgentHistoryExactLineLimitRoundTrip(t *testing.T) {
	st := newTestStore(t)
	message := agent.Message{MessageID: "boundary", Role: "user"}
	overhead := len(marshalAgentHistoryMsg(agent.Message{MessageID: message.MessageID, Role: "user", Content: "x"})) - 1
	message.Content = strings.Repeat("x", agentHistoryMaxLineBytes-overhead)
	if len(marshalAgentHistoryMsg(message)) != agentHistoryMaxLineBytes {
		t.Fatal("bad boundary fixture")
	}
	st.save("d", "t", "m", []agent.Message{message})
	got, _, _, _ := st.load("d", "t")
	if len(got) != 1 || got[0].Content != message.Content || st.historyHealth().Faulted != 0 {
		t.Fatal("writer accepted a line that loader cannot read")
	}
}

func TestAgentHistoryRejectedRewritePreservesOriginal(t *testing.T) {
	for _, kind := range []string{"line", "total", "metadata"} {
		t.Run(kind, func(t *testing.T) {
			st := newTestStore(t)
			st.save("d", "t", "m", []agent.Message{{Role: "user", Content: "original"}})
			original, err := os.ReadFile(st.path("d", "t"))
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "line":
				st.replace("d", "t", "m", []agent.Message{{Role: "user", Content: strings.Repeat("x", agentHistoryMaxLineBytes)}})
			case "total":
				message := agent.Message{Role: "user", Content: strings.Repeat("x", 6<<20)}
				st.replace("d", "t", "m", []agent.Message{message, message, message})
			case "metadata":
				st.setTitle("d", "t", strings.Repeat("x", agentHistoryMaxLineBytes), nil)
			}
			got, err := os.ReadFile(st.path("d", "t"))
			if err != nil || string(got) != string(original) {
				t.Fatalf("failed rewrite changed original: %v", err)
			}
			if st.historyHealth().Faulted != 1 {
				t.Fatal("write failure was hidden")
			}
		})
	}
}

func TestAgentHistoryHandlersRejectDestructiveActionsBeforeRecovery(t *testing.T) {
	for _, action := range []string{"reset", "delete", "active-recovery"} {
		t.Run(action, func(t *testing.T) {
			srv := newPrefsTestServer(t)
			entry, err := srv.agentSessionFor(agentServerDeviceID, &ClientConn{ID: agentServerDeviceID})
			if err != nil {
				t.Fatal(err)
			}
			th := entry.threads[entry.current]
			store := srv.agentStore()
			path := store.path(entry.deviceID, th.id)
			original := []byte("{\"t\":\"meta\",\"v\":1}\n{broken\n")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			var reply agentMsg
			ac := &agentConn{entry: entry, current: th.id, deviceID: entry.deviceID, reply: func(msg agentMsg) { reply = msg }}
			handler := &agentWSHandler{srv: srv}
			switch action {
			case "reset":
				handler.handleReset(ac, th.id)
			case "delete":
				handler.handleDeleteThread(ac, th.id)
			case "active-recovery":
				th.running = true
				handler.handleRecoverHistory(ac, th.id)
			}
			if reply.Op != "error" || entry.threads[th.id] != th {
				t.Fatalf("destructive action accepted: %+v", reply)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != string(original) {
				t.Fatalf("original changed: %v", err)
			}
		})
	}
}

func TestAgentHistoryAppendsAfterUnterminatedFinalRecord(t *testing.T) {
	for _, kind := range []string{"message", "outcome", "autonomy"} {
		t.Run(kind, func(t *testing.T) {
			st := newTestStore(t)
			history := []agent.Message{{Role: "user", Content: "retained"}}
			st.save("d", "t", "m", history)
			path := st.path("d", "t")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data[:len(data)-1], 0600); err != nil {
				t.Fatal(err)
			}
			st.load("d", "t")
			switch kind {
			case "message":
				history = append(history, agent.Message{Role: "assistant", Content: "next"})
				st.save("d", "t", "m", history)
			case "outcome":
				st.saveOutcome("d", "t", "m", "done", "stamp")
			case "autonomy":
				st.saveAutonomy("d", "t", "m", &agentGoal{Status: "paused"}, nil)
			}
			got, _, _, _ := st.load("d", "t")
			if !reflect.DeepEqual(got, history) || st.historyHealth().Faulted != 0 {
				t.Fatal("append joined JSON records")
			}
		})
	}
}

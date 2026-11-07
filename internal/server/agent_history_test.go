package server

import (
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
	time.Sleep(1100 * time.Millisecond) // meta timestamps are second-granular
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
	time.Sleep(1100 * time.Millisecond) // meta timestamps are second-granular
	st.save("dev1", "t_2", "m", []agent.Message{{Role: "user", Content: "two"}})
	// Append to t_1 without touching its meta line: it becomes the most
	// recently active thread by mtime, not by meta.
	st.save("dev1", "t_1", "m", []agent.Message{
		{Role: "user", Content: "one"},
		{Role: "assistant", Content: "reply"},
	})
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

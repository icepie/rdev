package server

import (
	"os"
	"path/filepath"
	"strings"
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

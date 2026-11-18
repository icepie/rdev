package server

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"rdev/internal/agent"
)

func transcriptMessages(n int) []agent.Message {
	messages := make([]agent.Message, n)
	for i := range messages {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		messages[i] = agent.Message{MessageID: fmt.Sprintf("m_%04d", i), Role: role, Content: fmt.Sprintf("record %d", i)}
	}
	return messages
}
func TestTranscriptSurvivesContextReductionAndPagesAfterRestart(t *testing.T) {
	st := newTestStore(t)
	messages := transcriptMessages(240)
	st.save("device", "thread", "model", messages)
	st.replace("device", "thread", "model", messages[230:])
	restarted := newAgentSessionStore(st.dir, st.ttl)
	page, err := restarted.transcriptPage("device", "thread", "", "", 50)
	if err != nil || len(page.History) != 50 || !page.HasMore || page.Before != "m_0190" {
		t.Fatalf("latest page=%+v err=%v", page, err)
	}
	older, err := restarted.transcriptPage("device", "thread", page.Before, "", 100)
	if err != nil || len(older.History) != 100 || older.History[99].MessageID != "m_0189" {
		t.Fatalf("older=%+v err=%v", older, err)
	}
	around, err := restarted.transcriptPage("device", "thread", "", "m_0002", 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range around.History {
		found = found || entry.MessageID == "m_0002"
	}
	if !found {
		t.Fatal("around omitted target")
	}
}
func TestTranscriptMigrationIdempotentWithoutContextRewrite(t *testing.T) {
	st := newTestStore(t)
	messages := []agent.Message{{Role: "user", Content: "original"}, {Role: "assistant", Content: "answer"}}
	if err := st.rewriteLocked("device", "legacy", "model", messages); err != nil {
		t.Fatal(err)
	}
	first, _, _, _ := st.load("device", "legacy")
	info, err := os.Stat(st.transcriptPath("device", "legacy"))
	if err != nil {
		t.Fatal(err)
	}
	restarted := newAgentSessionStore(st.dir, st.ttl)
	second, _, _, _ := restarted.load("device", "legacy")
	after, _ := os.Stat(st.transcriptPath("device", "legacy"))
	if first[0].MessageID == "" || first[0].MessageID != second[0].MessageID || info.Size() != after.Size() {
		t.Fatal("migration changed identity or duplicated records")
	}
}
func TestTranscriptKeepsOriginalToolOutputAndSafeForkBoundary(t *testing.T) {
	st := newTestStore(t)
	messages := []agent.Message{
		{MessageID: "user", Role: "user", Content: "run"},
		{MessageID: "assistant", Role: "assistant", ToolCalls: []agent.ToolCall{{ID: "call", Name: "shell", Arguments: `{"command":"echo result"}`}}},
		{MessageID: "result", Role: "tool", ToolCallID: "call", Content: "short", ArchiveContent: "complete original output"},
		{MessageID: "next", Role: "user", Content: "change"},
	}
	if err := st.captureTranscript("device", "source", messages); err != nil {
		t.Fatal(err)
	}
	messages[2].ArchiveContent = ""
	if err := st.captureTranscript("device", "source", messages); err != nil {
		t.Fatal(err)
	}
	page, err := st.transcriptPage("device", "source", "", "", 50)
	if err != nil || page.History[1].Tools[0].Result != "complete original output" {
		t.Fatalf("archive was shortened: %+v %v", page, err)
	}
	fork, err := st.forkTranscript("device", "source", "fork", "assistant", false)
	if err != nil || len(fork) != 3 || fork[2].Content != "complete original output" {
		t.Fatalf("tool boundary fork=%+v err=%v", fork, err)
	}
	edited, err := st.forkTranscript("device", "source", "edited", "next", true)
	if err != nil || len(edited) != 3 {
		t.Fatalf("edit replayed selected input: %+v %v", edited, err)
	}
	if _, err := st.forkTranscript("device", "source", "invalid", "assistant", true); err == nil {
		t.Fatal("assistant accepted editText")
	}
	again, _ := st.transcriptPage("device", "source", "", "", 50)
	if len(again.History) != 3 {
		t.Fatal("fork changed source")
	}
	if err := validateTranscriptBoundary(messages[1:2]); err == nil {
		t.Fatal("incomplete tool boundary accepted")
	}
}
func TestTranscriptQuotaRejectsWithoutDroppingHistory(t *testing.T) {
	st := newTestStore(t)
	messages := transcriptMessages(2)
	if err := st.captureTranscript("device", "thread", messages); err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	archive, _ := st.transcriptLocked("device", "thread")
	archive.size = agentTranscriptMaxBytes
	st.mu.Unlock()
	if err := st.captureTranscript("device", "thread", transcriptMessages(3)); err == nil {
		t.Fatal("quota not enforced")
	}
	page, err := st.transcriptPage("device", "thread", "", "", 50)
	if err != nil || len(page.History) != 2 {
		t.Fatalf("quota evicted archive: %+v %v", page, err)
	}
}
func TestTranscriptLiveReplayDoesNotDuplicateArchivedTurn(t *testing.T) {
	st := newTestStore(t)
	messages := transcriptMessages(6)
	if err := st.captureTranscript("device", "thread", messages); err != nil {
		t.Fatal(err)
	}
	srv := &Server{DataDir: t.TempDir(), AgentSessionTTL: st.ttl, agentStoreInst: st}
	srv.agentStoreOnce.Do(func() {})
	th := &agentThread{id: "thread", session: agent.New(agent.Config{}), running: true, turnMessageID: messages[4].MessageID}
	entry := &agentSessionEntry{deviceID: "device"}
	page, err := srv.agentTranscriptPageLocked(entry, th, "", "", 50)
	if err != nil || len(page.History) != 4 {
		t.Fatalf("live duplicate: %+v %v", page, err)
	}
	around, err := srv.agentTranscriptPageLocked(entry, th, "", messages[3].MessageID, 50)
	if err != nil || len(around.History) != 4 {
		t.Fatalf("around crossed live turn: %+v %v", around, err)
	}
}
func TestHistorySearchBodyAndAuthorization(t *testing.T) {
	entry := agentHistoryEntry{MessageID: "id", Role: "assistant", Tools: []agentHistoryTool{{Result: "prefix hidden NEEDLE result"}}}
	if snippet, ok := historySnippet(entry, "needle"); !ok || !strings.Contains(snippet, "NEEDLE") {
		t.Fatal("tool body not searchable")
	}
	h := &agentWSHandler{srv: &Server{}}
	ac := &agentConn{authed: true, deviceID: "a", entry: &agentSessionEntry{deviceID: "b", threads: map[string]*agentThread{"thread": {id: "thread"}}}}
	if _, _, err := h.historyThread(ac, agentMsg{Thread: "thread"}); err == nil {
		t.Fatal("cross-device session authorized")
	}
	ac.entry.deviceID = "a"
	if _, _, err := h.historyThread(ac, agentMsg{Thread: "unknown"}); err == nil {
		t.Fatal("unknown thread authorized")
	}
}

func TestForkHandlerReturnsDraftWithoutCopyingGrantsOrJobs(t *testing.T) {
	srv := newPrefsTestServer(t)
	entry, err := srv.agentSessionFor(agentServerDeviceID, &ClientConn{ID: agentServerDeviceID})
	if err != nil {
		t.Fatal(err)
	}
	source := entry.threads[entry.current]
	source.session.SetHistory(transcriptMessages(4))
	source.alwaysAllow["shell"] = true
	store := srv.agentStore()
	store.save(entry.deviceID, source.id, source.model, source.session.History())
	var reply agentMsg
	ac := &agentConn{authed: true, deviceID: entry.deviceID, entry: entry, current: source.id, reply: func(msg agentMsg) { reply = msg }}
	draft := "review this replacement"
	(&agentWSHandler{srv: srv}).handleForkThread(ac, agentMsg{ID: "fork-request", Thread: source.id, MessageID: "m_0002", EditText: &draft})
	if reply.Op != "thread_switched" || reply.ID != "fork-request" || reply.Draft == nil || *reply.Draft != draft {
		t.Fatalf("fork reply=%+v", reply)
	}
	fork := entry.threads[ac.current]
	if fork == source || fork.running || len(fork.alwaysAllow) != 0 || fork.jobs == source.jobs || len(fork.session.History()) != 2 {
		t.Fatal("fork copied source runtime or replayed draft")
	}
	if len(source.session.History()) != 4 || !source.alwaysAllow["shell"] {
		t.Fatal("fork mutated source")
	}
	srv.waitAgentWork()
}

func TestTranscriptClearDeletesArchiveAndChildRetention(t *testing.T) {
	st := newTestStore(t)
	st.save("device", "thread", "model", transcriptMessages(2))
	child := st.childTranscriptsPath("device", "thread")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	st.clear("device", "thread")
	for _, path := range []string{st.transcriptPath("device", "thread"), child} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("retained deleted data %s: %v", path, err)
		}
	}
}

func TestRecentTranscriptCacheSurvivesUpdatesAndDoesNotReadDisk(t *testing.T) {
	st := newTestStore(t)
	messages := transcriptMessages(120)
	if err := st.captureTranscript("device", "thread", messages); err != nil {
		t.Fatal(err)
	}
	messages[118].Display = []byte(`{"time":1}`)
	if err := st.captureTranscript("device", "thread", messages[118:]); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(st.transcriptPath("device", "thread")); err != nil {
		t.Fatal(err)
	}
	page := st.recentTranscriptPage("device", "thread")
	if len(page.History) != 50 || !page.HasMore || page.Before != "m_0070" || string(page.History[48].Display) != `{"time":1}` {
		t.Fatalf("invalid cached snapshot: %+v", page)
	}
}

func TestHistoryPageHandlerCorrelatesAndRejectsUnauthenticatedAccess(t *testing.T) {
	srv := newPrefsTestServer(t)
	entry, err := srv.agentSessionFor(agentServerDeviceID, &ClientConn{ID: agentServerDeviceID})
	if err != nil {
		t.Fatal(err)
	}
	th := entry.threads[entry.current]
	th.session.SetHistory(transcriptMessages(120))
	srv.agentStore().save(entry.deviceID, th.id, th.model, th.session.History())
	var reply agentMsg
	ac := &agentConn{authed: true, deviceID: entry.deviceID, entry: entry, current: th.id, reply: func(msg agentMsg) { reply = msg }}
	handler := &agentWSHandler{srv: srv}
	handler.handleHistoryPage(ac, agentMsg{ID: "page-request", Thread: th.id, Limit: 500})
	if reply.Op != "history_page" || reply.ID != "page-request" || reply.Thread != th.id || len(reply.History) != 100 || !reply.HistoryHasMore {
		t.Fatalf("page=%+v", reply)
	}
	ac.authed = false
	handler.handleHistoryPage(ac, agentMsg{ID: "denied", Thread: th.id})
	if reply.Error == "" || reply.ID != "denied" || len(reply.History) != 0 {
		t.Fatalf("unauthenticated page leaked data: %+v", reply)
	}
}

func TestHistorySearchSnippetMapsUnicodeCaseExpansionToOriginal(t *testing.T) {
	for _, prefix := range []string{strings.Repeat("Ⱥ", 100), strings.Repeat("İ", 100), strings.Repeat("你好", 100)} {
		original := prefix + "NEEDLE" + strings.Repeat("尾", 200)
		snippet, ok := historySnippet(agentHistoryEntry{Role: "assistant", Text: original}, "needle")
		if !ok || !strings.Contains(snippet, "NEEDLE") || !strings.HasSuffix(snippet, strings.Repeat("尾", 160)) {
			t.Fatalf("invalid Unicode snippet: %q matched=%v", snippet, ok)
		}
	}
}

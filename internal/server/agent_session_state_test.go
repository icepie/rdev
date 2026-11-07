package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lxzan/gws"
	"rdev/internal/agent"
)

// --- prefs ---

func TestAgentPrefsAtomicWriteAndLoad(t *testing.T) {
	srv := &Server{DataDir: t.TempDir()}
	p := agentPrefs{
		Model:          "p/m",
		PermissionMode: "ask",
		ReasoningLevel: "low",
		CurrentThread:  "t_1",
	}
	if err := srv.saveAgentPrefs("dev 1", p); err != nil {
		t.Fatalf("saveAgentPrefs: %v", err)
	}
	// Sanitized file name, no leftover tmp file.
	path := srv.agentPrefsPath("dev 1")
	if !strings.HasSuffix(path, string(filepath.Separator)+"dev_1.json") {
		t.Fatalf("path = %q", path)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("tmp file left behind")
	}
	got := srv.loadAgentPrefs("dev 1")
	if !reflect.DeepEqual(got, p) {
		t.Fatalf("loaded = %+v, want %+v", got, p)
	}
	// A corrupt file yields zero prefs.
	if err := os.WriteFile(path, []byte("{nope"), 0600); err != nil {
		t.Fatal(err)
	}
	if zero := srv.loadAgentPrefs("dev 1"); !reflect.DeepEqual(zero, agentPrefs{}) {
		t.Fatalf("corrupt load = %+v", zero)
	}
}

// newPrefsTestServer builds a minimal agent server with two models and a
// data dir (client nil: no probes run).
func newPrefsTestServer(t *testing.T) *Server {
	t.Helper()
	return &Server{
		DataDir:         t.TempDir(),
		AgentSessionTTL: 24 * time.Hour,
		Agent: &AgentConfig{Providers: []AgentProvider{{
			ID:      "p",
			BaseURL: "http://127.0.0.1:1",
			APIKey:  "k",
			Models:  []AgentModelConfig{{ID: "m1"}, {ID: "m2"}},
		}}},
		AgentMaxTurns: 4,
	}
}

func TestAgentPrefsRestorePriority(t *testing.T) {
	seed := func(t *testing.T, srv *Server, prefs agentPrefs) {
		t.Helper()
		// One persisted thread whose meta model differs from the prefs model.
		srv.agentStore().save("dev1", "t_1", "p/m2", []agent.Message{{Role: "user", Content: "hi"}})
		if err := srv.saveAgentPrefs("dev1", prefs); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("prefs beat thread meta and restore modes", func(t *testing.T) {
		srv := newPrefsTestServer(t)
		seed(t, srv, agentPrefs{Model: "p/m1", PermissionMode: "ask", ReasoningLevel: "low", CurrentThread: "t_1"})
		entry, err := srv.agentSessionFor("dev1", nil)
		if err != nil {
			t.Fatal(err)
		}
		if entry.model != "p/m1" {
			t.Fatalf("model = %q, want p/m1 (prefs)", entry.model)
		}
		if entry.permMode != "ask" || entry.reasoningLevel != "low" {
			t.Fatalf("modes = %q/%q", entry.permMode, entry.reasoningLevel)
		}
		if entry.current != "t_1" {
			t.Fatalf("current = %q", entry.current)
		}
	})

	t.Run("unresolvable prefs model falls back to thread meta", func(t *testing.T) {
		srv := newPrefsTestServer(t)
		seed(t, srv, agentPrefs{Model: "p/bogus"})
		entry, err := srv.agentSessionFor("dev1", nil)
		if err != nil {
			t.Fatal(err)
		}
		if entry.model != "p/m2" {
			t.Fatalf("model = %q, want p/m2 (thread meta)", entry.model)
		}
	})

	t.Run("bogus currentThread falls back to most recent", func(t *testing.T) {
		srv := newPrefsTestServer(t)
		seed(t, srv, agentPrefs{CurrentThread: "t_nope"})
		entry, err := srv.agentSessionFor("dev1", nil)
		if err != nil {
			t.Fatal(err)
		}
		if entry.current != "t_1" {
			t.Fatalf("current = %q, want t_1", entry.current)
		}
	})

	t.Run("no prefs keeps defaults", func(t *testing.T) {
		srv := newPrefsTestServer(t)
		entry, err := srv.agentSessionFor("dev1", nil)
		if err != nil {
			t.Fatal(err)
		}
		if entry.permMode != agentPermWrite || entry.reasoningLevel != "medium" {
			t.Fatalf("modes = %q/%q", entry.permMode, entry.reasoningLevel)
		}
	})
}

// --- thread cwd / title persistence ---

func TestAgentSetCwdRewritesMetaAndReloads(t *testing.T) {
	st := newTestStore(t)
	msgs := []agent.Message{{Role: "user", Content: "hello"}, {Role: "assistant", Content: "hi"}}
	st.save("dev1", "t_1", "p/m", msgs)

	st.setCwd("dev1", "t_1", "/home/x/proj", msgs)
	_, model, cwd, _ := st.load("dev1", "t_1")
	if cwd != "/home/x/proj" || model != "p/m" {
		t.Fatalf("after setCwd: cwd=%q model=%q", cwd, model)
	}

	// A later model-change rewrite must carry the cwd forward.
	msgs = append(msgs, agent.Message{Role: "user", Content: "more"})
	st.save("dev1", "t_1", "p/m2", msgs)
	_, model, cwd, _ = st.load("dev1", "t_1")
	if cwd != "/home/x/proj" || model != "p/m2" {
		t.Fatalf("after model-change rewrite: cwd=%q model=%q", cwd, model)
	}

	// Clearing cwd persists too.
	st.setCwd("dev1", "t_1", "", msgs)
	if _, _, cwd, _ := st.load("dev1", "t_1"); cwd != "" {
		t.Fatalf("cleared cwd = %q", cwd)
	}
}

// A reset (store.clear) keeps the thread's extras so the next save
// re-persists cwd/title; only thread deletion drops them.
func TestAgentResetKeepsThreadExtras(t *testing.T) {
	st := newTestStore(t)
	msgs := []agent.Message{{Role: "user", Content: "hello"}}
	st.save("dev1", "t_1", "p/m", msgs)
	st.setCwd("dev1", "t_1", "/proj", msgs)
	st.setTitle("dev1", "t_1", "Named", msgs)

	st.clear("dev1", "t_1")
	if _, err := os.Stat(st.path("dev1", "t_1")); !os.IsNotExist(err) {
		t.Fatal("file not cleared")
	}
	// Next save after the reset carries cwd/title back into the meta line.
	st.save("dev1", "t_1", "p/m", msgs)
	_, _, cwd, title := st.load("dev1", "t_1")
	if cwd != "/proj" || title != "Named" {
		t.Fatalf("after reset+save: cwd=%q title=%q", cwd, title)
	}

	// Deletion drops everything.
	st.clear("dev1", "t_1")
	st.dropExtras("dev1", "t_1")
	st.save("dev1", "t_1", "p/m", msgs)
	if _, _, cwd, title := st.load("dev1", "t_1"); cwd != "" || title != "" {
		t.Fatalf("after delete+save: cwd=%q title=%q", cwd, title)
	}
}

func TestAgentRenamePersistsExplicitTitle(t *testing.T) {
	st := newTestStore(t)
	msgs := []agent.Message{{Role: "user", Content: "first user message"}}
	st.save("dev1", "t_1", "p/m", msgs)

	// Derived title before rename.
	if metas := st.listThreads("dev1"); len(metas) != 1 || metas[0].Title != "first user message" {
		t.Fatalf("metas = %+v", metas)
	}
	st.setTitle("dev1", "t_1", "Renamed", msgs)
	metas := st.listThreads("dev1")
	if len(metas) != 1 || metas[0].Title != "Renamed" || metas[0].MetaTitle != "Renamed" {
		t.Fatalf("metas after rename = %+v", metas)
	}
	if _, _, _, title := st.load("dev1", "t_1"); title != "Renamed" {
		t.Fatalf("reloaded title = %q", title)
	}
	// The explicit title survives later rewrites.
	st.save("dev1", "t_1", "p/m2", append(msgs, agent.Message{Role: "user", Content: "next"}))
	if metas := st.listThreads("dev1"); metas[0].Title != "Renamed" {
		t.Fatalf("title after rewrite = %q", metas[0].Title)
	}
}

// --- relative path join ---

func TestResolveDevicePath(t *testing.T) {
	cases := []struct {
		goos, cwd, path, want string
	}{
		// Empty cwd or path: unchanged.
		{"linux", "", "a/b", "a/b"},
		{"linux", "/p", "", ""},
		// unix.
		{"linux", "/home/x/proj", "src/main.go", "/home/x/proj/src/main.go"},
		{"linux", "/home/x/proj/", "src/main.go", "/home/x/proj/src/main.go"},
		{"linux", "/home/x/proj", "/etc/hosts", "/etc/hosts"},
		{"darwin", "/Users/x", "f", "/Users/x/f"},
		// windows.
		{"windows", `C:\work\proj`, `src\main.go`, `C:\work\proj\src\main.go`},
		{"windows", `C:\work\proj\`, "f.txt", `C:\work\proj\f.txt`},
		{"windows", `C:\work\proj`, `D:\other\f`, `D:\other\f`},
		{"windows", `C:\work\proj`, `d:/other/f`, `d:/other/f`},
		{"windows", `C:\work\proj`, `\\srv\share\f`, `\\srv\share\f`},
		{"windows", `C:\work\proj`, `\rooted`, `\rooted`},
		{"windows", "", "rel", "rel"},
	}
	for _, c := range cases {
		if got := resolveDevicePath(c.goos, c.cwd, c.path); got != c.want {
			t.Errorf("resolveDevicePath(%q, %q, %q) = %q, want %q", c.goos, c.cwd, c.path, got, c.want)
		}
	}
}

// --- branch probe command construction ---

func TestGitBranchProbeCommand(t *testing.T) {
	if got := gitBranchProbeCommand("linux", "/home/x/proj"); got != `git -C '/home/x/proj' branch --show-current` {
		t.Fatalf("unix = %q", got)
	}
	// Single quotes inside the path are POSIX-escaped.
	if got := gitBranchProbeCommand("linux", "/home/x/it's"); got != `git -C '/home/x/it'\''s' branch --show-current` {
		t.Fatalf("unix quote = %q", got)
	}
	if got := gitBranchProbeCommand("windows", `C:\work\proj`); got != `git -C "C:\work\proj" branch --show-current` {
		t.Fatalf("windows = %q", got)
	}
}

// --- end-to-end WS: auth_ok cwd/branch, ping→pong, rename, set_cwd ---

type recordTransport struct {
	mu   sync.Mutex
	json [][]byte
}

func (t *recordTransport) WriteJSON(p []byte) error {
	t.mu.Lock()
	t.json = append(t.json, p)
	t.mu.Unlock()
	return nil
}
func (t *recordTransport) WriteBinary([]byte) error { return nil }
func (t *recordTransport) WritePing([]byte) error   { return nil }
func (t *recordTransport) Close(string) error       { return nil }
func (t *recordTransport) RemoteAddr() string       { return "fake" }

type agentWSRecorder struct {
	gws.BuiltinEventHandler
	msgs chan agentMsg
}

func (c *agentWSRecorder) OnMessage(_ *gws.Conn, message *gws.Message) {
	defer message.Close()
	var m agentMsg
	if err := json.Unmarshal(message.Bytes(), &m); err == nil {
		c.msgs <- m
	}
}

// waitAgentMsg drains ch until a message matches pred.
func waitAgentMsg(t *testing.T, ch chan agentMsg, what string, pred func(agentMsg) bool) agentMsg {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case m := <-ch:
			if pred(m) {
				return m
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func TestAgentWSSessionState(t *testing.T) {
	srv := newPrefsTestServer(t)
	srv.clients = map[string]*ClientConn{}
	srv.sessions = map[string]*ProxySession{}
	device := &ClientConn{
		ID:        "dev1",
		OS:        "linux",
		Transport: &recordTransport{},
		Sessions:  map[string]*ProxySession{},
		Forwards:  map[string]*ProxyForward{},
	}
	srv.clients["dev1"] = device

	httpSrv := httptest.NewServer(http.HandlerFunc(srv.HandleAgentWS))
	defer httpSrv.Close()

	rec := &agentWSRecorder{msgs: make(chan agentMsg, 64)}
	socket, _, err := gws.NewClient(rec, &gws.ClientOption{
		Addr:               "ws" + strings.TrimPrefix(httpSrv.URL, "http") + "/agent?device=dev1",
		HandshakeTimeout:   5 * time.Second,
		ReadMaxPayloadSize: 24 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer socket.WriteClose(1000, nil)
	go socket.ReadLoop()

	send := func(payload string) {
		t.Helper()
		if err := socket.WriteMessage(gws.OpcodeText, []byte(payload)); err != nil {
			t.Fatalf("send %s: %v", payload, err)
		}
	}

	auth := waitAgentMsg(t, rec.msgs, "auth_ok", func(m agentMsg) bool { return m.Op == "auth_ok" })
	if auth.Thread == "" {
		t.Fatal("auth_ok has no current thread")
	}
	threadID := auth.Thread

	// ping → pong.
	send(`{"op":"ping"}`)
	waitAgentMsg(t, rec.msgs, "pong", func(m agentMsg) bool { return m.Op == "pong" })

	// rename_thread → thread_list broadcast with the explicit title.
	send(`{"op":"rename_thread","thread":"` + threadID + `","title":"My Project"}`)
	waitAgentMsg(t, rec.msgs, "thread_list with new title", func(m agentMsg) bool {
		if m.Op != "thread_list" {
			return false
		}
		for _, th := range m.Threads {
			if th.ID == threadID && th.Title == "My Project" {
				return true
			}
		}
		return false
	})
	if metas := srv.agentStore().listThreads("dev1"); len(metas) != 1 || metas[0].Title != "My Project" {
		t.Fatalf("persisted metas = %+v", metas)
	}

	// Empty title is rejected.
	send(`{"op":"rename_thread","thread":"` + threadID + `","title":"  "}`)
	waitAgentMsg(t, rec.msgs, "rename error", func(m agentMsg) bool { return m.Op == "thread_action_error" })

	// set_cwd → thread_meta with the new cwd, persisted in the meta line.
	send(`{"op":"set_cwd","path":"/tmp/proj"}`)
	waitAgentMsg(t, rec.msgs, "thread_meta with cwd", func(m agentMsg) bool {
		return m.Op == "thread_meta" && m.Thread == threadID && m.Cwd == "/tmp/proj"
	})
	_, _, cwd, title := srv.agentStore().load("dev1", threadID)
	if cwd != "/tmp/proj" || title != "My Project" {
		t.Fatalf("reloaded cwd/title = %q/%q", cwd, title)
	}
	srv.agentSessionsMu.Lock()
	th := srv.agentSessions["dev1"].threads[threadID]
	thCwd := th.cwd
	srv.agentSessionsMu.Unlock()
	if thCwd != "/tmp/proj" {
		t.Fatalf("thread cwd = %q", thCwd)
	}

	// set_cwd "" restores the device default.
	send(`{"op":"set_cwd","path":""}`)
	waitFor(t, "thread cwd cleared", func() bool {
		srv.agentSessionsMu.Lock()
		defer srv.agentSessionsMu.Unlock()
		return srv.agentSessions["dev1"].threads[threadID].cwd == ""
	})
	if _, _, cwd, _ := srv.agentStore().load("dev1", threadID); cwd != "" {
		t.Fatalf("persisted cleared cwd = %q", cwd)
	}

	// switch_thread persists the current thread in prefs. Switching to the
	// thread the connection already views is a no-op by design (double-
	// click protection), so make a second thread current and switch back.
	send(`{"op":"new_thread"}`)
	waitAgentMsg(t, rec.msgs, "thread_switched to new", func(m agentMsg) bool {
		return m.Op == "thread_switched" && m.Thread != threadID
	})
	send(`{"op":"switch_thread","thread":"` + threadID + `"}`)
	waitAgentMsg(t, rec.msgs, "thread_switched", func(m agentMsg) bool {
		return m.Op == "thread_switched" && m.Thread == threadID
	})
	waitFor(t, "prefs written", func() bool {
		return srv.loadAgentPrefs("dev1").CurrentThread == threadID
	})
}

// --- turn lifecycle: stop, reset, queued model switch, reattach ---

// sseTextLine builds one `data:` SSE line carrying a content delta.
func sseTextLine(content string) string {
	chunk := map[string]any{"choices": []any{map[string]any{
		"index": 0,
		"delta": map[string]any{"content": content},
	}}}
	data, _ := json.Marshal(chunk)
	return "data: " + string(data) + "\n\n"
}

const sseDoneLine = "data: [DONE]\n\n"

// newTurnTestServer builds an agent server whose provider is the given fake
// LLM, with one registered fake device.
func newTurnTestServer(t *testing.T, llmURL string) *Server {
	t.Helper()
	srv := newPrefsTestServer(t)
	t.Cleanup(func() { srv.waitAgentWork() })
	srv.Agent.Providers[0].BaseURL = llmURL
	srv.clients = map[string]*ClientConn{}
	srv.sessions = map[string]*ProxySession{}
	srv.clients["dev1"] = &ClientConn{
		ID:        "dev1",
		OS:        "linux",
		Transport: &recordTransport{},
		Sessions:  map[string]*ProxySession{},
		Forwards:  map[string]*ProxyForward{},
	}
	return srv
}

// dialAgentWS opens one browser socket against the agent endpoint.
func dialAgentWS(t *testing.T, httpSrv *httptest.Server) (*gws.Conn, *agentWSRecorder) {
	t.Helper()
	rec := &agentWSRecorder{msgs: make(chan agentMsg, 64)}
	socket, _, err := gws.NewClient(rec, &gws.ClientOption{
		Addr:               "ws" + strings.TrimPrefix(httpSrv.URL, "http") + "/agent?device=dev1",
		HandshakeTimeout:   5 * time.Second,
		ReadMaxPayloadSize: 24 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { socket.WriteClose(1000, nil) })
	go socket.ReadLoop()
	return socket, rec
}

func sendAgentOp(t *testing.T, socket *gws.Conn, payload string) {
	t.Helper()
	if err := socket.WriteMessage(gws.OpcodeText, []byte(payload)); err != nil {
		t.Fatalf("send %s: %v", payload, err)
	}
}

// blockingLLM serves request 1 by streaming one delta and blocking until
// release is closed; later requests answer immediately.
func blockingLLM(t *testing.T, release <-chan struct{}) *httptest.Server {
	t.Helper()
	var hits atomic.Int32
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		fmt.Fprint(w, sseTextLine("partial"))
		if fl != nil {
			fl.Flush()
		}
		if n == 1 {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		fmt.Fprint(w, sseTextLine("answer")+sseDoneLine)
	}))
	t.Cleanup(llm.Close)
	return llm
}

// drainAgentOps collects every frame arriving within the window.
func drainAgentOps(ch chan agentMsg, window time.Duration) []agentMsg {
	deadline := time.After(window)
	var out []agentMsg
	for {
		select {
		case m := <-ch:
			out = append(out, m)
		case <-deadline:
			return out
		}
	}
}

func TestAgentStopSingleStoppedFrame(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	srv := newTurnTestServer(t, blockingLLM(t, release).URL)
	httpSrv := httptest.NewServer(http.HandlerFunc(srv.HandleAgentWS))
	defer httpSrv.Close()
	socket, rec := dialAgentWS(t, httpSrv)

	auth := waitAgentMsg(t, rec.msgs, "auth_ok", func(m agentMsg) bool { return m.Op == "auth_ok" })
	sendAgentOp(t, socket, `{"op":"chat","text":"hi"}`)
	waitAgentMsg(t, rec.msgs, "turn_start", func(m agentMsg) bool { return m.Op == "turn_start" })
	// Wait for the first streamed delta so the canceled turn has partial
	// content to persist.
	waitAgentMsg(t, rec.msgs, "delta", func(m agentMsg) bool { return m.Op == "delta" })

	sendAgentOp(t, socket, `{"op":"stop"}`)
	stopped := waitAgentMsg(t, rec.msgs, "stopped", func(m agentMsg) bool { return m.Op == "stopped" })
	if !stopped.Canceled {
		t.Fatal("stop of a running turn must carry canceled:true")
	}
	if stopped.Thread != auth.Thread {
		t.Fatalf("stopped thread = %q, want %q", stopped.Thread, auth.Thread)
	}
	// Exactly one terminal frame: no done/error/second stopped afterwards.
	for _, m := range drainAgentOps(rec.msgs, 300*time.Millisecond) {
		if m.Op == "stopped" || m.Op == "done" || m.Op == "error" {
			t.Fatalf("unexpected extra terminal frame: %+v", m)
		}
	}
	// The partial stream was persisted (cancel-with-partial).
	msgs, _, _, _ := srv.agentStore().load("dev1", auth.Thread)
	if len(msgs) != 2 || msgs[0].Role != "user" || msgs[1].Role != "assistant" || msgs[1].Content != "partial" {
		t.Fatalf("persisted history = %+v", msgs)
	}
}

func TestAgentResetMidTurn(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	srv := newTurnTestServer(t, blockingLLM(t, release).URL)
	httpSrv := httptest.NewServer(http.HandlerFunc(srv.HandleAgentWS))
	defer httpSrv.Close()
	socket, rec := dialAgentWS(t, httpSrv)

	auth := waitAgentMsg(t, rec.msgs, "auth_ok", func(m agentMsg) bool { return m.Op == "auth_ok" })
	sendAgentOp(t, socket, `{"op":"chat","text":"hi"}`)
	waitAgentMsg(t, rec.msgs, "turn_start", func(m agentMsg) bool { return m.Op == "turn_start" })
	// Wait for the first streamed delta: the provider counts its requests,
	// so the blocked first request must exist before the reset cancels it.
	waitAgentMsg(t, rec.msgs, "delta", func(m agentMsg) bool { return m.Op == "delta" })

	sendAgentOp(t, socket, `{"op":"reset"}`)
	resetOK := waitAgentMsg(t, rec.msgs, "reset_ok", func(m agentMsg) bool { return m.Op == "reset_ok" })
	if resetOK.Thread != auth.Thread {
		t.Fatalf("reset_ok thread = %q, want %q", resetOK.Thread, auth.Thread)
	}
	// The canceled turn unwinds silently: the epoch mismatch suppresses its
	// terminal frame, so no stopped/done/error may follow reset_ok.
	for _, m := range drainAgentOps(rec.msgs, 300*time.Millisecond) {
		if m.Op == "stopped" || m.Op == "done" || m.Op == "error" {
			t.Fatalf("stale turn frame after reset: %+v", m)
		}
	}
	// The unwind must not resurrect the cleared history file.
	if msgs, _, _, _ := srv.agentStore().load("dev1", auth.Thread); len(msgs) != 0 {
		t.Fatalf("history resurrected after reset: %+v", msgs)
	}
	// The thread is immediately usable again (provider answers at once).
	sendAgentOp(t, socket, `{"op":"chat","text":"again"}`)
	waitAgentMsg(t, rec.msgs, "done after reset", func(m agentMsg) bool { return m.Op == "done" })
}

func TestAgentQueuedChatModelSwitch(t *testing.T) {
	release := make(chan struct{})
	srv := newTurnTestServer(t, blockingLLM(t, release).URL)
	httpSrv := httptest.NewServer(http.HandlerFunc(srv.HandleAgentWS))
	defer httpSrv.Close()
	socket, rec := dialAgentWS(t, httpSrv)

	auth := waitAgentMsg(t, rec.msgs, "auth_ok", func(m agentMsg) bool { return m.Op == "auth_ok" })
	sendAgentOp(t, socket, `{"op":"chat","text":"one"}`)
	waitAgentMsg(t, rec.msgs, "turn_start", func(m agentMsg) bool { return m.Op == "turn_start" })

	// A second chat while running queues and may carry a model switch.
	sendAgentOp(t, socket, `{"op":"chat","text":"two","model":"p/m2"}`)
	queued := waitAgentMsg(t, rec.msgs, "queue", func(m agentMsg) bool { return m.Op == "queue" })
	var items []string
	if err := json.Unmarshal(queued.Items, &items); err != nil || len(items) != 1 {
		t.Fatalf("queue items = %s (%v)", queued.Items, err)
	}

	close(release)
	// Turn 1 completes on p/m1, then the queued turn runs on p/m2.
	firstDone := waitAgentMsg(t, rec.msgs, "first done", func(m agentMsg) bool { return m.Op == "done" })
	if firstDone.Model != "p/m1" {
		t.Fatalf("first done model = %q, want p/m1", firstDone.Model)
	}
	waitAgentMsg(t, rec.msgs, "second turn_start", func(m agentMsg) bool {
		return m.Op == "turn_start" && m.Text == "two"
	})
	// The second done belongs to the queued turn, which ran on p/m2.
	waitAgentMsg(t, rec.msgs, "second done", func(m agentMsg) bool {
		return m.Op == "done" && m.Model == "p/m2"
	})
	// The thread keeps the switched model.
	srv.agentSessionsMu.Lock()
	thModel := srv.agentSessions["dev1"].threads[auth.Thread].model
	srv.agentSessionsMu.Unlock()
	if thModel != "p/m2" {
		t.Fatalf("thread model = %q, want p/m2", thModel)
	}
}

func TestAgentReattachWorkingSinceStable(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	srv := newTurnTestServer(t, blockingLLM(t, release).URL)
	httpSrv := httptest.NewServer(http.HandlerFunc(srv.HandleAgentWS))
	defer httpSrv.Close()

	socket1, rec1 := dialAgentWS(t, httpSrv)
	auth := waitAgentMsg(t, rec1.msgs, "auth_ok", func(m agentMsg) bool { return m.Op == "auth_ok" })
	sendAgentOp(t, socket1, `{"op":"chat","text":"hi"}`)
	waitAgentMsg(t, rec1.msgs, "turn_start", func(m agentMsg) bool { return m.Op == "turn_start" })

	// A second tab attaches mid-turn: it must see the same working.since a
	// thread switch reports (no Date.now() resets).
	_, rec2 := dialAgentWS(t, httpSrv)
	auth2 := waitAgentMsg(t, rec2.msgs, "auth_ok #2", func(m agentMsg) bool { return m.Op == "auth_ok" })
	if !auth2.Running || auth2.Working == nil || auth2.Working.Since == "" {
		t.Fatalf("auth2 running/working = %v/%+v", auth2.Running, auth2.Working)
	}
	// socket1 already views auth.Thread, and switching to the viewed
	// thread is a no-op, so detour through a fresh thread and switch back
	// to exercise the snapshot path.
	sendAgentOp(t, socket1, `{"op":"new_thread"}`)
	waitAgentMsg(t, rec1.msgs, "thread_switched to new", func(m agentMsg) bool {
		return m.Op == "thread_switched" && m.Thread != auth.Thread
	})
	sendAgentOp(t, socket1, `{"op":"switch_thread","thread":"`+auth.Thread+`"}`)
	sw := waitAgentMsg(t, rec1.msgs, "thread_switched", func(m agentMsg) bool {
		return m.Op == "thread_switched" && m.Thread == auth.Thread
	})
	if sw.Working == nil || sw.Working.Since != auth2.Working.Since {
		t.Fatalf("working.since mismatch: auth2 %v vs switched %+v", auth2.Working.Since, sw.Working)
	}
	// The reattached tab replays the live backlog (turn_start + delta).
	var sawLive bool
	for _, m := range drainAgentOps(rec2.msgs, 300*time.Millisecond) {
		if m.Op == "delta" {
			sawLive = true
		}
	}
	if !sawLive {
		t.Fatal("reattached tab missed the live-frame replay")
	}
}

// TestAgentConcurrentChatStartsOneTurn: two tabs chat into the same idle
// thread at once; the single-lock handleChat must start exactly one turn
// and queue the other (A3 race regression).
func TestAgentConcurrentChatStartsOneTurn(t *testing.T) {
	release := make(chan struct{})
	srv := newTurnTestServer(t, blockingLLM(t, release).URL)
	httpSrv := httptest.NewServer(http.HandlerFunc(srv.HandleAgentWS))
	defer httpSrv.Close()

	socket1, rec1 := dialAgentWS(t, httpSrv)
	waitAgentMsg(t, rec1.msgs, "auth_ok", func(m agentMsg) bool { return m.Op == "auth_ok" })
	socket2, rec2 := dialAgentWS(t, httpSrv)
	waitAgentMsg(t, rec2.msgs, "auth_ok #2", func(m agentMsg) bool { return m.Op == "auth_ok" })

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); sendAgentOp(t, socket1, `{"op":"chat","text":"from one"}`) }()
	go func() { defer wg.Done(); sendAgentOp(t, socket2, `{"op":"chat","text":"from two"}`) }()
	wg.Wait()

	// Exactly one turn starts; the losing chat lands in the queue.
	waitAgentMsg(t, rec1.msgs, "turn_start", func(m agentMsg) bool { return m.Op == "turn_start" })
	queued := waitAgentMsg(t, rec1.msgs, "queue", func(m agentMsg) bool { return m.Op == "queue" })
	var items []string
	if err := json.Unmarshal(queued.Items, &items); err != nil || len(items) != 1 {
		t.Fatalf("queue items = %s (%v), want exactly one queued chat", queued.Items, err)
	}
	for _, m := range drainAgentOps(rec1.msgs, 200*time.Millisecond) {
		if m.Op == "turn_start" {
			t.Fatalf("second turn started while first blocked: %+v", m)
		}
	}

	// After the first turn unblocks, the queued chat runs as turn two.
	close(release)
	waitAgentMsg(t, rec1.msgs, "first done", func(m agentMsg) bool { return m.Op == "done" })
	waitAgentMsg(t, rec1.msgs, "second turn_start", func(m agentMsg) bool { return m.Op == "turn_start" })
	waitAgentMsg(t, rec1.msgs, "second done", func(m agentMsg) bool { return m.Op == "done" })
}

// TestAgentStopIdleAck: stop with no running turn answers a single
// thread-tagged stopped{canceled:false} ack (no error, no side effects).
func TestAgentStopIdleAck(t *testing.T) {
	srv := newTurnTestServer(t, "http://127.0.0.1:1")
	httpSrv := httptest.NewServer(http.HandlerFunc(srv.HandleAgentWS))
	defer httpSrv.Close()
	socket, rec := dialAgentWS(t, httpSrv)

	auth := waitAgentMsg(t, rec.msgs, "auth_ok", func(m agentMsg) bool { return m.Op == "auth_ok" })
	sendAgentOp(t, socket, `{"op":"stop"}`)
	stopped := waitAgentMsg(t, rec.msgs, "stopped", func(m agentMsg) bool { return m.Op == "stopped" })
	if stopped.Canceled {
		t.Fatal("idle stop must carry canceled:false")
	}
	if stopped.Thread != auth.Thread {
		t.Fatalf("stopped thread = %q, want %q", stopped.Thread, auth.Thread)
	}
	for _, m := range drainAgentOps(rec.msgs, 200*time.Millisecond) {
		if m.Op == "stopped" || m.Op == "done" || m.Op == "error" {
			t.Fatalf("unexpected frame after idle stop: %+v", m)
		}
	}
}

// TestAgentAttachAfterDone: attaching once the turn finished yields the
// full persisted history (user + assistant) and no live frames — the
// history/live disjointness invariant, idle side.
func TestAgentAttachAfterDone(t *testing.T) {
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseTextLine("answer")+sseDoneLine)
	}))
	defer llm.Close()
	srv := newTurnTestServer(t, llm.URL)
	httpSrv := httptest.NewServer(http.HandlerFunc(srv.HandleAgentWS))
	defer httpSrv.Close()

	socket1, rec1 := dialAgentWS(t, httpSrv)
	waitAgentMsg(t, rec1.msgs, "auth_ok", func(m agentMsg) bool { return m.Op == "auth_ok" })
	sendAgentOp(t, socket1, `{"op":"chat","text":"hi"}`)
	waitAgentMsg(t, rec1.msgs, "done", func(m agentMsg) bool { return m.Op == "done" })

	_, rec2 := dialAgentWS(t, httpSrv)
	auth2 := waitAgentMsg(t, rec2.msgs, "auth_ok #2", func(m agentMsg) bool { return m.Op == "auth_ok" })
	if auth2.Running {
		t.Fatal("auth_ok running = true after done")
	}
	if len(auth2.Live) != 0 {
		t.Fatalf("auth_ok live = %d frames after done, want 0", len(auth2.Live))
	}
	if len(auth2.History) != 2 {
		t.Fatalf("auth_ok history = %d entries, want 2 (user + assistant)", len(auth2.History))
	}
	// No turn frames are replayed to the attacher.
	for _, m := range drainAgentOps(rec2.msgs, 200*time.Millisecond) {
		if m.Op == "delta" || m.Op == "turn_start" || m.Op == "done" {
			t.Fatalf("stale turn frame replayed to attacher: %+v", m)
		}
	}
}

func TestThreadSnapshotRestoresWaitingState(t *testing.T) {
	th := &agentThread{id: "one", title: "One", running: true,
		approvals: map[string]chan approvalDecision{"approval": make(chan approvalDecision, 1)},
		questions: map[string]chan string{"question": make(chan string, 1)}}
	entry := &agentSessionEntry{threads: map[string]*agentThread{"one": th}}
	if got := agentThreadListLocked(entry)[0].Waiting; got != "approval" {
		t.Fatalf("approval snapshot: %q", got)
	}
	delete(th.approvals, "approval")
	if got := agentThreadListLocked(entry)[0].Waiting; got != "answer" {
		t.Fatalf("question snapshot: %q", got)
	}
	delete(th.questions, "question")
	if got := agentThreadListLocked(entry)[0].Waiting; got != "" {
		t.Fatalf("resolved snapshot: %q", got)
	}
	th.questions["stale"] = make(chan string, 1)
	th.running = false
	if got := agentThreadListLocked(entry)[0].Waiting; got != "" {
		t.Fatalf("idle snapshot retained waiting: %q", got)
	}
}

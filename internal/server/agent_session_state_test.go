package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	if got != p {
		t.Fatalf("loaded = %+v, want %+v", got, p)
	}
	// A corrupt file yields zero prefs.
	if err := os.WriteFile(path, []byte("{nope"), 0600); err != nil {
		t.Fatal(err)
	}
	if zero := srv.loadAgentPrefs("dev 1"); zero != (agentPrefs{}) {
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
	waitAgentMsg(t, rec.msgs, "rename error", func(m agentMsg) bool { return m.Op == "error" })

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

	// switch_thread persists the current thread in prefs.
	send(`{"op":"switch_thread","thread":"` + threadID + `"}`)
	waitAgentMsg(t, rec.msgs, "thread_switched", func(m agentMsg) bool {
		return m.Op == "thread_switched" && m.Thread == threadID
	})
	waitFor(t, "prefs written", func() bool {
		return srv.loadAgentPrefs("dev1").CurrentThread == threadID
	})
}

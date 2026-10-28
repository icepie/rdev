package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lxzan/gws"
	"rdev/internal/protocol"
)

// --- porcelain parsing ---

func TestParseGitStatusPorcelain(t *testing.T) {
	out := strings.Join([]string{
		" M internal/x.go",       // worktree-modified
		"M  staged.go",           // index-modified
		"MM both.go",             // both
		"A  new file.go",         // added, space in path
		" D gone.go",             // deleted in worktree
		"D  staged_gone.go",      // deleted in index
		"?? untracked dir/f.go",  // untracked
		"R  old.go -> new name.go", // rename: take the new path
		`M  "quo\"ted.go"`,       // C-quoted path
		"U  conflicted.go",       // unmerged
		"!! ignored.go",          // ignored: skipped
		"",                       // trailing newline
		"garbage",
		"XYnospace",
	}, "\n")
	want := []agentChangeFile{
		{Path: "internal/x.go", Status: "M"},
		{Path: "staged.go", Status: "M"},
		{Path: "both.go", Status: "M"},
		{Path: "new file.go", Status: "A"},
		{Path: "gone.go", Status: "D"},
		{Path: "staged_gone.go", Status: "D"},
		{Path: "untracked dir/f.go", Status: "??"},
		{Path: "new name.go", Status: "R"},
		{Path: `quo"ted.go`, Status: "M"},
		{Path: "conflicted.go", Status: "U"},
	}
	if got := parseGitStatusPorcelain(out); !reflect.DeepEqual(got, want) {
		t.Fatalf("parseGitStatusPorcelain = %+v, want %+v", got, want)
	}
	if got := parseGitStatusPorcelain(""); got == nil || len(got) != 0 {
		t.Fatalf("empty parse = %+v (nil=%v)", got, got == nil)
	}
}

func TestParseGitNumstat(t *testing.T) {
	out := strings.Join([]string{
		"10\t2\tinternal/x.go",
		"0\t5\tgone.go",
		"-\t-\timg.png",           // binary: zeros
		"3\t1\tweb/{old.tsx => new.tsx}", // brace rename: new path
		"7\t0\tdir/old name.go => dir/new name.go", // plain rename
		"2\t2\t\"quo\\\"ted.go\"", // C-quoted path
		"garbage",
		"1\t2",                    // missing path field
	}, "\n")
	want := map[string][2]int{
		"internal/x.go":     {10, 2},
		"gone.go":           {0, 5},
		"web/new.tsx":       {3, 1},
		"dir/new name.go":   {7, 0},
		`quo"ted.go`:        {2, 2},
	}
	if got := parseGitNumstat(out); !reflect.DeepEqual(got, want) {
		t.Fatalf("parseGitNumstat = %v, want %v", got, want)
	}
}

func TestMergeGitNumstat(t *testing.T) {
	files := []agentChangeFile{
		{Path: "a.go", Status: "M"},
		{Path: "b.go", Status: "??"}, // untracked: no numstat row
	}
	mergeGitNumstat(files, map[string][2]int{"a.go": {4, 1}})
	if files[0].Adds != 4 || files[0].Dels != 1 {
		t.Fatalf("tracked file = %+v", files[0])
	}
	if files[1].Adds != 0 || files[1].Dels != 0 {
		t.Fatalf("untracked file = %+v", files[1])
	}
}

func TestFilterFileSearch(t *testing.T) {
	paths := []string{
		"web/ui/pages/Agent.tsx",
		"web/ui/pages/Files.tsx",
		"internal/server/agent.go",
		"cmd/agent-test/main.go",
		"README.md",
	}
	// AND words, case-insensitive, basename hits first.
	got, dropped := filterFileSearch(paths, "AGENT tsx", 50)
	if dropped {
		t.Fatal("unexpected dropped")
	}
	if len(got) != 1 || got[0] != "web/ui/pages/Agent.tsx" {
		t.Fatalf("got %v", got)
	}
	// Basename matches rank before directory-only matches, then shorter
	// paths; "Agent.tsx" and "agent.go" both hit on the basename, so the
	// shorter path wins and "agent-test" (dir-only) comes last.
	got, _ = filterFileSearch(paths, "agent", 50)
	if len(got) != 3 ||
		got[0] != "web/ui/pages/Agent.tsx" ||
		got[1] != "internal/server/agent.go" ||
		got[2] != "cmd/agent-test/main.go" {
		t.Fatalf("ranking = %v", got)
	}
	// Limit reports dropped matches.
	_, dropped = filterFileSearch(paths, "agent", 1)
	if !dropped {
		t.Fatal("expected dropped=true")
	}
	// No words → empty.
	if got, _ := filterFileSearch(paths, "  ", 50); len(got) != 0 {
		t.Fatalf("blank query = %v", got)
	}
}

func TestGitProbeCommands(t *testing.T) {
	if got := gitStatusProbeCommand("linux", "/home/x/proj"); got != `git -C '/home/x/proj' status --porcelain=v1 -uall` {
		t.Fatalf("unix status = %q", got)
	}
	if got := gitStatusProbeCommand("windows", `C:\work\proj`); got != `git -C "C:\work\proj" status --porcelain=v1 -uall` {
		t.Fatalf("windows status = %q", got)
	}
	if got := gitDiffProbeCommand("linux", "/home/x/proj", "a b.go", true); got != `git -C '/home/x/proj' diff HEAD -- 'a b.go'` {
		t.Fatalf("unix diff HEAD = %q", got)
	}
	if got := gitDiffProbeCommand("linux", "/home/x/proj", "a.go", false); got != `git -C '/home/x/proj' diff -- 'a.go'` {
		t.Fatalf("unix diff fallback = %q", got)
	}
	if got := gitDiffProbeCommand("windows", `C:\work\proj`, `a"b.go`, true); got != `git -C "C:\work\proj" diff HEAD -- "a\"b.go"` {
		t.Fatalf("windows diff = %q", got)
	}
	if got := gitLsFilesProbeCommand("linux", "/home/x/proj", "it's.go"); got != `git -C '/home/x/proj' ls-files --error-unmatch -- 'it'\''s.go'` {
		t.Fatalf("unix ls-files = %q", got)
	}
	if got := gitNumstatProbeCommand("linux", "/home/x/proj", true); got != `git -C '/home/x/proj' diff --numstat HEAD --` {
		t.Fatalf("unix numstat HEAD = %q", got)
	}
	if got := gitNumstatProbeCommand("windows", `C:\work\proj`, false); got != `git -C "C:\work\proj" diff --numstat --` {
		t.Fatalf("windows numstat fallback = %q", got)
	}
}

// --- scripted fake device for probe round-trips ---

// scriptedTransport answers MsgNewSession probe commands with canned
// stdout/exit codes, emulating a device's exec session (data, exit code,
// close).
type scriptedTransport struct {
	srv     *Server
	respond func(command string) (string, int)

	mu   sync.Mutex
	cmds []string
}

func (t *scriptedTransport) WriteJSON(p []byte) error {
	var msg protocol.Message
	if err := json.Unmarshal(p, &msg); err != nil {
		return nil
	}
	if msg.Type != protocol.MsgNewSession {
		return nil
	}
	out, code := t.respond(msg.Command)
	t.mu.Lock()
	t.cmds = append(t.cmds, msg.Command)
	t.mu.Unlock()
	go t.reply(msg.SessionID, out, code)
	return nil
}

func (t *scriptedTransport) reply(sessionID, out string, code int) {
	t.srv.sessMu.RLock()
	sess := t.srv.sessions[sessionID]
	t.srv.sessMu.RUnlock()
	if sess == nil {
		return
	}
	if out != "" {
		sess.WriteCh <- []byte(out)
	}
	// Let the probe loop drain WriteCh before signalling close: with both
	// channels ready, select would pick randomly and could drop output.
	time.Sleep(20 * time.Millisecond)
	sess.SetExitCode(code)
	sess.SignalClose()
}

func (t *scriptedTransport) WriteBinary([]byte) error { return nil }
func (t *scriptedTransport) WritePing([]byte) error   { return nil }
func (t *scriptedTransport) Close(string) error       { return nil }
func (t *scriptedTransport) RemoteAddr() string       { return "fake" }

func (t *scriptedTransport) commands() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.cmds...)
}

// newProbeTestServer builds a server with one scripted linux device.
func newProbeTestServer(t *testing.T, respond func(string) (string, int)) (*Server, *scriptedTransport) {
	t.Helper()
	srv := newPrefsTestServer(t)
	srv.clients = map[string]*ClientConn{}
	srv.sessions = map[string]*ProxySession{}
	tr := &scriptedTransport{srv: srv, respond: respond}
	srv.clients["dev1"] = &ClientConn{
		ID:        "dev1",
		OS:        "linux",
		Transport: tr,
		Sessions:  map[string]*ProxySession{},
		Forwards:  map[string]*ProxyForward{},
	}
	return srv, tr
}

func TestProbeGitChanges(t *testing.T) {
	t.Run("parses status output", func(t *testing.T) {
		srv, _ := newProbeTestServer(t, func(cmd string) (string, int) {
			if strings.Contains(cmd, "status --porcelain=v1 -uall") {
				return " M a.go\n?? b/c.go\n", 0
			}
			if strings.Contains(cmd, "diff --numstat HEAD --") {
				return "3\t1\ta.go\n", 0
			}
			return "", 1
		})
		files, err := srv.probeGitChanges("dev1", "/repo")
		if err != nil {
			t.Fatalf("probeGitChanges: %v", err)
		}
		want := []agentChangeFile{
			{Path: "a.go", Status: "M", Adds: 3, Dels: 1},
			{Path: "b/c.go", Status: "??"},
		}
		if !reflect.DeepEqual(files, want) {
			t.Fatalf("files = %+v, want %+v", files, want)
		}
	})

	t.Run("numstat failure is soft", func(t *testing.T) {
		srv, _ := newProbeTestServer(t, func(cmd string) (string, int) {
			if strings.Contains(cmd, "status --porcelain=v1 -uall") {
				return " M a.go\n", 0
			}
			return "", 128 // numstat probe fails (e.g. no HEAD)
		})
		files, err := srv.probeGitChanges("dev1", "/repo")
		if err != nil {
			t.Fatalf("probeGitChanges: %v", err)
		}
		if len(files) != 1 || files[0].Adds != 0 || files[0].Dels != 0 {
			t.Fatalf("files = %+v", files)
		}
	})

	t.Run("non-zero exit is not a repo", func(t *testing.T) {
		srv, _ := newProbeTestServer(t, func(string) (string, int) { return "", 128 })
		if _, err := srv.probeGitChanges("dev1", "/repo"); !errors.Is(err, errGitNotRepo) {
			t.Fatalf("err = %v, want errGitNotRepo", err)
		}
	})

	t.Run("offline device errors", func(t *testing.T) {
		srv, _ := newProbeTestServer(t, func(string) (string, int) { return "", 0 })
		if _, err := srv.probeGitChanges("no-such-device", "/repo"); err == nil {
			t.Fatal("expected offline error")
		}
	})

	t.Run("empty cwd errors", func(t *testing.T) {
		srv, _ := newProbeTestServer(t, func(string) (string, int) { return "", 0 })
		if _, err := srv.probeGitChanges("dev1", ""); err == nil {
			t.Fatal("expected cwd error")
		}
	})
}

func TestProbeGitDiff(t *testing.T) {
	const diffText = "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ +line\n"

	t.Run("HEAD diff output", func(t *testing.T) {
		srv, tr := newProbeTestServer(t, func(cmd string) (string, int) {
			if strings.Contains(cmd, "diff HEAD --") {
				return diffText, 0
			}
			return "", 1
		})
		diff, err := srv.probeGitDiff("dev1", "/repo", "a.go")
		if err != nil || diff != diffText {
			t.Fatalf("diff = %q, err = %v", diff, err)
		}
		// Non-empty diff: no fallback, no ls-files round-trip.
		if cmds := tr.commands(); len(cmds) != 1 {
			t.Fatalf("commands = %v", cmds)
		}
	})

	t.Run("HEAD missing falls back to plain diff", func(t *testing.T) {
		srv, tr := newProbeTestServer(t, func(cmd string) (string, int) {
			switch {
			case strings.Contains(cmd, "diff HEAD --"):
				return "", 128 // ambiguous argument 'HEAD': no commits yet
			case strings.Contains(cmd, " diff -- "):
				return diffText, 0
			}
			return "", 1
		})
		diff, err := srv.probeGitDiff("dev1", "/repo", "a.go")
		if err != nil || diff != diffText {
			t.Fatalf("diff = %q, err = %v", diff, err)
		}
		cmds := tr.commands()
		if len(cmds) != 2 || !strings.Contains(cmds[0], "diff HEAD --") || !strings.Contains(cmds[1], " diff -- ") {
			t.Fatalf("fallback chain = %v", cmds)
		}
	})

	t.Run("untracked file", func(t *testing.T) {
		srv, _ := newProbeTestServer(t, func(cmd string) (string, int) {
			if strings.Contains(cmd, "ls-files") {
				return "", 1 // not in the index
			}
			return "", 0 // empty diff
		})
		if _, err := srv.probeGitDiff("dev1", "/repo", "new.go"); !errors.Is(err, errGitUntracked) {
			t.Fatalf("err = %v, want errGitUntracked", err)
		}
	})

	t.Run("unchanged tracked file is an empty diff", func(t *testing.T) {
		srv, _ := newProbeTestServer(t, func(cmd string) (string, int) {
			if strings.Contains(cmd, "ls-files") {
				return "a.go\n", 0
			}
			return "", 0
		})
		diff, err := srv.probeGitDiff("dev1", "/repo", "a.go")
		if err != nil || diff != "" {
			t.Fatalf("diff = %q, err = %v", diff, err)
		}
	})

	t.Run("both diffs failing is not a repo", func(t *testing.T) {
		srv, _ := newProbeTestServer(t, func(string) (string, int) { return "", 128 })
		if _, err := srv.probeGitDiff("dev1", "/repo", "a.go"); !errors.Is(err, errGitNotRepo) {
			t.Fatalf("err = %v, want errGitNotRepo", err)
		}
	})
}

// --- end-to-end WS: get_changes / get_diff + automatic changes pushes ---

// rawWSRecorder records decoded frames as generic maps (the changes/diff
// replies are not agentMsg-shaped).
type rawWSRecorder struct {
	gws.BuiltinEventHandler
	msgs chan map[string]json.RawMessage
}

func (c *rawWSRecorder) OnMessage(_ *gws.Conn, message *gws.Message) {
	defer message.Close()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(message.Bytes(), &m); err == nil {
		c.msgs <- m
	}
}

func waitRawMsg(t *testing.T, ch chan map[string]json.RawMessage, what string, pred func(map[string]json.RawMessage) bool) map[string]json.RawMessage {
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

func rawString(t *testing.T, m map[string]json.RawMessage, key string) string {
	t.Helper()
	raw, ok := m[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("%s not a string: %s", key, raw)
	}
	return s
}

func isOp(op string) func(map[string]json.RawMessage) bool {
	return func(m map[string]json.RawMessage) bool {
		var s string
		if err := json.Unmarshal(m["op"], &s); err != nil {
			return false
		}
		return s == op
	}
}

func TestAgentWSGetChangesAndDiff(t *testing.T) {
	const diffText = "diff --git a/internal/x.go b/internal/x.go\n--- a/internal/x.go\n+++ b/internal/x.go\n@@ +change\n"
	respond := func(cmd string) (string, int) {
		switch {
		case cmd == "pwd":
			return "/repo\n", 0
		case strings.Contains(cmd, "branch --show-current"):
			return "main\n", 0
		case strings.Contains(cmd, "status --porcelain=v1 -uall"):
			return " M internal/x.go\n?? new file.go\n", 0
		case strings.Contains(cmd, "diff HEAD -- 'internal/x.go'"):
			return diffText, 0
		case strings.Contains(cmd, "diff HEAD -- 'new file.go'"):
			return "", 0 // untracked: no diff output
		case strings.Contains(cmd, "ls-files"):
			return "", 1
		}
		return "", 1
	}
	srv, _ := newProbeTestServer(t, respond)

	httpSrv := httptest.NewServer(http.HandlerFunc(srv.HandleAgentWS))
	defer httpSrv.Close()

	rec := &rawWSRecorder{msgs: make(chan map[string]json.RawMessage, 128)}
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

	waitRawMsg(t, rec.msgs, "auth_ok", isOp("auth_ok"))

	// auth_ok triggers a meta probe which auto-pushes the changes snapshot
	// (branch "main", two files) for the current thread's cwd.
	pushed := waitRawMsg(t, rec.msgs, "auto-pushed changes", func(m map[string]json.RawMessage) bool {
		if !isOp("changes")(m) {
			return false
		}
		var files []agentChangeFile
		if err := json.Unmarshal(m["files"], &files); err != nil {
			return false
		}
		return len(files) == 2
	})
	if got := rawString(t, pushed, "cwd"); got != "/repo" {
		t.Fatalf("pushed cwd = %q", got)
	}
	if got := rawString(t, pushed, "branch"); got != "main" {
		t.Fatalf("pushed branch = %q", got)
	}
	if got := rawString(t, pushed, "error"); got != "" {
		t.Fatalf("pushed error = %q", got)
	}
	var pushedFiles []agentChangeFile
	if err := json.Unmarshal(pushed["files"], &pushedFiles); err != nil {
		t.Fatalf("pushed files: %v", err)
	}
	wantFiles := []agentChangeFile{
		{Path: "internal/x.go", Status: "M"},
		{Path: "new file.go", Status: "??"},
	}
	if !reflect.DeepEqual(pushedFiles, wantFiles) {
		t.Fatalf("pushed files = %+v, want %+v", pushedFiles, wantFiles)
	}

	// get_changes round-trip.
	send(`{"op":"get_changes"}`)
	reply := waitRawMsg(t, rec.msgs, "changes reply", func(m map[string]json.RawMessage) bool {
		return isOp("changes")(m)
	})
	if got := rawString(t, reply, "cwd"); got != "/repo" {
		t.Fatalf("reply cwd = %q", got)
	}
	var replyFiles []agentChangeFile
	if err := json.Unmarshal(reply["files"], &replyFiles); err != nil {
		t.Fatalf("reply files: %v", err)
	}
	if !reflect.DeepEqual(replyFiles, wantFiles) {
		t.Fatalf("reply files = %+v, want %+v", replyFiles, wantFiles)
	}

	// get_diff on a modified file returns the unified diff.
	send(`{"op":"get_diff","path":"internal/x.go"}`)
	diffReply := waitRawMsg(t, rec.msgs, "diff reply", func(m map[string]json.RawMessage) bool {
		return isOp("diff")(m) && rawString(t, m, "path") == "internal/x.go"
	})
	if got := rawString(t, diffReply, "diff"); got != diffText {
		t.Fatalf("diff = %q", got)
	}
	if got := rawString(t, diffReply, "error"); got != "" {
		t.Fatalf("diff error = %q", got)
	}

	// get_diff on an untracked file reports "untracked".
	send(`{"op":"get_diff","path":"new file.go"}`)
	untrackedReply := waitRawMsg(t, rec.msgs, "untracked diff reply", func(m map[string]json.RawMessage) bool {
		return isOp("diff")(m) && rawString(t, m, "path") == "new file.go"
	})
	if got := rawString(t, untrackedReply, "error"); got != "untracked" {
		t.Fatalf("untracked error = %q", got)
	}
}

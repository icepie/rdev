package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lxzan/gws"
	"rdev/internal/agent"
)

func TestAgentRestoredPreviewWaitsForDefaultCwd(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var probes atomic.Int32
	srv, _ := newProbeTestServer(t, func(cmd string) (string, int) {
		if cmd == "pwd" {
			if probes.Add(1) == 1 {
				close(entered)
			}
			<-release
			return "/repo\n", 0
		}
		if strings.Contains(cmd, "head -c") {
			return "restored file", 0
		}
		return "", 0
	})
	srv.agentStore().save("dev1", "restored", "p/m1", []agent.Message{{Role: "user", Content: "previous turn"}})
	server := httptest.NewServer(http.HandlerFunc(srv.HandleAgentWS))
	defer server.Close()
	rec := &rawWSRecorder{msgs: make(chan map[string]json.RawMessage, 128)}
	socket, _, err := gws.NewClient(rec, &gws.ClientOption{Addr: "ws" + strings.TrimPrefix(server.URL, "http") + "/agent?device=dev1", HandshakeTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.WriteClose(1000, nil)
	go socket.ReadLoop()
	auth := waitRawMsg(t, rec.msgs, "auth", isOp("auth_ok"))
	if rawString(t, auth, "thread") != "restored" {
		t.Fatal("persisted thread not restored")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("cwd probe did not start")
	}
	sendAgentOp(t, socket, `{"op":"file_read","id":"cold-a","path":"a.txt"}`)
	sendAgentOp(t, socket, `{"op":"file_read","id":"cold-b","path":"b.txt"}`)
	// A subsequent dispatcher reply proves reads did not block the socket.
	sendAgentOp(t, socket, `{"op":"ping"}`)
	waitRawMsg(t, rec.msgs, "responsive socket", isOp("pong"))
	unblock()
	seen := map[string]bool{}
	for len(seen) < 2 {
		reply := waitRawMsg(t, rec.msgs, "cold file", isOp("file_content"))
		if rawString(t, reply, "message") != "" || rawString(t, reply, "content") != "restored file" {
			t.Fatalf("preview failed: %s", reply)
		}
		seen[rawString(t, reply, "id")] = true
	}
	if !seen["cold-a"] || !seen["cold-b"] {
		t.Fatal(seen)
	}
	if probes.Load() != 1 {
		t.Fatalf("concurrent requests ran %d cwd probes", probes.Load())
	}
}

func TestAgentDefaultCwdRetriesFailedCommand(t *testing.T) {
	var calls atomic.Int32
	srv, _ := newProbeTestServer(t, func(cmd string) (string, int) {
		if calls.Add(1) == 1 {
			return "permission denied\n", 1
		}
		return "/reconnected\n", 0
	})
	entry := &agentSessionEntry{deviceID: "dev1"}
	if got := srv.awaitAgentDefaultCwd(entry); got != "" {
		t.Fatalf("cached failed command: %q", got)
	}
	if got := srv.awaitAgentDefaultCwd(entry); got != "/reconnected" {
		t.Fatalf("retry cwd: %q", got)
	}
	if got := srv.awaitAgentDefaultCwd(entry); got != "/reconnected" || calls.Load() != 2 {
		t.Fatalf("cache: %q, calls %d", got, calls.Load())
	}
}

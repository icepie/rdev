package client

import (
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"

	"rdev/internal/protocol"
)

func TestExecSessionKeepsStdinForRegularCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell redirection")
	}
	outputPath := t.TempDir() + "/stdin-output.txt"
	client := NewClient("", "test", "", "/bin/sh")
	sessionID := "stdin-regular-command"
	sess, err := client.startShellExecSession(&protocol.Message{
		SessionID: sessionID,
		Command:   "cat > " + strconv.Quote(outputPath),
	})
	if err != nil {
		t.Fatalf("startShellExecSession: %v", err)
	}
	if sess.stdinPipe == nil {
		t.Fatalf("regular exec command stdinPipe is nil")
	}
	client.mu.Lock()
	client.sessions[sessionID] = sess
	client.mu.Unlock()

	client.handleBinData(sessionID, []byte("hello from stdin\n"))
	client.handleStdinClose(&protocol.Message{SessionID: sessionID})

	select {
	case <-sess.done:
	case <-time.After(5 * time.Second):
		sess.close()
		t.Fatalf("session did not finish after stdin close")
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(data) != "hello from stdin\n" {
		t.Fatalf("output = %q, want stdin payload", string(data))
	}
}

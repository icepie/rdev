package client

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"rdev/internal/protocol"
)

func TestExecSessionKeepsStdinForRsyncServerCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell script")
	}
	tmpDir := t.TempDir()
	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	fakeRsync := filepath.Join(binDir, "rsync")
	if err := os.WriteFile(fakeRsync, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$RDEV_RSYNC_ARGS\"\ncat > \"$RDEV_RSYNC_STDIN\"\n"), 0755); err != nil {
		t.Fatalf("write fake rsync: %v", err)
	}
	argsPath := filepath.Join(tmpDir, "rsync-args.txt")
	stdinPath := filepath.Join(tmpDir, "rsync-stdin.bin")

	client := NewClient("", "test", "", "/bin/sh")
	sessionID := "stdin-rsync-server-command"
	sess, err := client.startShellExecSession(&protocol.Message{
		SessionID: sessionID,
		Command:   "rsync --server -logDtpre.iLsfxCIvu . " + strconv.Quote(filepath.Join(tmpDir, "dst")) + "/",
		Env: []string{
			"PATH=" + binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
			"RDEV_RSYNC_ARGS=" + argsPath,
			"RDEV_RSYNC_STDIN=" + stdinPath,
		},
	})
	if err != nil {
		t.Fatalf("startShellExecSession: %v", err)
	}
	if sess.stdinPipe == nil {
		t.Fatalf("rsync server command stdinPipe is nil")
	}
	client.mu.Lock()
	client.sessions[sessionID] = sess
	client.mu.Unlock()

	payload := []byte("rsync protocol payload\x00\x01\n")
	client.handleBinData(sessionID, payload)
	client.handleStdinClose(&protocol.Message{SessionID: sessionID})

	select {
	case <-sess.done:
	case <-time.After(5 * time.Second):
		sess.close()
		t.Fatalf("rsync server command did not finish after stdin close")
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	if !strings.Contains(string(args), "--server\n") {
		t.Fatalf("args = %q, want --server", string(args))
	}
	data, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatalf("read stdin: %v", err)
	}
	if string(data) != string(payload) {
		t.Fatalf("stdin payload = %q, want %q", string(data), string(payload))
	}
}

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

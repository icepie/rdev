package client

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
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

func TestSessionInputOrdersEOFBehindQueuedData(t *testing.T) {
	queue := newSessionInput(1024)
	if full, stopped := queue.push([]byte("first")); full || stopped {
		t.Fatalf("push rejected: full=%v stopped=%v", full, stopped)
	}
	queue.push([]byte("second"))
	queue.finishEOF()
	for _, want := range []string{"first", "second"} {
		data, eof, stopped := queue.next()
		if stopped || eof {
			t.Fatalf("next before drain: eof=%v stopped=%v", eof, stopped)
		}
		if string(data) != want {
			t.Fatalf("data = %q, want %q", data, want)
		}
	}
	if data, eof, stopped := queue.next(); data != nil || !eof || stopped {
		t.Fatalf("EOF not delivered after queued data: data=%q eof=%v stopped=%v", data, eof, stopped)
	}
}

func TestSessionInputBoundsAndStops(t *testing.T) {
	queue := newSessionInput(4)
	if full, _ := queue.push([]byte("abcd")); full {
		t.Fatal("payload within limit rejected")
	}
	if full, stopped := queue.push([]byte("e")); !full || stopped {
		t.Fatalf("overrun not reported: full=%v stopped=%v", full, stopped)
	}
	queue.stop()
	if _, _, stopped := queue.next(); !stopped {
		t.Fatal("stopped queue still yields data")
	}
	if full, stopped := queue.push([]byte("x")); full || !stopped {
		t.Fatalf("push after stop = full:%v stopped:%v", full, stopped)
	}
}

type blockingInputWriter struct {
	entered chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func newBlockingInputWriter() *blockingInputWriter {
	return &blockingInputWriter{entered: make(chan struct{}), closed: make(chan struct{})}
}

func (w *blockingInputWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.closed
	return 0, os.ErrClosed
}

func (w *blockingInputWriter) Close() error {
	select {
	case <-w.closed:
	default:
		close(w.closed)
	}
	return nil
}

// Cancelling a session closes the input handle so a write parked on a stopped
// child returns and the writer goroutine exits.
func TestSessionInputCancelUnblocksWriter(t *testing.T) {
	writer := newBlockingInputWriter()
	client := NewClient("", "test", "", "")
	sess := &clientSession{
		id:        "cancel",
		stdinPipe: writer,
		input:     newSessionInput(1 << 20),
		done:      make(chan struct{}),
	}
	client.sessions["cancel"] = sess
	go sess.runInput(client)

	client.handleBinData("cancel", []byte("payload"))
	select {
	case <-writer.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("queued input never reached the writer")
	}
	client.handleClose(&protocol.Message{SessionID: "cancel"})
	select {
	case <-sess.input.done:
	case <-time.After(5 * time.Second):
		t.Fatal("input writer did not exit after cancel")
	}
	select {
	case <-sess.done:
	default:
		t.Fatal("cancelled session stayed open")
	}
}

type stdinMessageTransport struct {
	mu   sync.Mutex
	msgs []protocol.Message
}

func (t *stdinMessageTransport) WriteJSON(data []byte) error {
	msg, err := protocol.Decode(data)
	if err != nil {
		return err
	}
	t.mu.Lock()
	t.msgs = append(t.msgs, *msg)
	t.mu.Unlock()
	return nil
}

func (t *stdinMessageTransport) WriteBinary([]byte) error { return nil }
func (t *stdinMessageTransport) WritePing([]byte) error   { return nil }
func (t *stdinMessageTransport) Close(string) error       { return nil }

func (t *stdinMessageTransport) saw(typ protocol.MessageType, sessionID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, msg := range t.msgs {
		if msg.Type == typ && msg.SessionID == sessionID {
			return true
		}
	}
	return false
}

// A child that stops reading fills its pipe and then this session's bounded
// queue. That session must end explicitly while another session keeps working.
func TestStalledSessionEndsWithoutBlockingOthers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	client := NewClient("", "test", "", "/bin/sh")
	transport := &stdinMessageTransport{}
	client.mu.Lock()
	client.transport = transport
	client.mu.Unlock()

	stalled, err := client.startShellExecSession(&protocol.Message{SessionID: "stalled", Command: "sleep 30"})
	if err != nil {
		t.Fatalf("start stalled session: %v", err)
	}
	client.mu.Lock()
	client.sessions["stalled"] = stalled
	client.mu.Unlock()

	chunk := bytes.Repeat([]byte("x"), 64<<10)
	for range 2*clientSessionInputBytes/len(chunk) + 1 {
		client.handleBinData("stalled", chunk)
	}
	select {
	case <-stalled.done:
	case <-time.After(10 * time.Second):
		stalled.close()
		t.Fatal("stalled session did not end after its input queue filled")
	}
	if !transport.saw(protocol.MsgClose, "stalled") {
		t.Fatal("stalled session ended without telling the server")
	}
	client.mu.Lock()
	_, retained := client.sessions["stalled"]
	client.mu.Unlock()
	if retained {
		t.Fatal("stalled session stayed registered")
	}

	outputPath := filepath.Join(t.TempDir(), "after-stall.txt")
	other, err := client.startShellExecSession(&protocol.Message{SessionID: "other", Command: "cat > " + strconv.Quote(outputPath)})
	if err != nil {
		t.Fatalf("start second session: %v", err)
	}
	client.mu.Lock()
	client.sessions["other"] = other
	client.mu.Unlock()
	client.handleBinData("other", []byte("still works\n"))
	client.handleStdinClose(&protocol.Message{SessionID: "other"})
	select {
	case <-other.done:
	case <-time.After(10 * time.Second):
		other.close()
		t.Fatal("second session blocked by the stalled one")
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read second session output: %v", err)
	}
	if string(data) != "still works\n" {
		t.Fatalf("second session output = %q", data)
	}
}

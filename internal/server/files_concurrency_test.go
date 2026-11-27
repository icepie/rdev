package server

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lxzan/gws"
	"rdev/internal/protocol"
)

func testFileSocket(send func(gws.Opcode, []byte) error) *fileSocket {
	return &fileSocket{out: newFrameWriter(fileSocketSendBytes, send, func(string) {})}
}

// A browser that stopped reading must not block the producer, and crossing the
// queue limit must close the socket instead of dropping a response.
func TestFileSocketSendQueueBoundsStalledBrowser(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseWriter := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseWriter)
	entered := make(chan struct{}, 1)
	wrote := make(chan struct{}, 4)
	failures := make(chan string, 1)
	var mu sync.Mutex
	var written []string
	writer := newFrameWriter(64, func(_ gws.Opcode, data []byte) error {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		mu.Lock()
		written = append(written, string(data))
		mu.Unlock()
		wrote <- struct{}{}
		return nil
	}, func(reason string) { failures <- reason })

	writer.push(gws.OpcodeText, []byte("first-frame"))
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("writer never started")
	}

	pushed := make(chan struct{})
	go func() {
		writer.push(gws.OpcodeText, bytes.Repeat([]byte("x"), 60))
		close(pushed)
	}()
	select {
	case <-pushed:
	case <-time.After(time.Second):
		t.Fatal("producer blocked on a stalled browser")
	}
	select {
	case reason := <-failures:
		if !strings.Contains(reason, "limit") {
			t.Fatalf("failure reason = %q", reason)
		}
	case <-time.After(time.Second):
		t.Fatal("queue overrun was not reported")
	}

	releaseWriter()
	select {
	case <-wrote:
	case <-time.After(time.Second):
		t.Fatal("in-flight frame was never written")
	}
	writer.push(gws.OpcodeText, []byte("late-frame"))

	mu.Lock()
	defer mu.Unlock()
	if len(written) != 1 || written[0] != "first-frame" {
		t.Fatalf("written = %q", written)
	}
}

func TestFileSocketWriterPreservesOrderAndStopsOnFailure(t *testing.T) {
	failures := make(chan string, 1)
	var mu sync.Mutex
	var written []string
	writer := newFrameWriter(1024, func(_ gws.Opcode, data []byte) error {
		mu.Lock()
		defer mu.Unlock()
		written = append(written, string(data))
		if len(written) == 2 {
			return errors.New("browser gone")
		}
		return nil
	}, func(reason string) { failures <- reason })

	for _, frame := range []string{"one", "two", "three"} {
		writer.push(gws.OpcodeBinary, []byte(frame))
	}
	select {
	case <-failures:
	case <-time.After(time.Second):
		t.Fatal("write failure was not reported")
	}
	writer.push(gws.OpcodeBinary, []byte("four"))
	mu.Lock()
	defer mu.Unlock()
	if len(written) != 2 || written[0] != "one" || written[1] != "two" {
		t.Fatalf("written = %q", written)
	}
}

// Enqueueing for one browser keeps advancing while another browser's writer is
// stalled: a slow file workspace cannot freeze the rest of the relay.
func TestStalledFileSocketDoesNotBlockOtherWorkspaces(t *testing.T) {
	release := make(chan struct{})
	slow := testFileSocket(func(gws.Opcode, []byte) error {
		<-release
		return nil
	})
	t.Cleanup(func() { close(release) })
	fastFrames := make(chan string, 8)
	fast := testFileSocket(func(_ gws.Opcode, data []byte) error {
		fastFrames <- string(data)
		return nil
	})

	start := time.Now()
	for range 32 {
		slow.writeText(fileMsg{Op: "list_result", RequestID: "slow"})
	}
	fast.writeText(fileMsg{Op: "upload_ready", TaskID: "fast"})
	select {
	case frame := <-fastFrames:
		if !strings.Contains(frame, "fast") {
			t.Fatalf("fast workspace frame = %q", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("fast workspace blocked behind a stalled browser")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("enqueueing took %v", elapsed)
	}
}

func TestClosingFileSocketDoesNotBlockOtherWorkspaces(t *testing.T) {
	entered, release, closed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	transport := &fakeDeviceTransport{onSend: func(msg *protocol.Message) {
		if msg.Type != protocol.MsgFileTransferCancel || msg.TaskID != "closing-task" {
			t.Errorf("unexpected cancellation: %+v", msg)
		}
		close(entered)
		<-release
	}}
	closing, other := &fileSocket{}, &fileSocket{}
	slowClient := &ClientConn{ID: "slow", Transport: transport}
	otherClient := &ClientConn{ID: "other"}
	srv := &Server{
		clients:      map[string]*ClientConn{"slow": slowClient, "other": otherClient},
		fileRequests: map[string]*fileRequestRoute{"closing-list": {socket: closing}, "other-list": {socket: other}},
		fileTasks: map[string]*fileTaskRoute{
			"closing-task": {socket: closing, deviceID: "slow", client: slowClient},
			"other-task":   {socket: other, deviceID: "other", client: otherClient},
		},
	}
	go func() { srv.closeFileSocket(closing); close(closed) }()
	defer func() {
		close(release)
		select {
		case <-closed:
		case <-time.After(time.Second):
			t.Error("cleanup did not finish")
		}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("cancel did not reach transport")
	}
	checked := make(chan bool, 1)
	go func() {
		srv.registerBoundFileTask("new-task", other, otherClient, false)
		srv.fileMu.RLock()
		valid := srv.fileTasks["closing-task"] == nil && srv.fileRequests["closing-list"] == nil &&
			srv.fileTasks["other-task"].socket == other && srv.fileRequests["other-list"].socket == other &&
			srv.fileTasks["new-task"].socket == other
		srv.fileMu.RUnlock()
		checked <- valid
	}()
	select {
	case valid := <-checked:
		if !valid {
			t.Fatal("closing removed another workspace or retained its own routes")
		}
	case <-time.After(time.Second):
		t.Fatal("slow device blocked unrelated file routing")
	}
}

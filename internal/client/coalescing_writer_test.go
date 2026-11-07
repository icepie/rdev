package client

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"rdev/internal/protocol"
)

type recordingOutputTransport struct {
	mu      sync.Mutex
	frames  [][]byte
	entered chan struct{}
	release chan struct{}
}

func (t *recordingOutputTransport) WriteJSON([]byte) error { return nil }
func (t *recordingOutputTransport) WritePing([]byte) error { return nil }
func (t *recordingOutputTransport) Close(string) error     { return nil }
func (t *recordingOutputTransport) WriteBinary(b []byte) error {
	if t.entered != nil {
		close(t.entered)
		<-t.release
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.frames = append(t.frames, bytes.Clone(b))
	return nil
}

func TestCoalescingFinalFlushWaitsForPendingSend(t *testing.T) {
	transport := &recordingOutputTransport{entered: make(chan struct{}), release: make(chan struct{})}
	c := NewClient("", "test", "", "")
	c.transport = transport
	w := newCoalescingWriterWithInterval(c, "one", protocol.BinData, time.Hour)
	w.Write([]byte("last output"))
	go w.flush()
	<-transport.entered
	flushed := make(chan struct{})
	go func() { w.flush(); close(flushed) }()
	select {
	case <-flushed:
		t.Error("final flush returned while output was still in flight")
	case <-time.After(20 * time.Millisecond):
	}
	close(transport.release)
	select {
	case <-flushed:
	case <-time.After(time.Second):
		t.Fatal("flush did not finish")
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if len(transport.frames) != 1 {
		t.Fatalf("frames = %d", len(transport.frames))
	}
	if !bytes.HasSuffix(transport.frames[0], []byte("last output")) {
		t.Fatal("missing output")
	}
}

func TestCoalescingPreservesSmallThenLargeOutput(t *testing.T) {
	transport := &recordingOutputTransport{}
	c := NewClient("", "test", "", "")
	c.transport = transport
	w := newCoalescingWriterWithInterval(c, "one", protocol.BinData, time.Hour)
	w.Write([]byte("prefix"))
	large := bytes.Repeat([]byte("x"), 8192)
	w.Write(large)
	w.Write([]byte("suffix"))
	w.flush()
	if len(transport.frames) != 3 {
		t.Fatalf("frames = %d", len(transport.frames))
	}
	for i, want := range [][]byte{[]byte("prefix"), large, []byte("suffix")} {
		if !bytes.HasSuffix(transport.frames[i], want) {
			t.Fatalf("frame %d reordered or corrupted", i)
		}
	}
}

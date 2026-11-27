package server

import (
	"bytes"
	"encoding/binary"
	"net"
	"sync"
	"testing"
	"time"
)

// A substream whose browser stopped reading must fail alone: the shared device
// read loop keeps running and other substreams still receive data.
func TestGPUDesktopSlowStreamFailsWithoutStallingOthers(t *testing.T) {
	tunnel := newGPUDesktopTunnel("dev", nil)
	slow, err := tunnel.openStream()
	if err != nil {
		t.Fatal(err)
	}
	fast, err := tunnel.openStream()
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a websocket proxy goroutine parked on a browser that stopped
	// reading: it only observes the queue once the substream ends.
	exited := make(chan struct{})
	go func() {
		<-slow.closed
		for range slow.data {
		}
		close(exited)
	}()

	// 1 MiB frames hit the byte cap (8 MiB) before the frame-count cap.
	chunk := make([]byte, 1<<20)
	for range gpuDesktopStreamQueueBytes/len(chunk) + 1 {
		tunnel.dispatchData(slow.id, chunk)
	}

	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("slow proxy goroutine did not exit after its queue overflowed")
	}
	tunnel.mu.Lock()
	_, registered := tunnel.streams[slow.id]
	tunnel.mu.Unlock()
	if registered {
		t.Fatal("failed substream remained registered")
	}

	tunnel.dispatchData(fast.id, []byte("fast-tail"))
	select {
	case data := <-fast.data:
		if string(data) != "fast-tail" {
			t.Fatalf("fast substream payload = %q", data)
		}
	case <-time.After(time.Second):
		t.Fatal("fast substream starved by the slow one")
	}

	// The device is told to stop producing for the failed substream only.
	closeFrame := waitGPUDesktopFrame(t, tunnel.send, gpuDesktopTunnelFrameClose, slow.id)
	if len(closeFrame) != 9 {
		t.Fatalf("close frame length = %d", len(closeFrame))
	}
}

func waitGPUDesktopFrame(t *testing.T, frames <-chan []byte, frameType byte, streamID uint64) []byte {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case frame := <-frames:
			if frame[0] == frameType && binary.BigEndian.Uint64(frame[1:9]) == streamID {
				return frame
			}
		case <-deadline:
			t.Fatalf("missing frame type=%d stream=%d", frameType, streamID)
			return nil
		}
	}
}

func TestGPUDesktopStreamPreservesChunkOrder(t *testing.T) {
	tunnel := newGPUDesktopTunnel("dev", nil)
	stream, err := tunnel.openStream()
	if err != nil {
		t.Fatal(err)
	}
	chunks := [][]byte{[]byte("first"), []byte("second"), []byte("third")}
	for _, chunk := range chunks {
		tunnel.dispatchData(stream.id, chunk)
	}
	for _, want := range chunks {
		data, ok := <-stream.data
		if !ok {
			t.Fatal("substream closed unexpectedly")
		}
		stream.release(data)
		if !bytes.Equal(data, want) {
			t.Fatalf("chunk = %q, want %q", data, want)
		}
	}
}

type writeDeadlineConn struct {
	net.Conn
	mu       sync.Mutex
	deadline time.Time
	writes   [][]byte
}

func (c *writeDeadlineConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadline = t
	c.mu.Unlock()
	return nil
}

func (c *writeDeadlineConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.writes = append(c.writes, bytes.Clone(p))
	c.mu.Unlock()
	return len(p), nil
}

// Browser writes must carry their own deadline so a stalled tab cannot pin the
// substream proxy goroutine.
func TestGPUDesktopBrowserWriteSetsDeadline(t *testing.T) {
	conn := &writeDeadlineConn{}
	start := time.Now()
	if err := writeGPUDesktopBrowser(conn, []byte("frame")); err != nil {
		t.Fatal(err)
	}
	conn.mu.Lock()
	deadline, writes := conn.deadline, conn.writes
	conn.mu.Unlock()
	if deadline.IsZero() {
		t.Fatal("browser write had no deadline")
	}
	if deadline.Before(start) || deadline.After(start.Add(gpuDesktopBrowserWriteWait+time.Second)) {
		t.Fatalf("deadline %v not derived from the write wait", deadline)
	}
	if len(writes) != 1 || string(writes[0]) != "frame" {
		t.Fatalf("writes = %q", writes)
	}
}

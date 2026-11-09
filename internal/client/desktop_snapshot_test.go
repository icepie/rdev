package client

import (
	"image"
	"sync/atomic"
	"testing"
	"time"
)

type countedCapturer struct{ calls atomic.Int32 }

func (c *countedCapturer) Bounds() image.Rectangle { return image.Rect(0, 0, 40, 20) }
func (c *countedCapturer) Close() error            { return nil }
func (c *countedCapturer) Capture() (image.Image, error) {
	c.calls.Add(1)
	return image.NewRGBA(c.Bounds()), nil
}

func TestDesktopSnapshotWorkerOnlyCapturesOnRequest(t *testing.T) {
	client := NewClient("", "test", "", "")
	transport := &recordingOutputTransport{}
	client.transport = transport
	capture := &countedCapturer{}
	session := &desktopSession{stop: make(chan struct{}), snapshot: make(chan struct{}, 1)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		client.runDesktopSnapshots("snapshot", session, capture, image.Pt(20, 10), 80)
	}()
	defer func() { close(session.stop); <-done }()
	// Longer than the former 2 FPS interval: idle sessions must do no work.
	time.Sleep(550 * time.Millisecond)
	if capture.calls.Load() != 0 {
		t.Fatal("captured without a request")
	}
	for i := 1; i <= 2; i++ {
		session.snapshot <- struct{}{}
		deadline := time.Now().Add(time.Second)
		for {
			transport.mu.Lock()
			n := len(transport.frames)
			transport.mu.Unlock()
			if n == i {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("snapshot did not return")
			}
			time.Sleep(time.Millisecond)
		}
		if capture.calls.Load() != int32(i) {
			t.Fatal("extra captures")
		}
	}
}

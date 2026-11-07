//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package client

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"rdev/internal/protocol"
)

func TestExecCancelStopsChildrenAndIsolatesConcurrentSession(t *testing.T) {
	c := NewClient("", "test", "", "/bin/sh")
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	leaked := filepath.Join(dir, "leaked")
	canceled, err := c.startShellExecSession(&protocol.Message{
		SessionID: "cancel", Command: "(touch " + strconv.Quote(ready) + "; sleep 0.4; touch " + strconv.Quote(leaked) + ") & wait",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer canceled.close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	kept := filepath.Join(dir, "kept")
	other, err := c.startShellExecSession(&protocol.Message{
		SessionID: "keep", Command: "sleep 0.6; touch " + strconv.Quote(kept),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer other.close()
	canceled.close()
	canceled.close() // Disconnect and explicit cancellation may both close it.
	select {
	case <-other.done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent session did not finish")
	}
	if _, err := os.Stat(kept); err != nil {
		t.Fatalf("concurrent session interrupted: %v", err)
	}
	if _, err := os.Stat(leaked); !os.IsNotExist(err) {
		t.Fatalf("canceled child continued executing: %v", err)
	}
}

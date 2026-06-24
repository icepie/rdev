package client

import (
	"testing"
	"time"
)

func TestReconnectBackoffJitterAndCap(t *testing.T) {
	backoff := newReconnectBackoff(time.Second, 4*time.Second)

	for i, wantBase := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second} {
		got := backoff.Next()
		min := wantBase - wantBase/5
		max := wantBase + wantBase/5
		if got < min || got > max {
			t.Fatalf("attempt %d delay = %s, want between %s and %s", i, got, min, max)
		}
	}
}

func TestReconnectBackoffReset(t *testing.T) {
	backoff := newReconnectBackoff(time.Second, 30*time.Second)
	_ = backoff.Next()
	_ = backoff.Next()

	backoff.Reset()
	got := backoff.Next()
	if got < 800*time.Millisecond || got > 1200*time.Millisecond {
		t.Fatalf("delay after reset = %s, want around 1s", got)
	}
}

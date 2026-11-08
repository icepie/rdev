package server

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestJobUpdatesCoalesceDuringSlowPublicationAndRestartWhenIdle(t *testing.T) {
	var workers sync.WaitGroup
	var launches, publications atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer func() { unblock(); workers.Wait() }()
	updates := &agentJobUpdates{
		launch: func(run func()) { launches.Add(1); workers.Add(1); go func() { defer workers.Done(); run() }() },
		publish: func() {
			if publications.Add(1) == 1 {
				close(entered)
				<-release
			}
		},
	}
	updates.Notify()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("publication did not start")
	}
	var senders sync.WaitGroup
	for range 1000 {
		senders.Add(1)
		go func() { defer senders.Done(); updates.Notify() }()
	}
	senders.Wait()
	if launches.Load() != 1 || publications.Load() != 1 {
		t.Fatal("slow publication spawned more workers")
	}
	unblock()
	workers.Wait()
	if publications.Load() != 2 {
		t.Fatalf("coalesced publications=%d, want 2", publications.Load())
	}
	updates.Notify()
	workers.Wait()
	if launches.Load() != 2 || publications.Load() != 3 {
		t.Fatal("idle queue did not restart")
	}
}

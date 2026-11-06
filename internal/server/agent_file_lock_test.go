package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestAgentFileMutationsSerializeAliases(t *testing.T) {
	var locks agentFileMutations
	var wg sync.WaitGroup
	value := 0
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			file := `C:\Work\file.txt`
			if i%2 == 0 {
				file = "c:/work/./file.txt"
			}
			release, err := locks.Acquire(context.Background(), "device", "windows", file)
			if err != nil {
				t.Error(err)
				return
			}
			defer release()
			value++
		}(i)
	}
	wg.Wait()
	if value != 32 {
		t.Fatalf("lost updates: %d", value)
	}
	if len(locks.paths) != 0 {
		t.Fatal("idle lock leaked")
	}
}

func TestAgentFileMutationsIsolationAndCancellation(t *testing.T) {
	var locks agentFileMutations
	release, err := locks.Acquire(context.Background(), "a", "linux", "/file")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, key := range [][2]string{{"a", "/other"}, {"b", "/file"}} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		unlock, err := locks.Acquire(ctx, key[0], "linux", key[1])
		cancel()
		if err != nil {
			t.Fatalf("unrelated file blocked: %v", err)
		}
		unlock()
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		unlock, err := locks.Acquire(ctx, "a", "linux", "/./file")
		if unlock != nil {
			unlock()
		}
		result <- err
	}()
	waitFor(t, "waiter registered", func() bool {
		locks.mu.Lock()
		defer locks.mu.Unlock()
		return locks.paths[agentMutationKey("a", "linux", "/file")].refs == 2
	})
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("wait: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled waiter blocked")
	}
	release()
	release() // cleanup is idempotent
	locks.mu.Lock()
	defer locks.mu.Unlock()
	if len(locks.paths) != 0 {
		t.Fatal("canceled waiter leaked registry entry")
	}
}

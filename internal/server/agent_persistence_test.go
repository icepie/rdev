package server

import (
	"context"
	"rdev/internal/agent"
	"sync"
	"testing"
	"time"
)

func TestSlowHistorySaveDoesNotHoldGlobalSessionLock(t *testing.T) {
	srv := newPrefsTestServer(t)
	entry, err := srv.agentSessionFor(agentServerDeviceID, &ClientConn{ID: agentServerDeviceID})
	if err != nil {
		t.Fatal(err)
	}
	th := entry.threads[entry.current]
	th.session.SetHistory([]agent.Message{{Role: "user", Content: "one turn"}})
	store := srv.agentStore()
	store.mu.Lock()
	var once sync.Once
	unblock := func() { once.Do(store.mu.Unlock) }
	defer unblock()
	// /compact with one short turn fails locally without a provider call.
	srv.startAgentTurn(entry, th, "/compact", nil)
	waitFor(t, "turn reached persistence", func() bool {
		if th.persistMu.TryLock() {
			th.persistMu.Unlock()
			return false
		}
		return true
	})
	acquired := make(chan struct{})
	go func() { srv.agentSessionsMu.Lock(); srv.agentSessionsMu.Unlock(); close(acquired) }()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("slow disk blocked global session lock")
	}
	unblock()
	waitFor(t, "turn completed", func() bool { srv.agentSessionsMu.Lock(); defer srv.agentSessionsMu.Unlock(); return !th.running })
}

func TestTurnSnapshotSurvivesSessionReplacement(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	original := &agent.Session{}
	th := &agentThread{session: original, epoch: 1, model: "old", turnCtx: ctx, cancel: cancel}
	turn := snapshotAgentTurnLocked(th)
	th.session = &agent.Session{}
	th.epoch++
	th.model = "new"
	if turn.session != original || turn.epoch != 1 || turn.model != "old" || turn.ctx != ctx {
		t.Fatal("running turn changed with thread replacement")
	}
}

func TestResetBeforeTurnGoroutineStartsCannotRestoreHistory(t *testing.T) {
	srv := newPrefsTestServer(t)
	entry, err := srv.agentSessionFor(agentServerDeviceID, &ClientConn{ID: agentServerDeviceID})
	if err != nil {
		t.Fatal(err)
	}
	th := entry.threads[entry.current]
	th.session.SetHistory([]agent.Message{{Role: "user", Content: "old history"}})
	srv.agentSessionsMu.Lock()
	srv.startAgentTurnLocked(entry, th, "/compact", nil)
	turn := snapshotAgentTurnLocked(th)
	srv.agentSessionsMu.Unlock()
	(&agentWSHandler{srv: srv}).handleReset(&agentConn{entry: entry, current: th.id, deviceID: entry.deviceID}, th.id)
	srv.runAgentTurn(entry, th, turn, "/compact", nil)
	if len(th.session.History()) != 0 || len(th.live.Snapshot()) != 0 {
		t.Fatal("old turn restored reset history")
	}
	if th.running || th.cancel != nil || th.turnCtx != nil {
		t.Fatal("reset before admission left the thread permanently busy")
	}
	history, _, _, _ := srv.agentStore().load(entry.deviceID, th.id)
	if len(history) != 0 {
		t.Fatal("old turn resurrected history on disk")
	}
}

func TestConcurrentPreferenceWritesPreserveCurrentSettings(t *testing.T) {
	srv := newPrefsTestServer(t)
	entry, err := srv.agentSessionFor(agentServerDeviceID, &ClientConn{ID: agentServerDeviceID})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); srv.writeAgentPrefs(entry) }()
	}
	wg.Wait()
	saved := srv.loadAgentPrefs(entry.deviceID)
	if saved.CurrentThread != entry.current || saved.Model != entry.model {
		t.Fatalf("corrupt preferences: %+v", saved)
	}
}

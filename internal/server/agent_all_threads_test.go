package server

import (
	"testing"

	"rdev/internal/agent"
)

// collectAllThreads merges live in-memory entries (with running flags and
// the connected OS) with devices that only exist on disk (offline, OS from
// persisted prefs).
func TestCollectAllThreadsMergesLiveAndDisk(t *testing.T) {
	srv := newPrefsTestServer(t)
	store := srv.agentStore()
	if store == nil {
		t.Fatal("no session store")
	}

	// Disk-only device: one persisted thread, OS known from prefs.
	store.save("dev2", "t_x", "p/m1", []agent.Message{{Role: "user", Content: "hello from dev2"}})
	if err := srv.saveAgentPrefs("dev2", agentPrefs{OS: "windows"}); err != nil {
		t.Fatalf("saveAgentPrefs: %v", err)
	}

	// Live device: connected client with a running thread.
	client := &ClientConn{ID: "dev1", OS: "darwin"}
	entry, err := srv.agentSessionFor("dev1", client)
	if err != nil {
		t.Fatalf("agentSessionFor: %v", err)
	}
	srv.agentSessionsMu.Lock()
	th := entry.threads[entry.current]
	th.running = true
	th.workingTool = "shell"
	srv.agentSessionsMu.Unlock()

	out := srv.collectAllThreads()
	if len(out) != 2 {
		t.Fatalf("devices = %d, want 2: %+v", len(out), out)
	}
	byDevice := map[string]agentDeviceThreads{}
	for _, d := range out {
		byDevice[d.Device] = d
	}

	live, ok := byDevice["dev1"]
	if !ok {
		t.Fatal("dev1 missing")
	}
	if !live.Online {
		t.Error("dev1 should be online (client attached)")
	}
	if live.OS != "darwin" {
		t.Errorf("dev1 os = %q, want darwin", live.OS)
	}
	if len(live.Threads) != 1 || !live.Threads[0].Running || live.Threads[0].Tool != "shell" {
		t.Errorf("dev1 threads = %+v, want one running shell thread", live.Threads)
	}

	disk, ok := byDevice["dev2"]
	if !ok {
		t.Fatal("dev2 missing")
	}
	if disk.Online {
		t.Error("dev2 should be offline")
	}
	if disk.OS != "windows" {
		t.Errorf("dev2 os = %q, want windows (from prefs)", disk.OS)
	}
	if len(disk.Threads) != 1 || disk.Threads[0].ID != "t_x" {
		t.Fatalf("dev2 threads = %+v, want t_x", disk.Threads)
	}
	if disk.Threads[0].Running {
		t.Error("disk thread must not report running")
	}
	if disk.Threads[0].Title != "hello from dev2" {
		t.Errorf("dev2 title = %q", disk.Threads[0].Title)
	}
}

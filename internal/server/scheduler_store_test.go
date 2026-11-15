package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScheduleCheckpointReusesRetainedOutput(t *testing.T) {
	m := schedulerFixture(t)
	for i := range 100 {
		id := fmt.Sprint(i)
		m.state.Runs[id] = ScheduleRun{ID: id, ScheduleID: id, State: "succeeded", ScheduledAt: m.now(), Output: strings.Repeat("x", scheduleLogLimit)}
	}
	if err := m.saveLocked(); err != nil {
		t.Fatal(err)
	}
	retained := m.store.manifest.Runs["1"]
	r := m.state.Runs["0"]
	r.Reason = "changed metadata"
	m.state.Runs["0"] = r
	if err := m.saveLocked(); err != nil {
		t.Fatal(err)
	}
	if m.store.manifest.Runs["1"] != retained {
		t.Fatal("rewrote unchanged retained output")
	}
	if m.store.lastCommitBytes >= 2*scheduleLogLimit {
		t.Fatalf("incremental commit wrote %d bytes", m.store.lastCommitBytes)
	}
	reopened := schedulerFixture(t)
	reopened.path = m.path
	if err := reopened.loadLocked(); err != nil {
		t.Fatal(err)
	}
	if reopened.state.Runs["0"].Reason != r.Reason || reopened.state.Runs["1"].Output != r.Output {
		t.Fatal("checkpoint failed to recover")
	}
}

func TestScheduleCheckpointRetentionAndOrphanRecovery(t *testing.T) {
	m := schedulerFixture(t)
	m.state.Runs["expired"] = ScheduleRun{ID: "expired", State: "succeeded", ScheduledAt: m.now().AddDate(0, 0, -31)}
	m.state.Runs["active"] = ScheduleRun{ID: "active", State: "running", ScheduledAt: m.now().AddDate(0, 0, -31)}
	if err := m.saveLocked(); err != nil {
		t.Fatal(err)
	}
	expired := m.store.manifest.Runs["expired"].File
	orphan := filepath.Join(filepath.Dir(m.path), "record-orphan.json")
	if err := os.WriteFile(orphan, []byte("partial uncommitted record"), 0600); err != nil {
		t.Fatal(err)
	}
	m.tick() // Retention must run even with no schedule or output changes.
	if _, ok := m.state.Runs["expired"]; ok {
		t.Fatal("idle retention did not prune")
	}
	if _, ok := m.state.Runs["active"]; !ok {
		t.Fatal("pruned active execution")
	}
	for _, path := range []string{orphan, filepath.Join(filepath.Dir(m.path), expired)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("unreferenced record remains: %s %v", path, err)
		}
	}
}

func TestScheduleCheckpointRejectsMissingOrEscapingRecords(t *testing.T) {
	for _, file := range []string{"record-missing.json", "../record-escape.json"} {
		m := schedulerFixture(t)
		manifest := schedulerManifest{Version: 2, Schedules: map[string]schedulerRecord{}, Runs: map[string]schedulerRecord{"r": {File: file, Bytes: 10}}}
		data, _ := json.Marshal(manifest)
		if err := os.WriteFile(m.path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := m.loadLocked(); err == nil {
			t.Fatalf("accepted %s", file)
		}
	}
}

func TestScheduleCheckpointFaultStaysLatched(t *testing.T) {
	m := schedulerFixture(t)
	m.state.Runs["r"] = ScheduleRun{ID: "r", State: "pending"}
	if err := os.Mkdir(m.path+".tmp", 0700); err != nil {
		t.Fatal(err)
	}
	if err := m.saveLocked(); err == nil {
		t.Fatal("expected write failure")
	}
	if err := os.Remove(m.path + ".tmp"); err != nil {
		t.Fatal(err)
	}
	if err := m.saveLocked(); err == nil {
		t.Fatal("uncertain storage resumed without restart")
	}
}

func TestScheduleCheckpointRestartDoesNotReplayAndCollectsOrphans(t *testing.T) {
	s := NewServer()
	s.DataDir = t.TempDir()
	s.AdminToken = "test"
	dir := filepath.Join(s.DataDir, "schedules")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	m := schedulerFixture(t)
	m.path = filepath.Join(dir, "state.json")
	for _, state := range []string{"pending", "dispatching", "running"} {
		m.state.Runs[state] = ScheduleRun{ID: state, State: state, ScheduledAt: time.Now()}
	}
	if err := m.saveLocked(); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(dir, "record-uncommitted.json")
	if err := os.WriteFile(orphan, []byte("interrupted write"), 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := s.Scheduler()
	if err != nil {
		t.Fatal(err)
	}
	defer s.StopScheduler()
	for _, id := range []string{"pending", "dispatching", "running"} {
		run, err := reopened.Run(id)
		want := "unknown"
		if id == "pending" {
			want = "interrupted"
		}
		if err != nil || run.State != want {
			t.Fatalf("recovered %s: %+v %v", id, run, err)
		}
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan remains: %v", err)
	}
}

func TestScheduleRetentionCountBounds(t *testing.T) {
	m := schedulerFixture(t)
	for i := range 1100 {
		id := fmt.Sprint(i)
		m.state.Runs[id] = ScheduleRun{ID: id, ScheduleID: fmt.Sprint(i % 10), State: "succeeded", ScheduledAt: m.now().Add(-time.Duration(i) * time.Second)}
	}
	m.pruneLocked(m.now())
	if len(m.state.Runs) != 1000 {
		t.Fatalf("retained %d runs", len(m.state.Runs))
	}
	counts := map[string]int{}
	for _, run := range m.state.Runs {
		counts[run.ScheduleID]++
	}
	for id, count := range counts {
		if count > 100 {
			t.Fatalf("schedule %s retained %d", id, count)
		}
	}
}

func TestSchedulerHealthDoesNotInitialize(t *testing.T) {
	s := NewServer()
	if h := s.SchedulerHealth(); h.Initialized || s.scheduler != nil {
		t.Fatalf("health initialized scheduler: %+v", h)
	}
	m := schedulerFixture(t)
	s.scheduler = m
	m.state.Runs["p"] = ScheduleRun{State: "pending"}
	m.state.Runs["d"] = ScheduleRun{State: "dispatching"}
	m.fault = fmt.Errorf("storage unavailable")
	h := s.SchedulerHealth()
	if !h.Initialized || h.Pending != 1 || h.Running != 1 || h.PersistenceFault == "" {
		t.Fatalf("bad health: %+v", h)
	}
}

func BenchmarkSchedulerCheckpointRetainedLogs(b *testing.B) {
	for _, count := range []int{10, 100, 1000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			m := &Scheduler{path: filepath.Join(b.TempDir(), "state.json"), now: time.Now, state: schedulerState{Version: 1, Schedules: map[string]Schedule{}, Runs: map[string]ScheduleRun{}}}
			for i := range count {
				id := fmt.Sprint(i)
				m.state.Runs[id] = ScheduleRun{ID: id, State: "succeeded", Output: strings.Repeat("x", scheduleLogLimit)}
			}
			if err := m.saveLocked(); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := range b.N {
				r := m.state.Runs["0"]
				r.Reason = fmt.Sprint(i)
				m.state.Runs["0"] = r
				if err := m.saveLocked(); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(m.store.lastCommitBytes), "bytes/commit")
		})
	}
}

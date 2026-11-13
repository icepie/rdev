package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func schedulerFixture(t *testing.T) *Scheduler {
	t.Helper()
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	m := &Scheduler{srv: NewServer(), path: filepath.Join(t.TempDir(), "state.json"), state: schedulerState{Version: 1, Schedules: map[string]Schedule{}, Runs: map[string]ScheduleRun{}}, active: map[string]context.CancelFunc{}, now: func() time.Time { return now }, ready: func(ScheduleSpec) (bool, string) { return true, "" }}
	m.execute = func(context.Context, ScheduleRun, func(ScheduledUpdate)) ScheduledResult {
		return ScheduledResult{State: "succeeded"}
	}
	return m
}
func fixtureSchedule(m *Scheduler) Schedule {
	return Schedule{ID: "task", Revision: 1, State: "enabled", CreatedAt: m.now(), NextRunAt: m.now(), ScheduleSpec: ScheduleSpec{Name: "backup", Kind: "shell", Device: "dev", Command: "backup", Cron: "* * * * *", Timezone: "UTC", Enabled: true, Authorized: true, TimeoutSeconds: 60, BusyPolicy: "skip", MaxWaitSeconds: 300, MisfirePolicy: "skip"}}
}
func TestScheduleDispatchReservationBeforeSideEffect(t *testing.T) {
	m := schedulerFixture(t)
	sc := fixtureSchedule(m)
	m.state.Schedules[sc.ID] = sc
	observed := make(chan ScheduleRun, 1)
	m.execute = func(_ context.Context, r ScheduleRun, _ func(ScheduledUpdate)) ScheduledResult {
		data, err := os.ReadFile(m.path)
		if err != nil {
			t.Error(err)
			return ScheduledResult{State: "failed"}
		}
		var stored schedulerState
		if err = json.Unmarshal(data, &stored); err != nil {
			t.Error(err)
		}
		persisted := stored.Runs[r.ID]
		if persisted.State != "dispatching" || stored.Schedules[sc.ID].Runs != 1 {
			t.Errorf("not durably reserved: %+v", stored)
		}
		observed <- r
		return ScheduledResult{State: "succeeded"}
	}
	m.tick()
	m.wg.Wait()
	select {
	case r := <-observed:
		got, err := m.Run(r.ID)
		if err != nil || got.State != "succeeded" {
			t.Fatalf("run=%+v err=%v", got, err)
		}
	default:
		t.Fatal("no execution")
	}
	m.tick()
	m.wg.Wait()
	if len(m.Runs(sc.ID)) != 1 {
		t.Fatal("same trigger duplicated")
	}
}
func TestSchedulePersistenceFailurePreventsExecution(t *testing.T) {
	m := schedulerFixture(t)
	sc := fixtureSchedule(m)
	m.state.Schedules[sc.ID] = sc
	m.path = filepath.Join(t.TempDir(), "missing", "state.json")
	var calls atomic.Int32
	m.execute = func(context.Context, ScheduleRun, func(ScheduledUpdate)) ScheduledResult {
		calls.Add(1)
		return ScheduledResult{State: "succeeded"}
	}
	m.tick()
	m.wg.Wait()
	if calls.Load() != 0 || m.fault == nil {
		t.Fatal("side effect ran without durable reservation")
	}
}
func TestScheduleBusyWaitPauseAndOverlap(t *testing.T) {
	m := schedulerFixture(t)
	sc := fixtureSchedule(m)
	sc.BusyPolicy = "wait"
	m.state.Schedules[sc.ID] = sc
	m.ready = func(ScheduleSpec) (bool, string) { return false, "thread_busy" }
	m.tick()
	runs := m.Runs(sc.ID)
	if len(runs) != 1 || runs[0].State != "pending" {
		t.Fatalf("runs=%+v", runs)
	}
	m.mu.Lock()
	r := m.newRunLocked(sc, m.now(), "manual")
	m.mu.Unlock()
	if r.State != "skipped" || r.Reason != "overlap" {
		t.Fatalf("overlap=%+v", r)
	}
	if _, err := m.Action(sc.ID, "pause", "user"); err != nil {
		t.Fatal(err)
	}
	pending, err := m.Run(runs[0].ID)
	if err != nil || pending.State != "skipped" || pending.Reason != "schedule_paused" {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
}
func TestScheduleWaitExpirationAndLimitPrecedence(t *testing.T) {
	for _, limit := range []bool{false, true} {
		m := schedulerFixture(t)
		sc := fixtureSchedule(m)
		sc.BusyPolicy = "wait"
		sc.MaxWaitSeconds = 1
		m.state.Schedules[sc.ID] = sc
		r := m.newRunLocked(sc, m.now().Add(-time.Minute), "manual")
		m.ready = func(ScheduleSpec) (bool, string) { return false, "thread_busy" }
		if limit {
			sc.MaxRuns = 1
			sc.Runs = 1
			m.state.Schedules[sc.ID] = sc
			for i := range scheduleMaxConcurrent {
				m.active[string(rune('a'+i))] = func() {}
			}
		}
		m.dispatch(r)
		got, _ := m.Run(r.ID)
		want := "thread_busy_wait_expired"
		if limit {
			want = "run_limit"
		}
		if got.State != "skipped" || got.Reason != want {
			t.Fatalf("limit=%v got=%+v", limit, got)
		}
	}
}
func TestScheduleTimezoneDSTAndSingleRun(t *testing.T) {
	sc := ScheduleSpec{Cron: "30 1 * * *", Timezone: "America/New_York"}
	before := time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC)
	first := nextSchedule(sc, before, "")
	if first != time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC) {
		t.Fatalf("first=%v", first)
	}
	next := nextSchedule(sc, first, scheduleWall(sc, first))
	if next != time.Date(2026, 11, 2, 6, 30, 0, 0, time.UTC) {
		t.Fatalf("repeated DST hour: %v", next)
	}
	sc.Cron = "* * * * *"
	last := time.Date(2026, 11, 1, 5, 59, 0, 0, time.UTC)
	next = nextSchedule(sc, last, scheduleWall(sc, last))
	if next != time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC) {
		t.Fatalf("repeated wall minutes: %v", next)
	}
	sc.Cron = "30 2 * * *"
	next = nextSchedule(sc, time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC), "")
	if next != time.Date(2026, 3, 9, 6, 30, 0, 0, time.UTC) {
		t.Fatalf("spring gap: %v", next)
	}
	once := ScheduleSpec{At: "2026-11-12T10:00:00Z", Timezone: "UTC"}
	if !nextSchedule(once, time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC), "").IsZero() {
		t.Fatal("single task repeated")
	}
}
func TestScheduleRecoveryDoesNotReplayDispatchedWork(t *testing.T) {
	s := NewServer()
	s.DataDir = t.TempDir()
	s.AdminToken = "test"
	dir := filepath.Join(s.DataDir, "schedules")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	state := schedulerState{Version: 1, Schedules: map[string]Schedule{}, Runs: map[string]ScheduleRun{"r": {ID: "r", State: "dispatching", ScheduledAt: time.Now()}}}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(dir, "state.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	m, err := s.Scheduler()
	if err != nil {
		t.Fatal(err)
	}
	defer s.StopScheduler()
	r, err := m.Run("r")
	if err != nil || r.State != "unknown" {
		t.Fatalf("recovered=%+v err=%v", r, err)
	}
	duplicate := NewServer()
	duplicate.DataDir = s.DataDir
	duplicate.AdminToken = "test"
	if _, err := duplicate.Scheduler(); err == nil {
		duplicate.StopScheduler()
		t.Fatal("second dispatcher acquired same storage")
	}
}
func TestScheduleSnapshotsDoNotShareMutableConfiguration(t *testing.T) {
	m := schedulerFixture(t)
	sc := fixtureSchedule(m)
	sc.AllowedTools = []string{"read_file"}
	sc.Events = []ScheduleChange{{Action: "create"}}
	m.state.Schedules[sc.ID] = sc
	got, _ := m.Get(sc.ID)
	got.AllowedTools[0] = "shell"
	got.Events[0].Action = "delete"
	again, _ := m.Get(sc.ID)
	if again.AllowedTools[0] != "read_file" || again.Events[0].Action != "create" {
		t.Fatal("caller mutated stored configuration")
	}
}

func TestScheduleLogTailKeepsAbsoluteUTF8Cursor(t *testing.T) {
	m := schedulerFixture(t)
	sc := fixtureSchedule(m)
	m.state.Schedules[sc.ID] = sc
	text := strings.Repeat("界", scheduleLogLimit/3+20)
	m.execute = func(_ context.Context, _ ScheduleRun, report func(ScheduledUpdate)) ScheduledResult {
		report(ScheduledUpdate{Output: text})
		report(ScheduledUpdate{Output: "done"})
		return ScheduledResult{State: "succeeded"}
	}
	m.tick()
	m.wg.Wait()
	r := m.Runs(sc.ID)[0]
	if !r.Truncated || !utf8.ValidString(r.Output) || len(r.Output) > scheduleLogLimit || r.OutputOffset+len(r.Output) != len(text)+4 || !strings.HasSuffix(r.Output, "done") {
		t.Fatalf("invalid tail: offset=%d size=%d truncated=%v", r.OutputOffset, len(r.Output), r.Truncated)
	}
}

func TestScheduleCatchupRunsOnlyLatestOccurrence(t *testing.T) {
	m := schedulerFixture(t)
	sc := fixtureSchedule(m)
	sc.NextRunAt = m.now().Add(-24 * time.Hour)
	sc.MisfirePolicy = "latest"
	sc.GraceSeconds = 300
	m.state.Schedules[sc.ID] = sc
	m.tick()
	m.wg.Wait()
	runs := m.Runs(sc.ID)
	if len(runs) != 1 || !runs[0].ScheduledAt.Equal(m.now()) || runs[0].State != "succeeded" {
		t.Fatalf("catchup=%+v", runs)
	}
}

func TestScheduleUpdateCannotMoveDeviceOwnership(t *testing.T) {
	m := schedulerFixture(t)
	sc := fixtureSchedule(m)
	m.state.Schedules[sc.ID] = sc
	changed := sc.ScheduleSpec
	changed.Device = "another-device"
	if _, err := m.Update(sc.ID, changed, sc.Revision, "agent"); err == nil {
		t.Fatal("task ownership moved across device scopes")
	}
	got, _ := m.Get(sc.ID)
	if got.Device != sc.Device || got.Revision != sc.Revision {
		t.Fatalf("rejected edit modified schedule: %+v", got)
	}
}

func TestScheduleExpirationAndTimezoneUpdate(t *testing.T) {
	m := schedulerFixture(t)
	spec := ScheduleSpec{At: "2026-11-12T10:00:00Z", Timezone: "UTC", ExpiresAt: "2026-11-12T10:00:00Z"}
	if next := nextSchedule(spec, m.now(), ""); !next.IsZero() {
		t.Fatalf("preview includes expired occurrence: %v", next)
	}
	sc := fixtureSchedule(m)
	sc.Timezone = "Asia/Tokyo"
	sc.LastWall = "2026-11-12T18:00"
	m.state.Schedules[sc.ID] = sc
	spec = sc.ScheduleSpec
	spec.Timezone = "UTC"
	updated, err := m.Update(sc.ID, spec, sc.Revision, "user")
	if err != nil || !updated.NextRunAt.Equal(m.now().Add(time.Minute)) {
		t.Fatalf("old timezone wall clock suppressed new schedule: next=%v err=%v", updated.NextRunAt, err)
	}
}

func TestScheduleEmptyCatchupWindowKeepsNextOccurrence(t *testing.T) {
	m := schedulerFixture(t)
	sc := fixtureSchedule(m)
	sc.Cron = "0 10 * * *"
	sc.NextRunAt = m.now().Add(-23 * time.Hour)
	sc.MisfirePolicy, sc.GraceSeconds = "latest", 300
	m.state.Schedules[sc.ID] = sc
	m.tick()
	m.wg.Wait()
	got, _ := m.Get(sc.ID)
	if !got.NextRunAt.Equal(m.now().Add(time.Hour)) {
		t.Fatalf("catchup consumed future occurrence: %v", got.NextRunAt)
	}
	runs := m.Runs(sc.ID)
	if len(runs) != 1 || runs[0].Reason != "misfire" {
		t.Fatalf("missed execution not recorded: %+v", runs)
	}
}

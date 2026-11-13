package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"
	"unicode/utf8"

	"github.com/gofrs/flock"
	"github.com/robfig/cron/v3"
	"rdev/internal/agent"
)

// ScheduleSpec is an explicitly authorized unattended execution, not a chat job.
type ScheduleSpec struct {
	Name           string   `json:"name"`
	Kind           string   `json:"kind"`
	Device         string   `json:"device"`
	Cwd            string   `json:"cwd"`
	Cron           string   `json:"cron"`
	At             string   `json:"at"`
	Timezone       string   `json:"timezone"`
	Command        string   `json:"command"`
	SessionMode    string   `json:"sessionMode"`
	Thread         string   `json:"thread"`
	Model          string   `json:"model"`
	Reasoning      string   `json:"reasoning"`
	Prompt         string   `json:"prompt"`
	AllowedTools   []string `json:"allowedTools"`
	TimeoutSeconds int      `json:"timeoutSeconds"`
	TokenBudget    int      `json:"tokenBudget"`
	BusyPolicy     string   `json:"busyPolicy"`
	MaxWaitSeconds int      `json:"maxWaitSeconds"`
	MaxRuns        int      `json:"maxRuns"`
	ExpiresAt      string   `json:"expiresAt"`
	Enabled        bool     `json:"enabled"`
	Authorized     bool     `json:"authorized"`
	MisfirePolicy  string   `json:"misfirePolicy"`
	GraceSeconds   int      `json:"graceSeconds"`
	MutexGroup     string   `json:"mutexGroup"`
}

type ScheduleChange struct {
	At       time.Time `json:"at"`
	Action   string    `json:"action"`
	Source   string    `json:"source"`
	Revision int       `json:"revision"`
	Detail   string    `json:"detail"`
}

type Schedule struct {
	ScheduleSpec
	ID        string           `json:"id"`
	Revision  int              `json:"revision"`
	State     string           `json:"state"`
	NextRunAt time.Time        `json:"nextRunAt"`
	CreatedAt time.Time        `json:"createdAt"`
	UpdatedAt time.Time        `json:"updatedAt"`
	Runs      int              `json:"runs"`
	Events    []ScheduleChange `json:"events"`
	LastWall  string           `json:"lastWall,omitempty"`
}

type ScheduleRun struct {
	ID               string       `json:"id"`
	ScheduleID       string       `json:"scheduleId"`
	Name             string       `json:"name"`
	Kind             string       `json:"kind"`
	Device           string       `json:"device"`
	Source           string       `json:"source"`
	State            string       `json:"state"`
	Reason           string       `json:"reason"`
	ScheduleRevision int          `json:"scheduleRevision"`
	ScheduledAt      time.Time    `json:"scheduledAt"`
	StartedAt        time.Time    `json:"startedAt"`
	FinishedAt       time.Time    `json:"finishedAt"`
	Snapshot         ScheduleSpec `json:"snapshot"`
	Thread           string       `json:"thread"`
	Turn             string       `json:"turn"`
	Output           string       `json:"output"`
	OutputOffset     int          `json:"outputOffset"`
	Truncated        bool         `json:"truncated"`
	ExitCode         *int         `json:"exitCode"`
	Usage            agent.Usage  `json:"usage"`
}

type ScheduledUpdate struct {
	Output string
	Thread string
	Turn   string
	Usage  *agent.Usage
	Actual *ScheduleSpec
}
type ScheduledResult struct {
	State    string
	Reason   string
	ExitCode *int
	Usage    agent.Usage
}

type schedulerState struct {
	Version   int                    `json:"version"`
	Schedules map[string]Schedule    `json:"schedules"`
	Runs      map[string]ScheduleRun `json:"runs"`
}

const scheduleLogLimit = 256 << 10
const scheduleStateLimit = 320 << 20
const scheduleMaxConcurrent = 8

// Scheduler is a single-owner durable dispatcher. No external side effect is
// started before its dispatch reservation has been fsynced successfully.
type Scheduler struct {
	mu      sync.Mutex
	srv     *Server
	path    string
	lock    *flock.Flock
	state   schedulerState
	active  map[string]context.CancelFunc
	closed  bool
	dirty   bool
	fault   error
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	now     func() time.Time
	ready   func(ScheduleSpec) (bool, string)
	execute func(context.Context, ScheduleRun, func(ScheduledUpdate)) ScheduledResult
}

func (s *Server) Scheduler() (*Scheduler, error) {
	s.schedulerMu.Lock()
	defer s.schedulerMu.Unlock()
	if s.scheduler != nil {
		return s.scheduler, nil
	}
	if s.DataDir == "" || s.AdminToken == "" {
		return nil, fmt.Errorf("scheduled tasks require a data directory and configured administrator token")
	}
	m, err := openScheduler(s)
	if err != nil {
		return nil, err
	}
	s.scheduler = m
	return m, nil
}
func (s *Server) StartScheduler() error {
	if s.AdminToken == "" || s.DataDir == "" {
		return nil
	}
	_, err := s.Scheduler()
	return err
}
func (s *Server) StopScheduler() {
	s.schedulerMu.Lock()
	m := s.scheduler
	s.schedulerMu.Unlock()
	if m != nil {
		m.close()
	}
}

func openScheduler(s *Server) (*Scheduler, error) {
	dir := filepath.Join(s.DataDir, "schedules")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock := flock.New(filepath.Join(dir, "owner.lock"))
	ok, err := lock.TryLock()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("schedule storage is already owned by another server")
	}
	m := &Scheduler{srv: s, path: filepath.Join(dir, "state.json"), lock: lock, now: time.Now, active: make(map[string]context.CancelFunc), ready: s.scheduledTaskReady, execute: s.runScheduledTask, state: schedulerState{Version: 1, Schedules: make(map[string]Schedule), Runs: make(map[string]ScheduleRun)}}
	failed := true
	defer func() {
		if failed {
			_ = lock.Unlock()
		}
	}()
	f, err := os.Open(m.path)
	if err == nil {
		data, e := io.ReadAll(io.LimitReader(f, scheduleStateLimit+1))
		_ = f.Close()
		if e != nil {
			return nil, e
		}
		if len(data) > scheduleStateLimit {
			return nil, fmt.Errorf("schedule state exceeds size limit")
		}
		if e = json.Unmarshal(data, &m.state); e != nil {
			return nil, fmt.Errorf("invalid schedule state: %w", e)
		}
		if m.state.Version != 1 || m.state.Schedules == nil || m.state.Runs == nil {
			return nil, fmt.Errorf("unsupported schedule state")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	now := m.now().UTC()
	for id, r := range m.state.Runs {
		if scheduleRunActive(r.State) {
			if r.State == "pending" {
				r.State = "interrupted"
				r.Reason = "server restarted before dispatch"
			} else {
				r.State = "unknown"
				r.Reason = "server restarted; execution outcome unconfirmed, not replayed"
			}
			r.FinishedAt = now
			m.state.Runs[id] = r
		}
	}
	for id, sc := range m.state.Schedules {
		if sc.State != "enabled" || sc.NextRunAt.IsZero() || !sc.NextRunAt.Before(now) {
			continue
		}
		// Restart catch-up is opt-in; never replay all missed occurrences.
		if sc.MisfirePolicy == "latest" {
			continue
		}
		m.addSkippedLocked(sc, sc.NextRunAt, "server_restart_misfire")
		sc.NextRunAt = nextSchedule(sc.ScheduleSpec, now, sc.LastWall)
		if sc.NextRunAt.IsZero() {
			sc.State = "completed"
			sc.Enabled = false
		}
		m.state.Schedules[id] = sc
	}
	m.pruneLocked(now)
	if err = m.saveLocked(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.tick()
			}
		}
	}()
	failed = false
	return m, nil
}

func (m *Scheduler) close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	m.cancel()
	for _, cancel := range m.active {
		cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
	m.mu.Lock()
	if err := m.saveLocked(); err != nil {
		log.Printf("schedule shutdown checkpoint: %v", err)
	}
	m.mu.Unlock()
	_ = m.lock.Unlock()
}

// saveLocked uses an atomic replacement and syncs before returning. A failed
// commit latches a fault: no dispatch is allowed until an operator restarts with
// healthy storage. This also covers uncertain directory-sync outcomes.
func (m *Scheduler) saveLocked() error {
	if m.fault != nil {
		return m.fault
	}
	data, err := json.Marshal(m.state)
	if err == nil && len(data) > scheduleStateLimit {
		err = fmt.Errorf("schedule storage limit reached")
	}
	if err == nil {
		var f *os.File
		f, err = os.OpenFile(m.path+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err == nil {
			_, err = f.Write(data)
			if err == nil {
				err = f.Sync()
			}
			closeErr := f.Close()
			if err == nil {
				err = closeErr
			}
		}
		if err == nil {
			err = os.Rename(m.path+".tmp", m.path)
		}
		// Windows cannot fsync an opened directory; the file itself is synced above.
		if err == nil && runtime.GOOS != "windows" {
			var d *os.File
			d, err = os.Open(filepath.Dir(m.path))
			if err == nil {
				err = d.Sync()
				_ = d.Close()
			}
		}
	}
	if err != nil {
		m.fault = fmt.Errorf("schedule persistence failed; dispatch stopped: %w", err)
		return m.fault
	}
	m.dirty = false
	return nil
}
func (m *Scheduler) usableLocked() error {
	if m.closed {
		return fmt.Errorf("scheduler is stopped")
	}
	return m.fault
}
func cloneScheduleSpec(s ScheduleSpec) ScheduleSpec {
	s.AllowedTools = slices.Clone(s.AllowedTools)
	return s
}
func cloneSchedule(s Schedule) Schedule {
	s.ScheduleSpec = cloneScheduleSpec(s.ScheduleSpec)
	s.Events = slices.Clone(s.Events)
	return s
}
func cloneScheduleRun(r ScheduleRun) ScheduleRun {
	r.Snapshot = cloneScheduleSpec(r.Snapshot)
	if r.ExitCode != nil {
		n := *r.ExitCode
		r.ExitCode = &n
	}
	return r
}
func (m *Scheduler) List() []Schedule {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Schedule, 0, len(m.state.Schedules))
	for _, s := range m.state.Schedules {
		out = append(out, cloneSchedule(s))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}
func (m *Scheduler) Get(id string) (Schedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.state.Schedules[id]
	if !ok {
		return Schedule{}, fmt.Errorf("schedule not found")
	}
	return cloneSchedule(s), nil
}
func (m *Scheduler) Runs(id string) []ScheduleRun {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ScheduleRun, 0)
	for _, r := range m.state.Runs {
		if id == "" || r.ScheduleID == id {
			out = append(out, cloneScheduleRun(r))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ScheduledAt.After(out[j].ScheduledAt) })
	return out
}
func (m *Scheduler) Run(id string) (ScheduleRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.state.Runs[id]
	if !ok {
		return ScheduleRun{}, fmt.Errorf("run not found")
	}
	return cloneScheduleRun(r), nil
}

var scheduleParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

func parseSchedule(spec ScheduleSpec) (cron.Schedule, error) {
	if len(strings.Fields(spec.Cron)) != 5 {
		return nil, fmt.Errorf("cron must contain exactly five fields")
	}
	if _, err := time.LoadLocation(spec.Timezone); err != nil {
		return nil, fmt.Errorf("invalid IANA timezone: %w", err)
	}
	return scheduleParser.Parse("CRON_TZ=" + spec.Timezone + " " + spec.Cron)
}
func normalizeSchedule(spec ScheduleSpec) (ScheduleSpec, error) {
	spec = cloneScheduleSpec(spec)
	spec.Name = strings.TrimSpace(spec.Name)
	spec.Device = strings.TrimSpace(spec.Device)
	if spec.Name == "" || len(spec.Name) > 200 {
		return spec, fmt.Errorf("name must contain 1–200 bytes")
	}
	if spec.Device == "" {
		return spec, fmt.Errorf("device is required")
	}
	if spec.Kind != "shell" && spec.Kind != "agent" {
		return spec, fmt.Errorf("kind must be shell or agent")
	}
	if (spec.Cron == "") == (spec.At == "") {
		return spec, fmt.Errorf("specify exactly one of cron or at")
	}
	if spec.Timezone == "" {
		spec.Timezone = "UTC"
	}
	if _, err := time.LoadLocation(spec.Timezone); err != nil {
		return spec, fmt.Errorf("invalid timezone")
	}
	if spec.Cron != "" {
		if _, err := parseSchedule(spec); err != nil {
			return spec, err
		}
	} else if _, err := time.Parse(time.RFC3339, spec.At); err != nil {
		return spec, fmt.Errorf("at must be RFC3339")
	}
	if spec.ExpiresAt != "" {
		if _, err := time.Parse(time.RFC3339, spec.ExpiresAt); err != nil {
			return spec, fmt.Errorf("expiresAt must be RFC3339")
		}
	}
	if spec.TimeoutSeconds == 0 {
		spec.TimeoutSeconds = 1800
	}
	if spec.TimeoutSeconds < 1 || spec.TimeoutSeconds > 86400 {
		return spec, fmt.Errorf("timeoutSeconds must be 1–86400")
	}
	if spec.MaxRuns < 0 {
		return spec, fmt.Errorf("maxRuns cannot be negative")
	}
	if spec.BusyPolicy == "" {
		spec.BusyPolicy = "skip"
	}
	if spec.BusyPolicy != "skip" && spec.BusyPolicy != "wait" {
		return spec, fmt.Errorf("busyPolicy must be skip or wait")
	}
	if spec.MaxWaitSeconds == 0 {
		spec.MaxWaitSeconds = 300
	}
	if spec.MaxWaitSeconds < 1 || spec.MaxWaitSeconds > 86400 {
		return spec, fmt.Errorf("maxWaitSeconds must be 1–86400")
	}
	if spec.MisfirePolicy == "" {
		spec.MisfirePolicy = "skip"
	}
	if spec.MisfirePolicy != "skip" && spec.MisfirePolicy != "latest" {
		return spec, fmt.Errorf("misfirePolicy must be skip or latest")
	}
	if spec.GraceSeconds == 0 {
		spec.GraceSeconds = 1800
	}
	if spec.GraceSeconds < 1 || spec.GraceSeconds > 86400 {
		return spec, fmt.Errorf("graceSeconds must be 1–86400")
	}
	if len(spec.Command) > 65536 || len(spec.Prompt) > 65536 || len(spec.Cwd) > 4096 || len(spec.MutexGroup) > 200 {
		return spec, fmt.Errorf("task content exceeds size limit")
	}
	if spec.Kind == "shell" {
		if strings.TrimSpace(spec.Command) == "" {
			return spec, fmt.Errorf("command is required")
		}
		if spec.Device == agentServerDeviceID {
			return spec, fmt.Errorf("shell tasks require a remote device, not the server")
		}
	} else {
		if spec.SessionMode == "" {
			spec.SessionMode = "new"
		}
		if spec.SessionMode != "new" && spec.SessionMode != "existing" {
			return spec, fmt.Errorf("sessionMode must be new or existing")
		}
		if strings.TrimSpace(spec.Prompt) == "" {
			return spec, fmt.Errorf("prompt is required")
		}
		if spec.SessionMode == "existing" && spec.Thread == "" {
			return spec, fmt.Errorf("existing session requires a thread")
		}
		if spec.SessionMode == "new" && spec.Model == "" {
			return spec, fmt.Errorf("new sessions require an explicit model")
		}
		if spec.TokenBudget == 0 {
			spec.TokenBudget = 100000
		}
		if spec.TokenBudget < 1 {
			return spec, fmt.Errorf("tokenBudget must be positive")
		}
	}
	if !spec.Authorized {
		return spec, fmt.Errorf("explicit unattended execution authorization is required")
	}
	return spec, nil
}
func scheduleWall(spec ScheduleSpec, t time.Time) string {
	loc, err := time.LoadLocation(spec.Timezone)
	if err != nil {
		return ""
	}
	return t.In(loc).Format("2006-01-02T15:04")
}
func nextSchedule(spec ScheduleSpec, after time.Time, lastWall string) time.Time {
	var next time.Time
	if spec.At != "" {
		next, _ = time.Parse(time.RFC3339, spec.At)
		if !next.After(after) {
			return time.Time{}
		}
	} else {
		c, err := parseSchedule(spec)
		if err != nil {
			return time.Time{}
		}
		next = c.Next(after)
		// During a DST rollback the clock revisits an entire range of local
		// minutes, not just one duplicate instant. Never revisit that range.
		for !next.IsZero() && lastWall != "" && scheduleWall(spec, next) <= lastWall {
			next = c.Next(next)
		}
	}
	if spec.ExpiresAt != "" {
		end, err := time.Parse(time.RFC3339, spec.ExpiresAt)
		if err != nil || !next.Before(end) {
			return time.Time{}
		}
	}
	return next.UTC()
}
func (m *Scheduler) Preview(spec ScheduleSpec) ([]time.Time, error) {
	if spec.Timezone == "" {
		spec.Timezone = "UTC"
	}
	if (spec.Cron == "") == (spec.At == "") {
		return nil, fmt.Errorf("specify exactly one of cron or at")
	}
	if _, err := time.LoadLocation(spec.Timezone); err != nil {
		return nil, err
	}
	if spec.Cron != "" {
		if _, err := parseSchedule(spec); err != nil {
			return nil, err
		}
	} else if _, err := time.Parse(time.RFC3339, spec.At); err != nil {
		return nil, err
	}
	if spec.ExpiresAt != "" {
		if _, err := time.Parse(time.RFC3339, spec.ExpiresAt); err != nil {
			return nil, err
		}
	}
	out := make([]time.Time, 0, 5)
	after := m.now()
	wall := ""
	for range 5 {
		n := nextSchedule(spec, after, wall)
		if n.IsZero() {
			break
		}
		out = append(out, n)
		after = n
		wall = scheduleWall(spec, n)
	}
	return out, nil
}
func addScheduleChange(sc *Schedule, now time.Time, action, source, detail string) {
	sc.UpdatedAt = now
	sc.Events = append(slices.Clone(sc.Events), ScheduleChange{At: now, Action: action, Source: source, Revision: sc.Revision, Detail: detail})
	if len(sc.Events) > 100 {
		sc.Events = sc.Events[len(sc.Events)-100:]
	}
}
func (m *Scheduler) Create(spec ScheduleSpec, source string) (Schedule, error) {
	spec, err := normalizeSchedule(spec)
	if err != nil {
		return Schedule{}, err
	}
	if err = m.srv.validateScheduledTarget(spec); err != nil {
		return Schedule{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err = m.usableLocked(); err != nil {
		return Schedule{}, err
	}
	if len(m.state.Schedules) >= 1000 {
		return Schedule{}, fmt.Errorf("at most 1000 schedules")
	}
	now := m.now().UTC()
	sc := Schedule{ScheduleSpec: spec, ID: generateID(), Revision: 1, State: "paused", CreatedAt: now, Events: []ScheduleChange{}}
	sc.NextRunAt = nextSchedule(spec, now, "")
	if sc.NextRunAt.IsZero() {
		return Schedule{}, fmt.Errorf("schedule has no future execution time")
	}
	if spec.Enabled {
		sc.State = "enabled"
	} else {
		sc.NextRunAt = time.Time{}
	}
	addScheduleChange(&sc, now, "create", source, "created task")
	m.state.Schedules[sc.ID] = sc
	if err = m.saveLocked(); err != nil {
		delete(m.state.Schedules, sc.ID)
		return Schedule{}, err
	}
	return cloneSchedule(sc), nil
}
func (m *Scheduler) Update(id string, spec ScheduleSpec, revision int, source string) (Schedule, error) {
	spec, err := normalizeSchedule(spec)
	if err != nil {
		return Schedule{}, err
	}
	if err = m.srv.validateScheduledTarget(spec); err != nil {
		return Schedule{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err = m.usableLocked(); err != nil {
		return Schedule{}, err
	}
	old, ok := m.state.Schedules[id]
	if !ok {
		return Schedule{}, fmt.Errorf("schedule not found")
	}
	if revision != old.Revision {
		return Schedule{}, fmt.Errorf("schedule revision conflict; reload before editing")
	}
	if spec.Device != old.Device {
		return Schedule{}, fmt.Errorf("task device cannot change; create a new task")
	}
	sc := cloneSchedule(old)
	sc.ScheduleSpec = spec
	if spec.Timezone != old.Timezone {
		sc.LastWall = ""
	}
	sc.Revision++
	sc.State = "paused"
	sc.NextRunAt = time.Time{}
	now := m.now().UTC()
	if spec.Enabled {
		sc.State = "enabled"
		sc.NextRunAt = nextSchedule(spec, now, sc.LastWall)
		if sc.NextRunAt.IsZero() {
			return Schedule{}, fmt.Errorf("schedule has no future execution time")
		}
	}
	before, _ := json.Marshal(old.ScheduleSpec)
	after, _ := json.Marshal(spec)
	addScheduleChange(&sc, now, "update", source, string(before)+" → "+string(after))
	m.state.Schedules[id] = sc
	m.clearPendingLocked(id, "schedule_updated")
	if err = m.saveLocked(); err != nil {
		m.state.Schedules[id] = old
		return Schedule{}, err
	}
	return cloneSchedule(sc), nil
}
func scheduleRunActive(state string) bool {
	return state == "pending" || state == "dispatching" || state == "running"
}
func (m *Scheduler) clearPendingLocked(id, reason string) {
	for rid, r := range m.state.Runs {
		if r.ScheduleID == id && r.State == "pending" {
			r.State = "skipped"
			r.Reason = reason
			r.FinishedAt = m.now().UTC()
			m.state.Runs[rid] = r
		}
	}
}
func (m *Scheduler) Action(id, action, source string) (any, error) {
	checkedRevision := 0
	if action == "enable" || action == "run_now" {
		sc, err := m.Get(id)
		if err != nil {
			return nil, err
		}
		checkedRevision = sc.Revision
		if !sc.Authorized {
			return nil, fmt.Errorf("unattended execution authorization required")
		}
		if err = m.srv.validateScheduledTarget(sc.ScheduleSpec); err != nil {
			return nil, err
		}
	}
	m.mu.Lock()
	if err := m.usableLocked(); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	sc, ok := m.state.Schedules[id]
	if !ok {
		m.mu.Unlock()
		return nil, fmt.Errorf("schedule not found")
	}
	if checkedRevision != 0 && sc.Revision != checkedRevision {
		m.mu.Unlock()
		return nil, fmt.Errorf("schedule revision conflict; retry action")
	}
	old := sc
	now := m.now().UTC()
	var result any
	switch action {
	case "pause":
		sc.State = "paused"
		sc.Enabled = false
		sc.NextRunAt = time.Time{}
		m.clearPendingLocked(id, "schedule_paused")
	case "enable":
		if sc.MaxRuns > 0 && sc.Runs >= sc.MaxRuns {
			m.mu.Unlock()
			return nil, fmt.Errorf("run limit exhausted")
		}
		sc.NextRunAt = nextSchedule(sc.ScheduleSpec, now, sc.LastWall)
		if sc.NextRunAt.IsZero() {
			m.mu.Unlock()
			return nil, fmt.Errorf("schedule has no future execution time")
		}
		sc.State = "enabled"
		sc.Enabled = true
	case "delete":
		m.clearPendingLocked(id, "schedule_deleted")
		delete(m.state.Schedules, id)
	case "run_now":
		if reason := scheduleLimitReason(sc, now); reason != "" {
			m.mu.Unlock()
			return nil, fmt.Errorf("%s", reason)
		}
		r := m.newRunLocked(sc, now, "manual")
		result = cloneScheduleRun(r)
	default:
		m.mu.Unlock()
		return nil, fmt.Errorf("unknown schedule action %q", action)
	}
	if action != "delete" {
		if action != "run_now" {
			sc.Revision++
		}
		addScheduleChange(&sc, now, action, source, action)
		m.state.Schedules[id] = sc
		if result == nil {
			result = cloneSchedule(sc)
		}
	} else {
		result = map[string]any{"id": id, "deleted": true}
	}
	if err := m.saveLocked(); err != nil {
		m.state.Schedules[id] = old
		m.mu.Unlock()
		return nil, err
	}
	m.mu.Unlock()
	if action == "run_now" {
		m.tick()
		if run, ok := result.(ScheduleRun); ok {
			return m.Run(run.ID)
		}
	}
	return result, nil
}
func (m *Scheduler) Cancel(id string) error {
	m.mu.Lock()
	if err := m.usableLocked(); err != nil {
		m.mu.Unlock()
		return err
	}
	r, ok := m.state.Runs[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("run not found")
	}
	if !scheduleRunActive(r.State) {
		m.mu.Unlock()
		return nil
	}
	cancel := m.active[id]
	if r.State == "pending" {
		r.State = "canceled"
		r.FinishedAt = m.now().UTC()
		r.Reason = "canceled before dispatch"
	} else {
		r.Reason = "cancellation requested; awaiting execution outcome"
	}
	m.state.Runs[id] = r
	err := m.saveLocked()
	m.mu.Unlock()
	if err == nil && cancel != nil {
		cancel()
	}
	return err
}
func (m *Scheduler) InvalidateThread(device, thread, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.usableLocked() != nil {
		return
	}
	changed := false
	for id, sc := range m.state.Schedules {
		if sc.Kind == "agent" && sc.SessionMode == "existing" && sc.Device == device && sc.Thread == thread {
			sc.State = "paused"
			sc.Enabled = false
			sc.NextRunAt = time.Time{}
			sc.Revision++
			addScheduleChange(&sc, m.now().UTC(), "pause", "system", reason)
			m.state.Schedules[id] = sc
			m.clearPendingLocked(id, reason)
			changed = true
		}
	}
	if changed {
		if err := m.saveLocked(); err != nil {
			log.Printf("pause schedules after thread invalidation: %v", err)
		}
	}
}
func scheduleLimitReason(sc Schedule, now time.Time) string {
	if sc.MaxRuns > 0 && sc.Runs >= sc.MaxRuns {
		return "run_limit"
	}
	if sc.ExpiresAt != "" {
		end, _ := time.Parse(time.RFC3339, sc.ExpiresAt)
		if !now.Before(end) {
			return "expired"
		}
	}
	return ""
}
func (m *Scheduler) addSkippedLocked(sc Schedule, at time.Time, reason string) {
	r := ScheduleRun{ID: generateID(), ScheduleID: sc.ID, Name: sc.Name, Kind: sc.Kind, Device: sc.Device, ScheduleRevision: sc.Revision, Snapshot: cloneScheduleSpec(sc.ScheduleSpec), ScheduledAt: at, FinishedAt: m.now().UTC(), Source: "scheduled", State: "skipped", Reason: reason}
	m.state.Runs[r.ID] = r
}
func (m *Scheduler) newRunLocked(sc Schedule, at time.Time, source string) ScheduleRun {
	r := ScheduleRun{ID: generateID(), ScheduleID: sc.ID, Name: sc.Name, Kind: sc.Kind, Device: sc.Device, ScheduleRevision: sc.Revision, Snapshot: cloneScheduleSpec(sc.ScheduleSpec), ScheduledAt: at, Source: source, State: "pending"}
	for _, other := range m.state.Runs {
		if other.ScheduleID == sc.ID && scheduleRunActive(other.State) {
			r.State = "skipped"
			r.Reason = "overlap"
			r.FinishedAt = m.now().UTC()
			break
		}
	}
	m.state.Runs[r.ID] = r
	return r
}
func (m *Scheduler) pruneLocked(now time.Time) {
	ids := make([]string, 0, len(m.state.Runs))
	for id, r := range m.state.Runs {
		if !scheduleRunActive(r.State) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return m.state.Runs[ids[i]].ScheduledAt.After(m.state.Runs[ids[j]].ScheduledAt) })
	counts := make(map[string]int)
	for i, id := range ids {
		r := m.state.Runs[id]
		counts[r.ScheduleID]++
		if i >= 1000 || counts[r.ScheduleID] > 100 || r.ScheduledAt.Before(now.AddDate(0, 0, -30)) {
			delete(m.state.Runs, id)
		}
	}
}

func (m *Scheduler) tick() {
	m.mu.Lock()
	if m.usableLocked() != nil {
		m.mu.Unlock()
		return
	}
	now := m.now().UTC()
	changed := m.dirty
	for id, sc := range m.state.Schedules {
		if sc.State != "enabled" {
			continue
		}
		if reason := scheduleLimitReason(sc, now); reason != "" {
			sc.State = "completed"
			sc.Enabled = false
			sc.NextRunAt = time.Time{}
			m.clearPendingLocked(id, reason)
			m.state.Schedules[id] = sc
			changed = true
			continue
		}
		if sc.NextRunAt.IsZero() || sc.NextRunAt.After(now) {
			continue
		}
		due := sc.NextRunAt
		// A single latest occurrence is enough for catch-up. Walk at most the
		// grace window (<=24h), not every missed minute since a long outage.
		if sc.MisfirePolicy == "latest" && sc.Cron != "" && now.Sub(due) > time.Minute {
			start := now.Add(-time.Duration(sc.GraceSeconds) * time.Second)
			if due.Before(start) {
				due = nextSchedule(sc.ScheduleSpec, start.Add(-time.Nanosecond), sc.LastWall)
			}
			for range 1441 {
				if due.IsZero() {
					break
				}
				n := nextSchedule(sc.ScheduleSpec, due, scheduleWall(sc.ScheduleSpec, due))
				if n.IsZero() || n.After(now) {
					break
				}
				due = n
			}
		}
		wall := scheduleWall(sc.ScheduleSpec, due)
		if due.IsZero() || due.After(now) || wall == sc.LastWall {
			m.addSkippedLocked(sc, sc.NextRunAt, "misfire")
			wall = sc.LastWall
		} else if now.Sub(due) > time.Minute && (sc.MisfirePolicy != "latest" || now.Sub(due) > time.Duration(sc.GraceSeconds)*time.Second) {
			m.addSkippedLocked(sc, due, "misfire")
		} else {
			m.newRunLocked(sc, due, "scheduled")
		}
		sc.LastWall = wall
		sc.NextRunAt = nextSchedule(sc.ScheduleSpec, now, wall)
		if sc.NextRunAt.IsZero() {
			sc.State = "completed"
			sc.Enabled = false
		}
		m.state.Schedules[id] = sc
		changed = true
	}
	if changed {
		m.pruneLocked(now)
		if err := m.saveLocked(); err != nil {
			log.Print(err)
			m.mu.Unlock()
			return
		}
	}
	pending := make([]ScheduleRun, 0)
	for _, r := range m.state.Runs {
		if r.State == "pending" {
			pending = append(pending, cloneScheduleRun(r))
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].ScheduledAt.Before(pending[j].ScheduledAt) })
	m.mu.Unlock()
	for _, r := range pending {
		m.dispatch(r)
	}
}
func (m *Scheduler) dispatch(candidate ScheduleRun) {
	ready, reason := m.ready(candidate.Snapshot) // Never enter agent/device locks while holding scheduler mutex.
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.usableLocked() != nil {
		return
	}
	r, ok := m.state.Runs[candidate.ID]
	if !ok || r.State != "pending" {
		return
	}
	sc, ok := m.state.Schedules[r.ScheduleID]
	if !ok {
		return
	}
	now := m.now().UTC()
	if sc.Revision != r.ScheduleRevision {
		reason = "schedule_updated"
		ready = false
	}
	if why := scheduleLimitReason(sc, now); why != "" {
		reason = why
		ready = false
	}
	if ready && len(m.active) >= scheduleMaxConcurrent {
		reason = "capacity"
		ready = false
	}
	if ready && r.Snapshot.MutexGroup != "" {
		for id := range m.active {
			other := m.state.Runs[id]
			if other.Snapshot.MutexGroup == r.Snapshot.MutexGroup {
				reason = "mutex_busy"
				ready = false
				break
			}
		}
	}
	if !ready {
		canWait := (reason == "busy" || reason == "thread_busy" || reason == "capacity" || reason == "mutex_busy") && r.Snapshot.BusyPolicy == "wait"
		maxWait := r.Snapshot.MaxWaitSeconds
		if reason == "device_offline" && r.Snapshot.MisfirePolicy == "latest" {
			canWait = true
			maxWait = r.Snapshot.GraceSeconds
		}
		if canWait && now.Sub(r.ScheduledAt) < time.Duration(maxWait)*time.Second {
			return
		}
		r.State = "skipped"
		r.Reason = reason
		if canWait {
			r.Reason += "_wait_expired"
		}
		r.FinishedAt = now
		m.state.Runs[r.ID] = r
		if err := m.saveLocked(); err != nil {
			log.Print(err)
		}
		return
	}
	r.State = "dispatching"
	r.StartedAt = now
	sc.Runs++
	m.state.Runs[r.ID] = r
	m.state.Schedules[sc.ID] = sc
	if err := m.saveLocked(); err != nil {
		log.Print(err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(r.Snapshot.TimeoutSeconds)*time.Second)
	m.active[r.ID] = cancel
	m.wg.Add(1)
	go func() { defer m.wg.Done(); defer cancel(); m.executeRun(ctx, r) }()
}
func (m *Scheduler) executeRun(ctx context.Context, r ScheduleRun) {
	result := ScheduledResult{State: "failed", Reason: "execution did not complete"}
	defer func() {
		if p := recover(); p != nil {
			result = ScheduledResult{State: "unknown", Reason: fmt.Sprintf("execution panicked; outcome unconfirmed: %v", p)}
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		current := m.state.Runs[r.ID]
		current.State = result.State
		if current.State == "" {
			current.State = "failed"
		}
		current.Reason = result.Reason
		current.ExitCode = result.ExitCode
		current.Usage = result.Usage
		current.FinishedAt = m.now().UTC()
		m.state.Runs[r.ID] = current
		delete(m.active, r.ID)
		m.pruneLocked(m.now())
		if err := m.saveLocked(); err != nil {
			log.Print(err)
		}
	}()
	m.mu.Lock()
	current := m.state.Runs[r.ID]
	current.State = "running"
	m.state.Runs[r.ID] = current
	m.dirty = true
	m.mu.Unlock()
	result = m.execute(ctx, r, func(update ScheduledUpdate) {
		m.mu.Lock()
		defer m.mu.Unlock()
		current, ok := m.state.Runs[r.ID]
		if !ok {
			return
		}
		if update.Output != "" {
			current.Output += update.Output
			if len(current.Output) > scheduleLogLimit {
				start := len(current.Output) - scheduleLogLimit
				for start < len(current.Output) && !utf8.RuneStart(current.Output[start]) {
					start++
				}
				current.Output = strings.Clone(current.Output[start:])
				current.OutputOffset += start
				current.Truncated = true
			}
		}
		if update.Thread != "" {
			current.Thread = update.Thread
		}
		if update.Turn != "" {
			current.Turn = update.Turn
		}
		if update.Usage != nil {
			current.Usage = *update.Usage
		}
		if update.Actual != nil {
			current.Snapshot = cloneScheduleSpec(*update.Actual)
		}
		m.state.Runs[r.ID] = current
		m.dirty = true
	})
}

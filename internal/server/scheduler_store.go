package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"time"
)

// The manifest is the commit point. Immutable records keep retained output out
// of subsequent commits; only changed entities and the bounded index are written.
type schedulerRecord struct {
	File  string `json:"file"`
	Bytes int64  `json:"bytes"`
}

type schedulerManifest struct {
	Version   int                        `json:"version"`
	Schedules map[string]schedulerRecord `json:"schedules"`
	Runs      map[string]schedulerRecord `json:"runs"`
}

type schedulerStore struct {
	manifest        schedulerManifest
	committed       schedulerState
	lastCommitAt    time.Time
	lastCommitBytes int64
	storedBytes     int64
}

const scheduleManifestLimit = 1 << 20

func readScheduleFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = fmt.Errorf("schedule storage limit reached")
	}
	return data, err
}

func (m *Scheduler) loadLocked() error {
	data, err := readScheduleFile(m.path, scheduleStateLimit)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var version struct {
		Version int `json:"version"`
	}
	if err = json.Unmarshal(data, &version); err != nil {
		return fmt.Errorf("invalid schedule state: %w", err)
	}
	if version.Version == 1 {
		if err = json.Unmarshal(data, &m.state); err != nil {
			return err
		}
		if m.state.Schedules == nil || m.state.Runs == nil {
			return fmt.Errorf("invalid legacy schedule state")
		}
		return nil // Migrated atomically on the first commit, before dispatch.
	}
	if version.Version != 2 || len(data) > scheduleManifestLimit {
		return fmt.Errorf("unsupported schedule manifest")
	}
	var manifest schedulerManifest
	if err = json.Unmarshal(data, &manifest); err != nil {
		return err
	}
	if manifest.Schedules == nil || manifest.Runs == nil {
		return fmt.Errorf("invalid schedule manifest")
	}
	state := schedulerState{Version: 1, Schedules: make(map[string]Schedule), Runs: make(map[string]ScheduleRun)}
	total := int64(len(data))
	load := func(ref schedulerRecord, target any) error {
		if !strings.HasPrefix(ref.File, "record-") || !strings.HasSuffix(ref.File, ".json") || filepath.Base(ref.File) != ref.File || strings.ContainsAny(ref.File, `/\\`) || ref.Bytes <= 0 || ref.Bytes > scheduleStateLimit-total {
			return fmt.Errorf("invalid schedule record reference")
		}
		payload, e := readScheduleFile(filepath.Join(filepath.Dir(m.path), ref.File), ref.Bytes)
		if e != nil {
			return e
		}
		if int64(len(payload)) != ref.Bytes {
			return fmt.Errorf("schedule record size mismatch")
		}
		total += ref.Bytes
		return json.Unmarshal(payload, target)
	}
	for id, ref := range manifest.Schedules {
		var sc Schedule
		if err = load(ref, &sc); err != nil {
			return err
		}
		if sc.ID != id {
			return fmt.Errorf("schedule record identity mismatch")
		}
		state.Schedules[id] = sc
	}
	for id, ref := range manifest.Runs {
		var run ScheduleRun
		if err = load(ref, &run); err != nil {
			return err
		}
		if run.ID != id {
			return fmt.Errorf("schedule run identity mismatch")
		}
		state.Runs[id] = run
	}
	m.state = state
	m.store.manifest = manifest
	m.store.committed = cloneSchedulerState(state)
	m.store.storedBytes = total
	return nil
}

// Deep-copy only the mutable slices. Output strings are immutable and shared.
func cloneSchedulerState(state schedulerState) schedulerState {
	out := schedulerState{Version: 1, Schedules: make(map[string]Schedule, len(state.Schedules)), Runs: make(map[string]ScheduleRun, len(state.Runs))}
	for id, sc := range state.Schedules {
		out.Schedules[id] = cloneSchedule(sc)
	}
	for id, run := range state.Runs {
		out.Runs[id] = cloneScheduleRun(run)
	}
	return out
}

func syncScheduleDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	err = d.Sync()
	closeErr := d.Close()
	if err == nil {
		err = closeErr
	}
	return err
}

func writeScheduleFile(path string, data []byte, exclusive bool) error {
	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if exclusive {
		flags = os.O_CREATE | os.O_WRONLY | os.O_EXCL
	}
	f, err := os.OpenFile(path, flags, 0600)
	if err != nil {
		return err
	}
	n, err := f.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	return err
}

func (m *Scheduler) collectRecordsLocked(manifest schedulerManifest) error {
	live := make(map[string]bool, len(manifest.Schedules)+len(manifest.Runs))
	for _, ref := range manifest.Schedules {
		live[ref.File] = true
	}
	for _, ref := range manifest.Runs {
		live[ref.File] = true
	}
	entries, err := os.ReadDir(filepath.Dir(m.path))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "record-") && strings.HasSuffix(name, ".json") && !live[name] && !entry.IsDir() {
			if err = os.Remove(filepath.Join(filepath.Dir(m.path), name)); err != nil {
				return err
			}
		}
	}
	return nil
}

// saveLocked is a synchronous durability barrier. Any uncertain write, sync,
// rename or cleanup latches a fault; even a later successful write cannot resume
// dispatch without restart and recovery from the authoritative manifest.
func (m *Scheduler) saveLocked() error {
	if m.fault != nil {
		return m.fault
	}
	if err := m.commitLocked(); err != nil {
		m.fault = fmt.Errorf("schedule persistence failed; dispatch stopped: %w", err)
		return m.fault
	}
	m.dirty = false
	return nil
}

func (m *Scheduler) commitLocked() error {
	manifest := schedulerManifest{Version: 2, Schedules: make(map[string]schedulerRecord, len(m.state.Schedules)), Runs: make(map[string]schedulerRecord, len(m.state.Runs))}
	committed := schedulerState{Version: 1, Schedules: make(map[string]Schedule, len(m.state.Schedules)), Runs: make(map[string]ScheduleRun, len(m.state.Runs))}
	var total, written int64
	persist := func(value any) (schedulerRecord, error) {
		data, err := json.Marshal(value)
		if err != nil {
			return schedulerRecord{}, err
		}
		if int64(len(data)) > scheduleStateLimit-total {
			return schedulerRecord{}, fmt.Errorf("schedule storage limit reached")
		}
		ref := schedulerRecord{File: "record-" + generateID() + ".json", Bytes: int64(len(data))}
		if err = writeScheduleFile(filepath.Join(filepath.Dir(m.path), ref.File), data, true); err != nil {
			return schedulerRecord{}, err
		}
		written += ref.Bytes
		return ref, nil
	}
	for id, sc := range m.state.Schedules {
		ref, exists := m.store.manifest.Schedules[id]
		committed.Schedules[id] = m.store.committed.Schedules[id]
		if !exists || !reflect.DeepEqual(sc, m.store.committed.Schedules[id]) {
			var err error
			ref, err = persist(sc)
			if err != nil {
				return err
			}
			committed.Schedules[id] = cloneSchedule(sc)
		}
		manifest.Schedules[id] = ref
		total += ref.Bytes
	}
	for id, run := range m.state.Runs {
		ref, exists := m.store.manifest.Runs[id]
		committed.Runs[id] = m.store.committed.Runs[id]
		if !exists || !reflect.DeepEqual(run, m.store.committed.Runs[id]) {
			var err error
			ref, err = persist(run)
			if err != nil {
				return err
			}
			committed.Runs[id] = cloneScheduleRun(run)
		}
		manifest.Runs[id] = ref
		total += ref.Bytes
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if len(data) > scheduleManifestLimit || total+int64(len(data)) > scheduleStateLimit {
		return fmt.Errorf("schedule storage limit reached")
	}
	// Record directory entries must be durable before the manifest references them.
	if err = syncScheduleDirectory(m.path); err != nil {
		return err
	}
	if err = writeScheduleFile(m.path+".tmp", data, false); err != nil {
		return err
	}
	if err = os.Rename(m.path+".tmp", m.path); err != nil {
		return err
	}
	if err = syncScheduleDirectory(m.path); err != nil {
		return err
	}
	m.store.manifest = manifest
	m.store.committed = committed
	m.store.lastCommitAt = m.now().UTC()
	m.store.lastCommitBytes = written + int64(len(data))
	m.store.storedBytes = total + int64(len(data))
	return m.collectRecordsLocked(manifest)
}

// SchedulerHealthSnapshot contains no task commands, output or credentials.
type SchedulerHealthSnapshot struct {
	Initialized      bool      `json:"initialized"`
	Stopped          bool      `json:"stopped"`
	Schedules        int       `json:"schedules"`
	Pending          int       `json:"pending"`
	Running          int       `json:"running"`
	PersistenceFault string    `json:"persistenceFault,omitempty"`
	LastCommitAt     time.Time `json:"lastCommitAt"`
	LastCommitBytes  int64     `json:"lastCommitBytes"`
	StoredBytes      int64     `json:"storedBytes"`
}

func (s *Server) SchedulerHealth() SchedulerHealthSnapshot {
	s.schedulerMu.Lock()
	m := s.scheduler
	s.schedulerMu.Unlock()
	if m == nil {
		return SchedulerHealthSnapshot{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	h := SchedulerHealthSnapshot{Initialized: true, Stopped: m.closed, Schedules: len(m.state.Schedules), LastCommitAt: m.store.lastCommitAt, LastCommitBytes: m.store.lastCommitBytes, StoredBytes: m.store.storedBytes}
	if m.fault != nil {
		h.PersistenceFault = m.fault.Error()
	}
	for _, run := range m.state.Runs {
		switch run.State {
		case "pending":
			h.Pending++
		case "dispatching", "running":
			h.Running++
		}
	}
	return h
}

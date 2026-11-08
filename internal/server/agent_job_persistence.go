package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const agentJobsFileLimit = 16 << 20

type agentJobsFile struct {
	Version int                `json:"version"`
	Jobs    []agentJobSnapshot `json:"jobs"`
}

func (st *agentSessionStore) jobsPath(device, thread string) string {
	return strings.TrimSuffix(st.path(device, thread), ".jsonl") + ".jobs.json"
}

// Separate bounded snapshots keep job output out of the model's append history.
// The caller owns the thread persistence lock; store.mu serializes TTL cleanup.
func (st *agentSessionStore) saveJobs(device, thread, model string, jobs []agentJobSnapshot) error {
	data, err := json.Marshal(agentJobsFile{Version: 1, Jobs: jobs})
	if err != nil {
		return err
	}
	if len(data) > agentJobsFileLimit {
		return fmt.Errorf("job snapshot exceeds %d bytes", agentJobsFileLimit)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	history := st.path(device, thread)
	if _, err := os.Stat(history); os.IsNotExist(err) {
		if err := st.rewriteLocked(device, thread, model, nil); err != nil {
			return err
		}
	}
	path := st.jobsPath(device, thread)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	now := time.Now()
	return os.Chtimes(history, now, now)
}

func (st *agentSessionStore) loadJobs(device, thread string) []agentJobSnapshot {
	st.mu.Lock()
	defer st.mu.Unlock()
	f, err := os.Open(st.jobsPath(device, thread))
	if err != nil {
		return nil
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, agentJobsFileLimit+1))
	if err != nil || len(data) > agentJobsFileLimit {
		return nil
	}
	var saved agentJobsFile
	if json.Unmarshal(data, &saved) != nil || saved.Version != 1 {
		return nil
	}
	return saved.Jobs
}

func boundedStoredJob(snapshot agentJobSnapshot) agentJobSnapshot {
	snapshot.Label = agentJobTail(snapshot.Label)
	snapshot.Output = agentJobTail(snapshot.Output)
	snapshot.Result = agentJobTail(snapshot.Result)
	snapshot.Error = agentJobTail(snapshot.Error)
	pending := snapshot.PendingGuidance
	if len(pending) > agentMaxPendingGuidance {
		pending = pending[len(pending)-agentMaxPendingGuidance:]
	}
	snapshot.PendingGuidance = make([]string, len(pending))
	for i, text := range pending {
		if len(text) > agentMaxGuidanceBytes {
			text = text[:agentMaxGuidanceBytes]
		}
		snapshot.PendingGuidance[i] = strings.ToValidUTF8(text, "")
	}
	return snapshot
}

func (m *agentJobs) snapshotForStorage() ([]agentJobSnapshot, uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.revision == m.savedRevision {
		return nil, m.revision
	}
	snapshots := make([]agentJobSnapshot, 0, len(m.jobs))
	for _, job := range m.jobs {
		snapshot := job.snapshot
		if snapshot.Finished == nil {
			snapshot.Output = job.output.String()
			if job.guidance != nil {
				snapshot.PendingGuidance = job.guidance.snapshot()
			}
		}
		snapshots = append(snapshots, boundedStoredJob(snapshot))
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].Started.Before(snapshots[j].Started) })
	return snapshots, m.revision
}

// Restore records, never execution. A process restart cannot prove whether a
// remote side effect finished, so automatically resubmitting a job is unsafe.
func (m *agentJobs) restore(snapshots []agentJobSnapshot) (interrupted bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || len(m.jobs) != 0 {
		return false
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].Started.After(snapshots[j].Started) })
	for _, snapshot := range snapshots {
		if len(m.jobs) >= agentMaxRetainedJobs {
			break
		}
		if snapshot.ID == "" || len(snapshot.ID) > 128 || (snapshot.Kind != "shell" && snapshot.Kind != "subagent") {
			continue
		}
		if _, exists := m.jobs[snapshot.ID]; exists {
			continue
		}
		switch snapshot.State {
		case "running", "completed", "failed", "canceled":
		default:
			continue
		}
		snapshot = boundedStoredJob(snapshot)
		if snapshot.State == "running" {
			snapshot.State = "failed"
			snapshot.Error = "Background task interrupted by server restart; it was not resumed."
			now := time.Now()
			snapshot.Finished = &now
			interrupted = true
		} else if snapshot.Finished == nil {
			continue
		}
		snapshot.CancelRequested = false
		done := make(chan struct{})
		close(done)
		m.jobs[snapshot.ID] = &agentJob{snapshot: snapshot, done: done}
	}
	if interrupted {
		m.revision++
	}
	return interrupted
}

// Independent from socket publication: a slow browser cannot delay disk state.
// Recheck manager identity under persistMu so reset/delete cannot resurrect jobs.
func (s *Server) persistAgentJobs(entry *agentSessionEntry, thread *agentThread, owner *agentJobs) {
	store := s.agentStore()
	if store == nil {
		return
	}
	thread.persistMu.Lock()
	defer thread.persistMu.Unlock()
	s.agentSessionsMu.Lock()
	current := s.agentSessions[entry.deviceID] == entry && entry.threads[thread.id] == thread && thread.jobs == owner
	model := thread.model
	s.agentSessionsMu.Unlock()
	if !current {
		return
	}
	snapshots, revision := owner.snapshotForStorage()
	if snapshots == nil {
		return
	}
	if err := store.saveJobs(entry.deviceID, thread.id, model, snapshots); err != nil {
		log.Printf("agent jobs: save %s/%s: %v", entry.deviceID, thread.id, err)
		return
	}
	owner.mu.Lock()
	owner.savedRevision = revision
	owner.mu.Unlock()
}

// Keep an idle entry warm until its newest lifecycle revision is on disk.
// This closes the completion -> callback scheduling -> eviction race.
func (m *agentJobs) pendingPersistence() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.persistenceEnabled && m.revision != m.savedRevision
}

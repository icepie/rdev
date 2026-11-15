package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"
)

// Fixed categories avoid retaining paths, credentials or unbounded error labels.
type serverHealth struct {
	startedAt         time.Time
	deviceFailures    atomic.Uint64
	transportFailures atomic.Uint64
}

func (s *Server) recordTransferFailure(code string) {
	if code == "device" {
		s.health.deviceFailures.Add(1)
	} else {
		s.health.transportFailures.Add(1)
	}
}

type healthResources struct {
	Devices        int `json:"devices"`
	Sessions       int `json:"sessions"`
	Forwards       int `json:"forwards"`
	Transfers      int `json:"transfers"`
	AgentTransfers int `json:"agentTransfers"`
	AgentRunning   int `json:"agentRunning"`
	AgentQueued    int `json:"agentQueued"`
}

type healthSnapshot struct {
	Status           string                  `json:"status"`
	At               time.Time               `json:"at"`
	UptimeSeconds    int64                   `json:"uptimeSeconds"`
	Resources        healthResources         `json:"resources"`
	Scheduler        SchedulerHealthSnapshot `json:"scheduler"`
	History          AgentHistoryHealth      `json:"history"`
	Identity         IdentityHealthSnapshot  `json:"identity"`
	TransferFailures struct {
		Device    uint64 `json:"device"`
		Transport uint64 `json:"transport"`
	} `json:"transferFailures"`
}

// Each subsystem is sampled independently: no nested resource locks and no disk
// scans on a scrape. Counts are observational, not an admission-control snapshot.
func (s *Server) healthSnapshot() healthSnapshot {
	h := healthSnapshot{Status: "ok", At: time.Now().UTC()}
	if !s.health.startedAt.IsZero() {
		h.UptimeSeconds = int64(time.Since(s.health.startedAt).Seconds())
	}
	s.mu.RLock()
	h.Resources.Devices = len(s.clients)
	s.mu.RUnlock()
	s.sessMu.RLock()
	h.Resources.Sessions = len(s.sessions)
	s.sessMu.RUnlock()
	s.fwdMu.RLock()
	h.Resources.Forwards = len(s.forwards)
	s.fwdMu.RUnlock()
	s.fileMu.RLock()
	h.Resources.Transfers = len(s.fileTasks)
	s.fileMu.RUnlock()
	s.agentFileMu.Lock()
	h.Resources.AgentTransfers = len(s.agentFileTasks)
	s.agentFileMu.Unlock()
	s.agentSessionsMu.Lock()
	for _, entry := range s.agentSessions {
		for _, thread := range entry.threads {
			if thread.running {
				h.Resources.AgentRunning++
			}
			h.Resources.AgentQueued += len(thread.queue)
		}
	}
	s.agentSessionsMu.Unlock()
	h.Scheduler = s.SchedulerHealth()
	h.History = s.AgentHistoryHealth()
	h.Identity = s.IdentityHealth()
	h.TransferFailures.Device = s.health.deviceFailures.Load()
	h.TransferFailures.Transport = s.health.transportFailures.Load()
	if h.Scheduler.PersistenceFault != "" || h.Scheduler.Stopped || h.History.Faulted > 0 || h.Identity.PersistenceFault {
		h.Status = "degraded"
	}
	return h
}

func healthReadMethod(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	return true
}

// HandleLiveness deliberately exposes no operational information or dependency
// checks. Readiness and detailed diagnostics require the normal admin boundary.
func (s *Server) HandleLiveness(w http.ResponseWriter, r *http.Request) {
	if !healthReadMethod(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte("ok\n"))
	}
}

func (s *Server) HandleReadiness(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuth(w, r) || !healthReadMethod(w, r) {
		return
	}
	h := s.healthSnapshot()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if h.Status != "ok" {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	if r.Method != http.MethodHead {
		_, _ = fmt.Fprintln(w, h.Status)
	}
}

func (s *Server) HandleHealth(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuth(w, r) || !healthReadMethod(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodHead {
		_ = json.NewEncoder(w).Encode(s.healthSnapshot())
	}
}

func (s *Server) HandleMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuth(w, r) || !healthReadMethod(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	if r.Method == http.MethodHead {
		return
	}
	h := s.healthSnapshot()
	gauge := func(name, help string, value int64) {
		_, _ = fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n%s %d\n", name, help, name, name, value)
	}
	ready := int64(0)
	if h.Status == "ok" {
		ready = 1
	}
	gauge("rdev_ready", "Whether initialized subsystems have no known storage faults.", ready)
	gauge("rdev_uptime_seconds", "Seconds since server initialization.", h.UptimeSeconds)
	gauge("rdev_devices", "Connected devices.", int64(h.Resources.Devices))
	gauge("rdev_sessions", "Active terminal and command sessions.", int64(h.Resources.Sessions))
	gauge("rdev_forwards", "Active forwards.", int64(h.Resources.Forwards))
	gauge("rdev_file_transfers", "Active browser and agent file transfer routes.", int64(h.Resources.Transfers+h.Resources.AgentTransfers))
	gauge("rdev_agent_turns", "Running agent turns.", int64(h.Resources.AgentRunning))
	gauge("rdev_agent_queued", "Queued agent messages.", int64(h.Resources.AgentQueued))
	gauge("rdev_schedule_pending", "Pending schedule runs.", int64(h.Scheduler.Pending))
	gauge("rdev_schedule_running", "Dispatching and running schedule runs.", int64(h.Scheduler.Running))
	gauge("rdev_schedule_storage_bytes", "Scheduler persisted state bytes.", h.Scheduler.StoredBytes)
	gauge("rdev_schedule_last_commit_bytes", "Bytes written by the last scheduler commit.", h.Scheduler.LastCommitBytes)
	identityFault := int64(0)
	if h.Identity.PersistenceFault {
		identityFault = 1
	}
	gauge("rdev_identity_storage_fault", "Whether device identity persistence has a known fault.", identityFault)
	schedulerFault := int64(0)
	if h.Scheduler.PersistenceFault != "" {
		schedulerFault = 1
	}
	gauge("rdev_schedule_storage_fault", "Whether scheduler persistence has a known fault.", schedulerFault)
	gauge("rdev_history_faults", "Known history storage faults.", int64(h.History.Faulted))
	_, _ = fmt.Fprintf(w, "# HELP rdev_transfer_failures_total File transfer failures observed since process start.\n# TYPE rdev_transfer_failures_total counter\nrdev_transfer_failures_total{reason=\"device\"} %d\nrdev_transfer_failures_total{reason=\"transport\"} %d\n", h.TransferFailures.Device, h.TransferFailures.Transport)
}

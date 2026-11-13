package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ScheduleRequest is the common wire request for schedule management. Action
// is deliberately explicit so the same endpoint can be used by the web UI,
// CLI and small integrations without inferring mutations from HTTP verbs.
type ScheduleRequest struct {
	Action     string        `json:"action"`
	ID         string        `json:"id,omitempty"`
	ScheduleID string        `json:"scheduleId,omitempty"`
	Revision   int           `json:"revision,omitempty"`
	Spec       *ScheduleSpec `json:"spec,omitempty"`
	Offset     int           `json:"offset,omitempty"`
	Limit      int           `json:"limit,omitempty"`
}

type scheduleAPIResponse struct {
	Data  any    `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

func scheduleJSON(w http.ResponseWriter, status int, data any, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err != nil {
		_ = json.NewEncoder(w).Encode(scheduleAPIResponse{Error: err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(scheduleAPIResponse{Data: data})
}

// HandleSchedules serves all durable schedule operations. AdminToken is
// required here even when the rest of the server is configured for anonymous
// access: schedules can execute commands and therefore must never have an
// unauthenticated fallback.
func (s *Server) HandleSchedules(w http.ResponseWriter, r *http.Request) {
	if s.AdminToken == "" || !s.authOK(r) {
		scheduleJSON(w, http.StatusUnauthorized, nil, fmt.Errorf("scheduler admin authentication required"))
		return
	}
	var req ScheduleRequest
	isGet := r.Method == http.MethodGet
	if isGet {
		q := r.URL.Query()
		req.Action = q.Get("action")
		req.ID = q.Get("id")
		req.ScheduleID = q.Get("scheduleId")
		req.Offset = parseScheduleInt(q.Get("offset"))
		req.Limit = parseScheduleInt(q.Get("limit"))
	} else {
		if r.Method != http.MethodPost {
			scheduleJSON(w, http.StatusMethodNotAllowed, nil, fmt.Errorf("method %s not allowed", r.Method))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
		dec := json.NewDecoder(r.Body)
		if err := dec.Decode(&req); err != nil {
			scheduleJSON(w, http.StatusBadRequest, nil, fmt.Errorf("invalid schedule request: %w", err))
			return
		}
	}
	req.Action = strings.ToLower(strings.TrimSpace(req.Action))
	if req.Action == "" {
		req.Action = "list"
	}
	if isGet {
		switch req.Action {
		case "list", "get", "runs", "run", "logs", "options":
		default:
			scheduleJSON(w, http.StatusMethodNotAllowed, nil, fmt.Errorf("GET cannot perform scheduler mutation %q", req.Action))
			return
		}
	}

	m, err := s.Scheduler()
	if err != nil {
		scheduleJSON(w, http.StatusServiceUnavailable, nil, err)
		return
	}
	m.mu.Lock()
	fault := m.usableLocked()
	m.mu.Unlock()
	if fault != nil {
		scheduleJSON(w, http.StatusServiceUnavailable, nil, fault)
		return
	}
	var data any
	switch req.Action {
	case "list":
		data = m.List()
	case "get":
		if req.ID == "" {
			scheduleJSON(w, http.StatusBadRequest, nil, fmt.Errorf("id is required"))
			return
		}
		data, err = m.Get(req.ID)
	case "create":
		if req.Spec == nil {
			scheduleJSON(w, http.StatusBadRequest, nil, fmt.Errorf("spec is required"))
			return
		}
		if !req.Spec.Authorized {
			scheduleJSON(w, http.StatusForbidden, nil, fmt.Errorf("explicit authorization is required"))
			return
		}
		data, err = m.Create(*req.Spec, "api")
	case "update":
		if req.ID == "" || req.Spec == nil {
			scheduleJSON(w, http.StatusBadRequest, nil, fmt.Errorf("id and spec are required"))
			return
		}
		data, err = m.Update(req.ID, *req.Spec, req.Revision, "api")
	case "pause", "enable", "delete", "run_now":
		if req.ID == "" {
			scheduleJSON(w, http.StatusBadRequest, nil, fmt.Errorf("id is required"))
			return
		}
		if req.Action == "enable" {
			if got, e := m.Get(req.ID); e == nil && !got.Authorized {
				scheduleJSON(w, http.StatusForbidden, nil, fmt.Errorf("explicit authorization is required"))
				return
			}
		}
		data, err = m.Action(req.ID, req.Action, "api")
	case "runs":
		id := req.ScheduleID
		runs := m.Runs(id)
		for i := range runs {
			runs[i].Output = ""
		}
		data = runs
		if data == nil {
			data = []ScheduleRun{}
		}
	case "run":
		if req.ID == "" {
			scheduleJSON(w, http.StatusBadRequest, nil, fmt.Errorf("run id is required"))
			return
		}
		data, err = m.Run(req.ID)
	case "logs":
		data, err = scheduleLogs(m, req)
	case "cancel":
		if req.ID == "" {
			scheduleJSON(w, http.StatusBadRequest, nil, fmt.Errorf("run id is required"))
			return
		}
		err = m.Cancel(req.ID)
		data = map[string]any{"id": req.ID, "canceled": err == nil}
	case "preview":
		if req.Spec == nil {
			scheduleJSON(w, http.StatusBadRequest, nil, fmt.Errorf("spec is required"))
			return
		}
		data, err = m.Preview(*req.Spec)
	case "options":
		data = s.scheduleOptions()
	default:
		scheduleJSON(w, http.StatusBadRequest, nil, fmt.Errorf("unknown scheduler action %q", req.Action))
		return
	}
	if err != nil {
		scheduleJSON(w, scheduleErrorStatus(err), nil, err)
		return
	}
	scheduleJSON(w, http.StatusOK, data, nil)
}

func parseScheduleInt(v string) int { n, _ := strconv.Atoi(v); return n }

func scheduleErrorStatus(err error) int {
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "not found"), strings.Contains(text, "unknown schedule"), strings.Contains(text, "unknown run"):
		return http.StatusNotFound
	case strings.Contains(text, "revision"), strings.Contains(text, "conflict"), strings.Contains(text, "already"):
		return http.StatusConflict
	case strings.Contains(text, "authoriz"), strings.Contains(text, "permission"):
		return http.StatusForbidden
	case strings.Contains(text, "invalid"), strings.Contains(text, "required"), strings.Contains(text, "unsupported"):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

type scheduleLogResponse struct {
	Run        ScheduleRun `json:"run"`
	Output     string      `json:"output"`
	NextOffset int         `json:"nextOffset"`
	Reset      bool        `json:"reset"`
	Truncated  bool        `json:"truncated"`
}

func scheduleLogs(m *Scheduler, req ScheduleRequest) (scheduleLogResponse, error) {
	runID := req.ID
	var run ScheduleRun
	var err error
	if runID != "" {
		run, err = m.Run(runID)
	} else if req.ScheduleID != "" {
		runs := m.Runs(req.ScheduleID)
		if len(runs) == 0 {
			return scheduleLogResponse{}, fmt.Errorf("no runs found for schedule %q", req.ScheduleID)
		}
		sort.SliceStable(runs, func(i, j int) bool {
			a, b := runs[i].StartedAt, runs[j].StartedAt
			if a.IsZero() {
				return false
			}
			if b.IsZero() {
				return true
			}
			return a.After(b)
		})
		run = runs[0]
		if run.StartedAt.IsZero() {
			return scheduleLogResponse{}, fmt.Errorf("no actual execution found for schedule %q", req.ScheduleID)
		}
	} else {
		return scheduleLogResponse{}, fmt.Errorf("id or scheduleId is required")
	}
	if err != nil {
		return scheduleLogResponse{}, err
	}
	output := run.Output
	base := run.OutputOffset
	off := req.Offset
	reset := false
	if off < base {
		off = base
		reset = true
	}
	local := off - base
	if local < 0 {
		local = 0
	}
	if local > len(output) {
		local = len(output)
	}
	// Byte offsets are accepted for efficient follow requests, but never split
	// a UTF-8 sequence. Move a middle offset to the next rune boundary.
	for local < len(output) && !utf8.RuneStart(output[local]) {
		local++
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 16384
	}
	if limit > 65536 {
		limit = 65536
	}
	end := local + limit
	if end > len(output) {
		end = len(output)
	}
	for end > local && end < len(output) && !utf8.RuneStart(output[end]) {
		end--
	}
	if end == local && local < len(output) {
		_, size := utf8.DecodeRuneInString(output[local:])
		end = local + size
	}
	next := base + end
	run.Output = ""
	return scheduleLogResponse{Run: run, Output: output[local:end], NextOffset: next, Reset: reset, Truncated: run.Truncated || next < base+len(output)}, nil
}

type scheduleDeviceOption struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Online bool   `json:"online"`
}
type scheduleModelOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type scheduleThreadOption struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Device string `json:"device"`
	Cwd    string `json:"cwd"`
	Model  string `json:"model"`
}
type scheduleOptionsResponse struct {
	Devices []scheduleDeviceOption `json:"devices"`
	Models  []scheduleModelOption  `json:"models"`
	Threads []scheduleThreadOption `json:"threads"`
}

func (s *Server) scheduleOptions() scheduleOptionsResponse {
	out := scheduleOptionsResponse{Devices: []scheduleDeviceOption{}, Models: []scheduleModelOption{}, Threads: []scheduleThreadOption{}}
	devices := make(map[string]bool)
	threads := make(map[string]bool)
	s.mu.RLock()
	for id, c := range s.clients {
		devices[id] = c != nil
	}
	s.mu.RUnlock()
	if s.Agent != nil {
		devices[agentServerDeviceID] = true
		seen := map[string]bool{}
		for _, p := range s.Agent.Providers {
			for _, model := range p.Models {
				id := p.ID + "/" + model.ID
				if !seen[id] {
					seen[id] = true
					name := model.Name
					if name == "" {
						name = id
					}
					out.Models = append(out.Models, scheduleModelOption{ID: id, Name: name})
				}
			}
		}
		sort.Slice(out.Models, func(i, j int) bool { return out.Models[i].ID < out.Models[j].ID })
	}
	s.agentSessionsMu.Lock()
	for device, entry := range s.agentSessions {
		if _, ok := devices[device]; !ok {
			devices[device] = false
		}
		for id, th := range entry.threads {
			threads[device+"/"+id] = true
			out.Threads = append(out.Threads, scheduleThreadOption{ID: id, Title: th.title, Device: device, Cwd: agentEffectiveCwdLocked(entry, th), Model: th.model})
		}
	}
	s.agentSessionsMu.Unlock()
	if store := s.agentStore(); store != nil {
		for _, device := range store.listDevices() {
			if device == sanitizeAgentHistoryName(agentServerDeviceID) {
				device = agentServerDeviceID
			}
			if _, ok := devices[device]; !ok {
				devices[device] = false
			}
			for _, meta := range store.listThreads(device) {
				if !threads[device+"/"+meta.ID] {
					out.Threads = append(out.Threads, scheduleThreadOption{ID: meta.ID, Title: meta.Title, Device: device, Cwd: meta.Cwd, Model: meta.Model})
				}
			}
		}
	}
	if scheduler, err := s.Scheduler(); err == nil {
		for _, task := range scheduler.List() {
			if _, ok := devices[task.Device]; !ok {
				devices[task.Device] = false
			}
		}
	}
	for id, online := range devices {
		out.Devices = append(out.Devices, scheduleDeviceOption{ID: id, Name: id, Online: online})
	}
	sort.Slice(out.Devices, func(i, j int) bool { return out.Devices[i].ID < out.Devices[j].ID })
	sort.Slice(out.Threads, func(i, j int) bool {
		if out.Threads[i].Device != out.Threads[j].Device {
			return out.Threads[i].Device < out.Threads[j].Device
		}
		return out.Threads[i].ID < out.Threads[j].ID
	})
	return out
}

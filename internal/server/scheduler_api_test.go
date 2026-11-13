package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func schedulerAPIFixture(t *testing.T) (*Server, *Scheduler, ScheduleRun) {
	t.Helper()
	s := NewServer()
	s.DataDir = t.TempDir()
	s.AdminToken = "admin-secret"
	m, err := s.Scheduler()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.StopScheduler)
	sc := Schedule{ID: "task-1", Revision: 1, State: "enabled", ScheduleSpec: ScheduleSpec{Name: "task", Kind: "shell", Device: "dev-1", Command: "echo task", Cron: "* * * * *", Timezone: "UTC", Authorized: true, Enabled: true}}
	run := ScheduleRun{ID: "run-1", ScheduleID: sc.ID, Name: sc.Name, Kind: sc.Kind, Device: sc.Device, State: "succeeded", ScheduleRevision: 1, ScheduledAt: time.Now().UTC(), StartedAt: time.Now().UTC(), FinishedAt: time.Now().UTC(), Snapshot: sc.ScheduleSpec, Output: "界界done", OutputOffset: 3, Truncated: true}
	m.mu.Lock()
	m.state.Schedules[sc.ID] = sc
	m.state.Runs[run.ID] = run
	if err := m.saveLocked(); err != nil {
		m.mu.Unlock()
		t.Fatal(err)
	}
	m.mu.Unlock()
	return s, m, run
}

func schedulerAPIRequest(s *Server, method, body, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/schedules", strings.NewReader(body))
	if token != "" {
		req.Header.Set("X-RDev-Token", token)
	}
	w := httptest.NewRecorder()
	s.HandleSchedules(w, req)
	return w
}

func TestHandleSchedulesRequiresConfiguredAdminToken(t *testing.T) {
	s := NewServer()
	if got := schedulerAPIRequest(s, http.MethodPost, `{"action":"list"}`, "admin-secret").Code; got != http.StatusUnauthorized {
		t.Fatalf("missing configured token status=%d", got)
	}
	s.AdminToken = "admin-secret"
	if got := schedulerAPIRequest(s, http.MethodPost, `{"action":"list"}`, "wrong").Code; got != http.StatusUnauthorized {
		t.Fatalf("bad token status=%d", got)
	}
}

func TestHandleSchedulesReadAndMutationMethodScope(t *testing.T) {
	s, _, _ := schedulerAPIFixture(t)
	w := schedulerAPIRequest(s, http.MethodGet, "", "admin-secret")
	if w.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", w.Code, w.Body)
	}
	var envelope struct {
		Data []Schedule `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data == nil || len(envelope.Data) != 1 || envelope.Data[0].ID != "task-1" {
		t.Fatalf("list=%+v", envelope.Data)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/schedules?action=pause&id=task-1", nil)
	req.Header.Set("X-RDev-Token", "admin-secret")
	w = httptest.NewRecorder()
	s.HandleSchedules(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET mutation status=%d body=%s", w.Code, w.Body)
	}
}

func TestHandleSchedulesLogsUsesUTF8SafeAbsoluteCursor(t *testing.T) {
	s, _, _ := schedulerAPIFixture(t)
	w := schedulerAPIRequest(s, http.MethodPost, `{"action":"logs","id":"run-1","offset":0,"limit":1}`, "admin-secret")
	if w.Code != http.StatusOK {
		t.Fatalf("logs status=%d body=%s", w.Code, w.Body)
	}
	var envelope struct {
		Data struct {
			Run        ScheduleRun `json:"run"`
			Output     string      `json:"output"`
			NextOffset int         `json:"nextOffset"`
			Reset      bool        `json:"reset"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.Data.Reset || envelope.Data.Output == "" || !utf8.ValidString(envelope.Data.Output) || envelope.Data.NextOffset <= 3 {
		t.Fatalf("log page=%+v", envelope.Data)
	}
	if envelope.Data.Run.Output != "" {
		t.Fatal("logs response duplicated full output in run summary")
	}
}

func TestHandleSchedulesRunsOmitsOutput(t *testing.T) {
	s, _, _ := schedulerAPIFixture(t)
	w := schedulerAPIRequest(s, http.MethodPost, `{"action":"runs","scheduleId":"task-1"}`, "admin-secret")
	if w.Code != http.StatusOK {
		t.Fatalf("runs status=%d body=%s", w.Code, w.Body)
	}
	var envelope struct {
		Data []ScheduleRun `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data) != 1 || envelope.Data[0].Output != "" {
		t.Fatalf("runs=%+v", envelope.Data)
	}
}

func TestScheduledToolsCannotReadOtherTasks(t *testing.T) {
	s, m, own := schedulerAPIFixture(t)
	other := own
	other.ID, other.ScheduleID, other.Output = "other-run", "other-task", "private output"
	m.mu.Lock()
	m.state.Runs[other.ID] = other
	m.mu.Unlock()
	owner := &agentSessionEntry{deviceID: agentServerDeviceID}
	tool := &scheduleRunsTool{srv: s, owner: owner, scheduleID: own.ScheduleID}
	for _, action := range []string{"get", "logs"} {
		args, _ := json.Marshal(map[string]string{"action": action, "id": other.ID})
		if _, err := tool.Execute(t.Context(), args); err == nil {
			t.Fatalf("scheduled %s read another task's execution", action)
		}
	}
	result, err := tool.Execute(t.Context(), json.RawMessage(`{"action":"list"}`))
	if err != nil || strings.Contains(result, other.ID) || !strings.Contains(result, own.ID) {
		t.Fatalf("self-scoped list=%s err=%v", result, err)
	}
	if tools := s.scheduleAgentTools(owner, &agentThread{scheduled: &scheduledAgentTurn{}}); len(tools) != 0 {
		t.Fatal("empty scheduled identity exposed tools")
	}
}

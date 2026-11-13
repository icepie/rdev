package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"rdev/internal/agent"
)

// scheduleAgentTools returns durable schedule capabilities for one conversation.
// Device conversations are scoped to their owner; server Agent conversations
// are the authenticated coordinator and may manage all configured devices.
func (s *Server) scheduleAgentTools(entry *agentSessionEntry, th *agentThread) []agent.Tool {
	if entry == nil {
		return nil
	}
	if th != nil && th.scheduled != nil {
		id := th.scheduled.scheduleID
		if id == "" {
			return nil
		}
		return []agent.Tool{&scheduleQueryTool{srv: s, owner: entry, scheduleID: id}, &scheduleRunsTool{srv: s, owner: entry, scheduleID: id}}
	}
	return []agent.Tool{
		&scheduleQueryTool{srv: s, owner: entry},
		&scheduleManagementTool{srv: s, owner: entry, thread: th},
		&scheduleRunsTool{srv: s, owner: entry},
		&cancelScheduleRunTool{srv: s, owner: entry},
	}
}

type scheduleQueryTool struct {
	srv        *Server
	owner      *agentSessionEntry
	scheduleID string
}

func (*scheduleQueryTool) Name() string    { return "schedules" }
func (*scheduleQueryTool) Dangerous() bool { return false }
func (*scheduleQueryTool) Description() string {
	return "List or inspect durable schedules. Read-only and does not execute anything. Results include exact task links under /tasks/."
}
func (*scheduleQueryTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"action":{"type":"string","enum":["list","get"]},"id":{"type":"string"}},"required":["action"],"additionalProperties":false}`)
}
func (t *scheduleQueryTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var a struct{ Action, ID string }
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid schedule arguments: %w", err)
	}
	a.Action = strings.ToLower(strings.TrimSpace(a.Action))
	m, err := t.srv.Scheduler()
	if err != nil {
		return "", err
	}
	switch a.Action {
	case "list":
		all := m.List()
		out := make([]Schedule, 0, len(all))
		links := map[string]string{}
		for _, sc := range all {
			if t.scheduleID != "" && sc.ID != t.scheduleID {
				continue
			}
			if t.canRead(sc) {
				out = append(out, sc)
				links[sc.ID] = "/tasks/" + sc.ID
			}
		}
		return scheduleToolJSON(map[string]any{"schedules": out, "links": links})
	case "get":
		if a.ID == "" {
			a.ID = t.scheduleID
		}
		if a.ID == "" {
			return "", fmt.Errorf("id is required")
		}
		if t.scheduleID != "" && a.ID != t.scheduleID {
			return "", fmt.Errorf("schedule is outside this scheduled turn")
		}
		sc, err := m.Get(a.ID)
		if err != nil {
			return "", err
		}
		if !t.canRead(sc) {
			return "", fmt.Errorf("schedule is outside this conversation's device scope")
		}
		return scheduleToolJSON(map[string]any{"schedule": sc, "url": "/tasks/" + sc.ID})
	default:
		return "", fmt.Errorf("unsupported schedule action %q", a.Action)
	}
}
func (t *scheduleQueryTool) canRead(sc Schedule) bool {
	return (t.scheduleID == "" || sc.ID == t.scheduleID) && (t.owner.deviceID == agentServerDeviceID || sc.Device == t.owner.deviceID)
}

type scheduleManagementTool struct {
	srv    *Server
	owner  *agentSessionEntry
	thread *agentThread
}

func (*scheduleManagementTool) Name() string    { return "manage_schedules" }
func (*scheduleManagementTool) Dangerous() bool { return true }
func (*scheduleManagementTool) Description() string {
	return "Create, update, pause, enable, delete, or run a durable unattended shell/Agent schedule. Explicit authorization is required for repeated execution. Device conversations can only manage schedules on their own device."
}
func (*scheduleManagementTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"action":{"type":"string","enum":["create","update","pause","enable","delete","run_now"]},"id":{"type":"string"},"revision":{"type":"integer","minimum":1},"spec":{"type":"object","properties":{"name":{"type":"string"},"kind":{"type":"string","enum":["shell","agent"]},"device":{"type":"string"},"cwd":{"type":"string"},"cron":{"type":"string"},"at":{"type":"string"},"timezone":{"type":"string"},"command":{"type":"string"},"sessionMode":{"type":"string","enum":["new","existing"]},"thread":{"type":"string"},"model":{"type":"string"},"reasoning":{"type":"string"},"prompt":{"type":"string"},"allowedTools":{"type":"array","items":{"type":"string"}},"timeoutSeconds":{"type":"integer","minimum":1},"tokenBudget":{"type":"integer","minimum":1},"busyPolicy":{"type":"string","enum":["skip","wait"]},"maxWaitSeconds":{"type":"integer","minimum":1},"maxRuns":{"type":"integer","minimum":0},"expiresAt":{"type":"string"},"enabled":{"type":"boolean"},"authorized":{"type":"boolean"},"misfirePolicy":{"type":"string","enum":["skip","latest"]},"graceSeconds":{"type":"integer","minimum":1},"mutexGroup":{"type":"string"}},"additionalProperties":false}},"required":["action"],"additionalProperties":false}`)
}

type scheduleToolArgs struct {
	Action   string        `json:"action"`
	ID       string        `json:"id"`
	Revision int           `json:"revision"`
	Spec     *ScheduleSpec `json:"spec"`
}

func (t *scheduleManagementTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var a scheduleToolArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid schedule arguments: %w", err)
	}
	a.Action = strings.ToLower(strings.TrimSpace(a.Action))
	m, err := t.srv.Scheduler()
	if err != nil {
		return "", err
	}
	switch a.Action {
	case "create":
		if a.Spec == nil {
			return "", fmt.Errorf("spec is required")
		}
		spec := *a.Spec
		if spec.Device == "" {
			spec.Device = t.owner.deviceID
		}
		if !t.canDevice(spec.Device) {
			return "", fmt.Errorf("schedule device is outside this conversation's scope")
		}
		if spec.Kind == "agent" && spec.SessionMode == "existing" && spec.Thread == "" && t.thread != nil {
			spec.Thread = t.thread.id
		}
		if !spec.Authorized {
			return "", fmt.Errorf("explicit unattended execution authorization is required")
		}
		sc, err := m.Create(spec, "agent")
		if err != nil {
			return "", err
		}
		return scheduleToolJSON(map[string]any{"schedule": sc, "url": "/tasks/" + sc.ID})
	case "update":
		if a.ID == "" || a.Spec == nil {
			return "", fmt.Errorf("id and spec are required")
		}
		old, err := m.Get(a.ID)
		if err != nil {
			return "", err
		}
		if !t.canRead(old) {
			return "", fmt.Errorf("schedule is outside this conversation's device scope")
		}
		spec := *a.Spec
		if spec.Device == "" {
			spec.Device = old.Device
		}
		if !t.canDevice(spec.Device) {
			return "", fmt.Errorf("schedule device is outside this conversation's scope")
		}
		if !spec.Authorized {
			return "", fmt.Errorf("explicit unattended execution authorization is required")
		}
		sc, err := m.Update(a.ID, spec, a.Revision, "agent")
		if err != nil {
			return "", err
		}
		return scheduleToolJSON(map[string]any{"schedule": sc, "url": "/tasks/" + sc.ID})
	case "pause", "enable", "delete", "run_now":
		if a.ID == "" {
			return "", fmt.Errorf("id is required")
		}
		sc, err := m.Get(a.ID)
		if err != nil {
			return "", err
		}
		if !t.canRead(sc) {
			return "", fmt.Errorf("schedule is outside this conversation's device scope")
		}
		result, err := m.Action(a.ID, a.Action, "agent")
		if err != nil {
			return "", err
		}
		return scheduleToolJSON(map[string]any{"result": result, "url": "/tasks/" + a.ID})
	default:
		return "", fmt.Errorf("unsupported schedule action %q", a.Action)
	}
}
func (t *scheduleManagementTool) canDevice(device string) bool {
	return t.owner.deviceID == agentServerDeviceID || device == t.owner.deviceID
}
func (t *scheduleManagementTool) canRead(sc Schedule) bool { return t.canDevice(sc.Device) }

type scheduleRunsTool struct {
	srv        *Server
	owner      *agentSessionEntry
	scheduleID string
}

func (*scheduleRunsTool) Name() string    { return "schedule_runs" }
func (*scheduleRunsTool) Dangerous() bool { return false }
func (*scheduleRunsTool) Description() string {
	return "List executions, inspect a run, or read bounded output logs for schedules in this conversation's device scope."
}
func (*scheduleRunsTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"action":{"type":"string","enum":["list","get","logs"]},"id":{"type":"string"},"scheduleId":{"type":"string"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1,"maximum":65536}},"required":["action"],"additionalProperties":false}`)
}
func (t *scheduleRunsTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var a struct {
		Action, ID, ScheduleID string
		Offset, Limit          int
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid run arguments: %w", err)
	}
	a.Action = strings.ToLower(strings.TrimSpace(a.Action))
	m, err := t.srv.Scheduler()
	if err != nil {
		return "", err
	}
	switch a.Action {
	case "list":
		if t.scheduleID != "" {
			a.ScheduleID = t.scheduleID
		}
		runs := m.Runs(a.ScheduleID)
		out := make([]ScheduleRun, 0, len(runs))
		for _, run := range runs {
			if t.canRead(run) {
				run.Output = ""
				out = append(out, run)
			}
		}
		return scheduleToolJSON(map[string]any{"runs": out})
	case "get":
		if a.ID == "" {
			return "", fmt.Errorf("run id is required")
		}
		run, err := m.Run(a.ID)
		if err != nil {
			return "", err
		}
		if !t.canRead(run) {
			return "", fmt.Errorf("run is outside this conversation's device scope")
		}
		return scheduleToolJSON(map[string]any{"run": run, "url": "/tasks/runs/" + run.ID})
	case "logs":
		if a.ID == "" && a.ScheduleID == "" {
			a.ScheduleID = t.scheduleID
		}
		if a.ID == "" && a.ScheduleID == "" {
			return "", fmt.Errorf("id or scheduleId is required")
		}
		lr, err := scheduleLogs(m, ScheduleRequest{ID: a.ID, ScheduleID: a.ScheduleID, Offset: a.Offset, Limit: a.Limit})
		if err != nil {
			return "", err
		}
		if !t.canRead(lr.Run) {
			return "", fmt.Errorf("run is outside this conversation's device scope")
		}
		return scheduleToolJSON(lr)
	default:
		return "", fmt.Errorf("unsupported run action %q", a.Action)
	}
}
func (t *scheduleRunsTool) canRead(run ScheduleRun) bool {
	return (t.scheduleID == "" || run.ScheduleID == t.scheduleID) && (t.owner.deviceID == agentServerDeviceID || run.Device == t.owner.deviceID)
}

type cancelScheduleRunTool struct {
	srv   *Server
	owner *agentSessionEntry
}

func (*cancelScheduleRunTool) Name() string    { return "cancel_schedule_run" }
func (*cancelScheduleRunTool) Dangerous() bool { return true }
func (*cancelScheduleRunTool) Description() string {
	return "Cancel an active scheduled execution in this conversation's device scope; it does not pause or delete the schedule."
}
func (*cancelScheduleRunTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`)
}
func (t *cancelScheduleRunTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", err
	}
	if a.ID == "" {
		return "", fmt.Errorf("run id is required")
	}
	m, err := t.srv.Scheduler()
	if err != nil {
		return "", err
	}
	run, err := m.Run(a.ID)
	if err != nil {
		return "", err
	}
	if t.owner.deviceID != agentServerDeviceID && run.Device != t.owner.deviceID {
		return "", fmt.Errorf("run is outside this conversation's device scope")
	}
	if err = m.Cancel(a.ID); err != nil {
		return "", err
	}
	return scheduleToolJSON(map[string]any{"runId": a.ID, "canceled": true})
}
func scheduleToolJSON(v any) (string, error) { b, err := json.Marshal(v); return string(b), err }

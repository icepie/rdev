package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Only server-mode conversations receive these tools. Mutations use the same
// lifecycle as the UI (persistence locks, cancellation and subscriber updates).
type agentSessionsTool struct {
	srv    *Server
	owner  *agentSessionEntry
	thread string
	mutate bool
}

func (t *agentSessionsTool) Name() string {
	if t.mutate {
		return "manage_agent_session"
	}
	return "agent_sessions"
}
func (t *agentSessionsTool) Dangerous() bool { return t.mutate }
func (t *agentSessionsTool) Description() string {
	if t.mutate {
		return "Create, rename or delete an Agent conversation on an explicitly named device, including offline saved conversations. Does not send messages or execute device commands. Deletion cancels that conversation's work and removes history. Cannot delete the conversation making this call. Use agent_sessions to discover exact device/thread IDs first."
	}
	return "List Agent conversations across devices or read paginated conversation history, including offline saved history. Omit device to list all devices. Reading requires explicit device and thread IDs. This does not switch the user's active conversation."
}
func (t *agentSessionsTool) Schema() json.RawMessage {
	if t.mutate {
		return json.RawMessage(`{"type":"object","properties":{"action":{"type":"string","enum":["create","rename","delete"]},"device":{"type":"string","minLength":1},"thread":{"type":"string"},"title":{"type":"string","maxLength":200}},"required":["action","device"],"additionalProperties":false}`)
	}
	return json.RawMessage(`{"type":"object","properties":{"action":{"type":"string","enum":["list","read"]},"device":{"type":"string"},"thread":{"type":"string"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1,"maximum":100}},"required":["action"],"additionalProperties":false}`)
}
func (t *agentSessionsTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if t.owner == nil || t.owner.deviceID != agentServerDeviceID {
		return "", fmt.Errorf("conversation management is only available in server Agent mode")
	}
	var a struct {
		Action, Device, Thread, Title string
		Offset, Limit                 int
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", err
	}
	if (!t.mutate && a.Action != "list" && a.Action != "read") || (t.mutate && a.Action != "create" && a.Action != "rename" && a.Action != "delete") {
		return "", fmt.Errorf("unsupported session action")
	}
	if a.Action == "list" {
		groups := t.srv.collectAllThreads()
		if a.Device != "" {
			filtered := make([]agentDeviceThreads, 0, 1)
			for _, g := range groups {
				if g.Device == a.Device {
					filtered = append(filtered, g)
				}
			}
			groups = filtered
		}
		return marshalJobResult(groups)
	}
	if a.Device == "" {
		return "", fmt.Errorf("device is required")
	}
	if a.Action != "create" && a.Thread == "" {
		return "", fmt.Errorf("thread is required")
	}
	if a.Action == "delete" && a.Device == t.owner.deviceID && a.Thread == t.thread {
		return "", fmt.Errorf("cannot delete the conversation making this call")
	}
	a.Title = strings.TrimSpace(a.Title)
	if (a.Action == "rename" && a.Title == "") || len([]rune(a.Title)) > 200 {
		return "", fmt.Errorf("title must contain 1 to 200 characters")
	}
	_, online := t.srv.GetClient(a.Device)
	known := a.Device == agentServerDeviceID || online
	if !known {
		t.srv.agentSessionsMu.Lock()
		known = t.srv.agentSessions[a.Device] != nil
		t.srv.agentSessionsMu.Unlock()
		if !known {
			if store := t.srv.agentStore(); store != nil {
				known = len(store.listThreads(a.Device)) > 0
			}
		}
	}
	if !known {
		return "", fmt.Errorf("unknown device %q; use agent_sessions list", a.Device)
	}
	handler := &agentWSHandler{srv: t.srv, adminBypass: t.srv.AdminToken != ""}
	entry, err := handler.threadActionEntry(&agentConn{deviceID: t.owner.deviceID, entry: t.owner}, a.Device)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var response agentMsg
	conn := &agentConn{deviceID: a.Device, entry: entry, reply: func(msg agentMsg) { response = msg }}
	switch a.Action {
	case "read":
		handler.handlePeekThread(conn, a.Thread)
	case "create":
		handler.handleNewThread(conn)
		a.Thread = response.Thread
		if response.Op != "error" && a.Title != "" {
			handler.handleRenameThread(conn, a.Thread, a.Title)
		}
	case "rename":
		handler.handleRenameThread(conn, a.Thread, a.Title)
	case "delete":
		handler.handleDeleteThread(conn, a.Thread)
	}
	if response.Op == "error" {
		return "", fmt.Errorf("%s", response.Message)
	}
	if a.Action == "read" {
		if a.Offset < 0 || a.Limit < 0 || a.Limit > 100 {
			return "", fmt.Errorf("invalid history pagination")
		}
		if a.Limit == 0 {
			a.Limit = 30
		}
		total := len(response.History)
		start := min(a.Offset, total)
		end := min(start+a.Limit, total)
		return marshalJobResult(map[string]any{"device": a.Device, "thread": a.Thread, "history": response.History[start:end], "offset": start, "total": total, "hasMore": end < total, "running": response.Running})
	}
	return marshalJobResult(map[string]any{"device": a.Device, "thread": a.Thread, "action": a.Action, "status": "completed"})
}

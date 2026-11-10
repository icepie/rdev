package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"rdev/internal/agent"
)

type agentParticipant struct {
	Device     string `json:"device"`
	Cwd        string `json:"cwd,omitempty"`
	Online     bool   `json:"online"`
	Authorized bool   `json:"authorized"`
	Primary    bool   `json:"primary,omitempty"`
}

type agentDeviceScope struct {
	mu      sync.Mutex
	srv     *Server
	primary string
	members map[string]string   // device -> independent working directory
	grants  map[string][32]byte // verified password fingerprint; never persisted
	active  map[uint64]scopedDeviceOperation
	next    uint64
}
type scopedDeviceOperation struct {
	device string
	cancel context.CancelFunc
}

func newAgentDeviceScope(s *Server, primary, cwd string) *agentDeviceScope {
	return &agentDeviceScope{srv: s, primary: primary, members: map[string]string{primary: cwd}, grants: make(map[string][32]byte), active: make(map[uint64]scopedDeviceOperation)}
}
func (s *agentDeviceScope) authorizedLocked(c *ClientConn) bool {
	if c.ID == s.primary || c.Password == "" {
		return true
	}
	grant, ok := s.grants[c.ID]
	return ok && grant == sha256.Sum256([]byte(c.Password))
}
func (s *agentDeviceScope) resolve(device string) (*ClientConn, string, error) {
	if device == "" {
		device = s.primary
	}
	c, ok := s.srv.GetClient(device)
	if !ok {
		return nil, "", fmt.Errorf("device %q is offline", device)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cwd, member := s.members[device]
	if !member {
		return nil, "", fmt.Errorf("device %q has not joined this conversation", device)
	}
	if !s.authorizedLocked(c) {
		return nil, "", fmt.Errorf("reauthorize device %q in conversation participants", device)
	}
	return c, cwd, nil
}
func (s *agentDeviceScope) begin(ctx context.Context, device string) (context.Context, func(), error) {
	if device == "" {
		device = s.primary
	}
	c, ok := s.srv.GetClient(device)
	if !ok {
		return nil, nil, fmt.Errorf("device %q is offline", device)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.members[device]; !ok || !s.authorizedLocked(c) {
		return nil, nil, fmt.Errorf("device %q is not authorized for this conversation", device)
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	s.next++
	id := s.next
	s.active[id] = scopedDeviceOperation{device, cancel}
	return ctx, func() { cancel(); s.mu.Lock(); delete(s.active, id); s.mu.Unlock() }, nil
}
func (s *agentDeviceScope) snapshot() []agentParticipant {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := make([]agentParticipant, 0, len(s.members))
	for id, cwd := range s.members {
		c, online := s.srv.GetClient(id)
		rows = append(rows, agentParticipant{Device: id, Cwd: cwd, Online: online, Authorized: online && s.authorizedLocked(c), Primary: id == s.primary})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Primary != rows[j].Primary {
			return rows[i].Primary
		}
		return rows[i].Device < rows[j].Device
	})
	return rows
}
func (s *agentDeviceScope) saved() []agentParticipant {
	rows := s.snapshot()
	for i := range rows {
		rows[i].Online = false
		rows[i].Authorized = false
	}
	return rows
}
func (s *agentDeviceScope) update(device, cwd, password string, admin, remove bool) error {
	if device == s.primary {
		return fmt.Errorf("the original device stays attached to this conversation")
	}
	if device == "" || device == agentServerDeviceID {
		return fmt.Errorf("select a remote device")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if remove {
		delete(s.members, device)
		delete(s.grants, device)
		for _, op := range s.active {
			if op.device == device {
				op.cancel()
			}
		}
		return nil
	}
	if len(s.members) >= 16 {
		if _, exists := s.members[device]; !exists {
			return fmt.Errorf("a conversation supports up to 16 devices")
		}
	}
	c, ok := s.srv.GetClient(device)
	if !ok {
		return fmt.Errorf("device is offline")
	}
	if !admin && c.Password != "" && !s.authorizedLocked(c) && subtle.ConstantTimeCompare([]byte(password), []byte(c.Password)) != 1 {
		return fmt.Errorf("device password is required or incorrect")
	}
	s.members[device] = cwd
	s.grants[device] = sha256.Sum256([]byte(c.Password))
	return nil
}

// Wrappers re-check membership at execution (including inside background jobs).
// Removing a member cancels active work and prevents queued work from starting.
type scopedAgentTool struct {
	agent.Tool
	scope  *agentDeviceScope
	device string
}

func (t *scopedAgentTool) Dangerous() bool {
	d, ok := t.Tool.(interface{ Dangerous() bool })
	return ok && d.Dangerous()
}
func (t *scopedAgentTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	ctx, finish, err := t.scope.begin(ctx, t.device)
	if err != nil {
		return "", err
	}
	defer finish()
	return t.Tool.Execute(ctx, args)
}
func (s *agentDeviceScope) tools(device string) ([]agent.Tool, error) {
	c, cwd, err := s.resolve(device)
	if err != nil {
		return nil, err
	}
	tools := newDeviceTools(s.srv, c, cwd)
	for i, tool := range tools {
		tools[i] = &scopedAgentTool{Tool: tool, scope: s, device: c.ID}
	}
	return tools, nil
}
func (h *agentWSHandler) handleParticipants(ac *agentConn, msg agentMsg) {
	if ac.entry == nil {
		return
	}
	h.srv.agentSessionsMu.Lock()
	thread := msg.Thread
	if thread == "" {
		thread = ac.current
	}
	th := ac.entry.threads[thread]
	h.srv.agentSessionsMu.Unlock()
	if th == nil || th.deviceScope == nil {
		ac.sendErrorThread(thread, "participants are available in device conversations")
		return
	}
	if msg.Mode != "list" {
		if msg.Mode != "add" && msg.Mode != "remove" {
			ac.sendErrorThread(thread, "unknown participant operation")
			return
		}
		if err := th.deviceScope.update(msg.Device, msg.Path, msg.Password, h.adminBypass, msg.Mode == "remove"); err != nil {
			ac.writeJSON(agentMsg{Op: "participants", Thread: thread, Participants: th.deviceScope.snapshot(), Error: err.Error(), ID: msg.ID})
			return
		}
		// Rebuild at the next turn boundary: joining an online device must
		// enable scoped tools even when the original device is offline.
		h.srv.agentSessionsMu.Lock()
		th.pendingReasoning = true
		h.srv.agentSessionsMu.Unlock()
		h.srv.writeAgentPrefs(ac.entry)
	}
	h.srv.agentBroadcast(ac.entry, agentMsg{Op: "participants", Thread: thread, Participants: th.deviceScope.snapshot(), ID: msg.ID})
}

func (h *agentWSHandler) hasOnlineParticipant(ac *agentConn, thread string) bool {
	if ac.entry == nil {
		return false
	}
	if thread == "" {
		thread = ac.current
	}
	h.srv.agentSessionsMu.Lock()
	th := ac.entry.threads[thread]
	var scope *agentDeviceScope
	if th != nil {
		scope = th.deviceScope
	}
	h.srv.agentSessionsMu.Unlock()
	if scope != nil {
		for _, row := range scope.snapshot() {
			if !row.Primary && row.Online && row.Authorized {
				return true
			}
		}
	}
	return false
}

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"rdev/internal/agent"
)

type automationView struct {
	Source string `json:"source,omitempty"`
	Kind   string `json:"kind"`
	Device string `json:"device"`
	Target string `json:"target,omitempty"`
	Base   string `json:"base,omitempty"`
	Action string `json:"action"`
	Paused bool   `json:"paused"`
}
type automationPage struct {
	browser *browserSession
	target  string
}
type agentAutomation struct {
	gpu          map[string]*gpuAgentDesktop
	desktop      map[string]*agentDesktopSession
	opMu         sync.Mutex
	mu           sync.Mutex
	manualInputs int // transient manual-input interruption, not a control mode
	paused       bool
	cancel       context.CancelFunc
	pages        map[string]automationPage
	screens      map[string]automationScreen
	view         automationView

	browserHistory []browserActionRecord
}

func (s *Server) automationTools(entry *agentSessionEntry, th *agentThread) []agent.Tool {
	if th.automation == nil {
		th.automation = &agentAutomation{pages: make(map[string]automationPage)}
	}
	base := automationTool{srv: s, entry: entry, thread: th, state: th.automation}
	result := []agent.Tool{&computerTool{automationTool: base}, &cuaTool{automationTool: base}, &autojsTool{automationTool: base}}
	if s.BrowserEnabled {
		result = append(result, &browserTool{automationTool: base})
	}
	return result
}

type automationTool struct {
	srv    *Server
	entry  *agentSessionEntry
	thread *agentThread
	state  *agentAutomation
}

func (*automationTool) Dangerous() bool { return true }
func (t *automationTool) client(device string) (*ClientConn, error) {
	if device == "" {
		device = t.entry.deviceID
	}
	if device == "" || device == agentServerDeviceID {
		return nil, fmt.Errorf("select an explicit connected device using list_devices")
	}
	if t.entry.deviceID != agentServerDeviceID && device != t.entry.deviceID {
		return nil, fmt.Errorf("this conversation is bound to device %q", t.entry.deviceID)
	}
	client, ok := t.srv.GetClient(device)
	if !ok {
		return nil, fmt.Errorf("device %q is offline", device)
	}
	if t.entry.deviceID == agentServerDeviceID && client.Password != "" && t.srv.AdminToken == "" {
		return nil, fmt.Errorf("an admin token is required for protected devices")
	}
	return client, nil
}
func (t *automationTool) begin(ctx context.Context) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if !t.state.opMu.TryLock() {
		return nil, nil, fmt.Errorf("another automation action is running in this conversation")
	}
	t.state.mu.Lock()
	defer t.state.mu.Unlock()
	if t.state.manualInputs > 0 {
		t.state.opMu.Unlock()
		return nil, nil, fmt.Errorf("manual desktop input is in progress; take a fresh screenshot before continuing")
	}
	if t.state.paused {
		t.state.opMu.Unlock()
		return nil, nil, fmt.Errorf("user has taken control; wait for user to resume automation")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	t.state.cancel = cancel
	return ctx, func() { cancel(); t.state.mu.Lock(); t.state.cancel = nil; t.state.mu.Unlock(); t.state.opMu.Unlock() }, nil
}
func (t *automationTool) publish(view automationView) {
	t.state.mu.Lock()
	view.Paused = t.state.paused
	t.state.view = view
	t.state.mu.Unlock()
	msg := agentMsg{Op: "automation", Thread: t.thread.id, Automation: &view}
	data, _ := json.Marshal(msg)
	t.srv.agentSessionsMu.Lock()
	current := t.entry.threads[t.thread.id] == t.thread && t.thread.automation == t.state
	if current {
		t.thread.live.Append(msg, data)
	}
	t.srv.agentSessionsMu.Unlock()
	if current {
		t.srv.agentBroadcastData(t.entry, data)
	}
}
func (h *agentWSHandler) handleAutomation(ac *agentConn, msg agentMsg) {
	h.srv.agentSessionsMu.Lock()
	th := ac.entry.threads[msg.Thread]
	if th == nil {
		h.srv.agentSessionsMu.Unlock()
		ac.sendErrorThread(msg.Thread, "conversation not found")
		return
	}
	state := th.automation
	h.srv.agentSessionsMu.Unlock()
	if state == nil {
		return
	}
	state.mu.Lock()
	state.paused = msg.Value
	if state.paused && state.cancel != nil {
		state.cancel()
	}
	view := state.view
	view.Paused = state.paused
	state.view = view
	state.mu.Unlock()
	if msg.Value {
		state.opMu.Lock()
		state.screens = nil
		closeDesktopAutomation(state)
		state.opMu.Unlock()
		h.srv.releaseAutomationDesktop(state)
	}
	h.srv.agentBroadcastTurn(ac.entry, th, agentMsg{Op: "automation", Automation: &view})
}
func automationSummary(raw json.RawMessage) string {
	var a struct{ Device, Action string }
	_ = json.Unmarshal(raw, &a)
	return a.Device + ": " + a.Action
}

func (s *Server) releaseAutomationDesktop(state *agentAutomation) {
	s.automationMu.Lock()
	defer s.automationMu.Unlock()
	for device, lease := range s.desktopOwners {
		if lease.owner == state {
			delete(s.desktopOwners, device)
		}
	}
}

// Retiring a conversation releases only its owned targets, never the shared
// device browser. Drain canceled input before making the screen available.
func (s *Server) retireAutomation(state *agentAutomation) {
	if state == nil {
		return
	}
	state.mu.Lock()
	state.paused = true
	if state.cancel != nil {
		state.cancel()
	}
	state.mu.Unlock()
	s.goAgent(func() {
		state.opMu.Lock()
		defer state.opMu.Unlock()
		s.releaseAutomationDesktop(state)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, page := range state.pages {
			if ctx.Err() != nil {
				break
			}
			c, err := dialBrowserCDP(ctx, page.browser, page.target)
			if err == nil {
				_ = c.call(ctx, "Page.close", map[string]any{}, nil)
				c.close()
			}
		}
		state.pages = nil
		state.screens = nil
		closeDesktopAutomation(state)
	})
}

// Caller holds opMu, so cleanup cannot interrupt a paired input release.
func closeDesktopAutomation(state *agentAutomation) {
	for _, d := range state.desktop {
		d.close()
	}
	state.desktop = nil
	for _, d := range state.gpu {
		d.close()
	}
	state.gpu = nil
}

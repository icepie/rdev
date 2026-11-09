package server

import (
	"fmt"
	"time"
)

// threadActionEntry allows authenticated management of offline history without
// switching the browser's active device or disrupting its live conversation.
func (h *agentWSHandler) threadActionEntry(ac *agentConn, device string) (*agentSessionEntry, error) {
	if device == "" || device == ac.deviceID {
		return ac.entry, nil
	}
	s := h.srv
	s.mu.RLock()
	client := s.clients[device]
	s.mu.RUnlock()
	if !h.adminBypass && (s.AdminToken != "" || client == nil || client.Password != "") {
		return nil, fmt.Errorf("administrator authentication is required to manage another device's conversations")
	}
	return s.agentSessionFor(device, client)
}

func (h *agentWSHandler) handleThreadAction(ac *agentConn, msg agentMsg) {
	device := msg.Device
	if device == "" {
		device = ac.deviceID
	}
	entry, err := h.threadActionEntry(ac, device)
	if err != nil {
		ac.writeJSON(agentMsg{Op: "thread_action_error", ID: msg.ID, Device: device, Thread: msg.Thread, Message: err.Error()})
		return
	}
	scoped := &agentConn{responseTo: ac, deviceID: device, entry: entry, responseThread: msg.Thread, responseID: msg.ID}
	if msg.Op == "delete_thread" {
		h.handleDeleteThread(scoped, msg.Thread)
	} else if msg.Op == "pin_thread" {
		h.handlePinThread(scoped, msg.Thread, msg.Pinned)
	} else {
		h.handleRenameThread(scoped, msg.Thread, msg.Title)
	}
}

func (h *agentWSHandler) handlePinThread(ac *agentConn, id string, pinned bool) {
	if ac.entry == nil {
		ac.sendError("agent session not ready")
		return
	}
	s := h.srv
	// Serialize persistence with other preference edits, without holding the
	// global state lock over disk I/O. Roll back failed writes before publishing.
	ac.entry.prefsMu.Lock()
	s.agentSessionsMu.Lock()
	th := ac.entry.threads[id]
	if th == nil {
		s.agentSessionsMu.Unlock()
		ac.entry.prefsMu.Unlock()
		ac.sendError("unknown thread")
		return
	}
	previous := th.pinnedAt
	if pinned && th.pinnedAt == 0 {
		th.pinnedAt = time.Now().UnixMilli()
	} else if !pinned {
		th.pinnedAt = 0
	}
	prefs := s.agentPrefsLocked(ac.entry)
	s.agentSessionsMu.Unlock()
	err := s.saveAgentPrefs(ac.deviceID, prefs)
	if err != nil {
		s.agentSessionsMu.Lock()
		if ac.entry.threads[id] == th {
			th.pinnedAt = previous
		}
		s.agentSessionsMu.Unlock()
	}
	ac.entry.prefsMu.Unlock()
	if err != nil {
		ac.sendError("could not save pinned conversation")
		return
	}
	ac.writeJSON(agentMsg{Op: "thread_pinned", Device: ac.deviceID, Thread: id, Pinned: pinned})
	s.broadcastThreadList(ac.entry)
}

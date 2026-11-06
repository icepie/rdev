package server

import "fmt"

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
	} else {
		h.handleRenameThread(scoped, msg.Thread, msg.Title)
	}
}

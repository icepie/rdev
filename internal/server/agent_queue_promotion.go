package server

import (
	"encoding/json"
	"strings"
)

// OMP promote_queued_message appends a follow-up to steering atomically. Use
// identities instead of text matching so identical drafts in two tabs stay safe.
func queueMode(item agentQueuedChat, model string) string {
	if item.steering {
		return "steer"
	}
	if len(item.display) != 0 {
		return "notification"
	}
	if strings.HasPrefix(strings.TrimSpace(item.text), "/") {
		return "command"
	}
	if item.model != "" && item.model != model {
		return "model"
	}
	return "follow_up"
}

func queueModes(th *agentThread) []string {
	modes := make([]string, len(th.queue))
	for i, item := range th.queue {
		modes[i] = queueMode(item, th.model)
		if modes[i] == "follow_up" && th.turnCommand {
			modes[i] = "turn_command"
		}
	}
	return modes
}

func (h *agentWSHandler) handlePromoteQueued(ac *agentConn, threadRef, queueID string) {
	entry := ac.entry
	if entry == nil {
		return
	}
	srv := h.srv
	srv.agentSessionsMu.Lock()
	threadID := ac.current
	if threadRef != "" {
		threadID = threadRef
	}
	th := entry.threads[threadID]
	message := "Queued message is no longer available."
	success := false
	if th != nil && queueID != "" {
		for i, item := range th.queue {
			if item.id != queueID {
				continue
			}
			if !th.running || (th.turnCtx != nil && th.turnCtx.Err() != nil) {
				message = "The current response has finished."
				break
			}
			if th.turnCommand {
				message = "The current session command must finish before guidance can run."
				break
			}
			switch queueMode(item, th.model) {
			case "steer":
				success = true // Idempotent while the item still exists.
			case "command":
				message = "Session commands must run as a separate turn."
			case "model":
				message = "This message requests a different model and must run as a separate turn."
			case "notification":
				message = "Background notifications cannot be promoted."
			case "follow_up":
				item = takeQueuedChat(&th.queue, i)
				item.steering = true
				item.display = json.RawMessage(`{"steering":true}`)
				index := 0
				for index < len(th.queue) && th.queue[index].steering {
					index++
				}
				th.queue = append(th.queue, agentQueuedChat{})
				copy(th.queue[index+1:], th.queue[index:])
				th.queue[index] = item
				if th.steeringWake != nil {
					close(th.steeringWake)
					th.steeringWake = nil
				}
				success = true
			}
			break
		}
	}
	srv.agentSessionsMu.Unlock()
	if success {
		message = ""
	}
	ac.writeJSON(agentMsg{Op: "queue_promoted", Thread: threadID, QueueID: queueID, IsError: !success, Message: message})
	if th != nil {
		srv.broadcastQueue(entry, th)
	}
}

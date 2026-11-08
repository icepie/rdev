package server

import (
	"slices"
	"strings"
)

// Edit in place under the same lock as admission. A message already claimed
// by the turn cannot be recreated by a delayed editor. Attachments and model
// stay with the original item; text updates never replace hidden image data.
func (h *agentWSHandler) handleQueueMutation(ac *agentConn, msg agentMsg) {
	if ac.entry == nil {
		return
	}
	s := h.srv
	s.agentSessionsMu.Lock()
	threadID := msg.Thread
	if threadID == "" {
		threadID = ac.current
	}
	th := ac.entry.threads[threadID]
	failure := "Queued message is no longer available."
	if th != nil {
		for i := range th.queue {
			item := &th.queue[i]
			if msg.QueueID == "" || item.id != msg.QueueID {
				continue
			}
			if item.steering || len(item.display) != 0 {
				failure = "Only pending follow-up messages can be edited or reordered."
				break
			}
			if msg.Op == "update_queue" {
				switch {
				case msg.ExpectedText == nil || *msg.ExpectedText != item.text:
					failure = "This queued message changed in another window. Reopen it before saving."
				case len(msg.Text) > 1024*1024:
					failure = "Queued message is too large."
				case strings.TrimSpace(msg.Text) == "" && len(item.images) == 0:
					failure = "Queued message cannot be empty."
				default:
					item.text = msg.Text
					failure = ""
				}
			} else {
				if !slices.Equal(msg.ExpectedQueueIDs, queueIDs(th.queue)) {
					failure = "The queue changed. Retry using the current order."
					break
				}
				target := len(th.queue)
				if msg.BeforeID != "" {
					target = -1
					for j, candidate := range th.queue {
						if candidate.id == msg.BeforeID {
							target = j
							break
						}
					}
				}
				if target < 0 || (target < len(th.queue) && (th.queue[target].steering || len(th.queue[target].display) != 0)) {
					failure = "Invalid queue destination."
					break
				}
				if target != i {
					moved := takeQueuedChat(&th.queue, i)
					if target > i {
						target--
					}
					th.queue = append(th.queue, agentQueuedChat{})
					copy(th.queue[target+1:], th.queue[target:])
					th.queue[target] = moved
				}
				failure = ""
			}
			break
		}
	}
	s.agentSessionsMu.Unlock()
	success := failure == ""
	ac.writeJSON(agentMsg{Op: "queue_updated", Thread: threadID, ID: msg.ID, QueueID: msg.QueueID, IsError: failure != "", Message: failure, Success: &success})
	if th != nil {
		s.broadcastQueue(ac.entry, th)
	}
}

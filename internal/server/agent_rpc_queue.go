package server

import (
	"encoding/json"
	"time"

	"rdev/internal/agent"
)

// OMP matches the first user message in the selected queue by text. The optional
// rdev queueId extension additionally pins that match to a stable identity so a
// stale multi-window action cannot consume another identical draft.
func (h *agentWSHandler) handleRPCRemoveQueued(ac *agentConn, request agentMsg, queue string) {
	if ac.entry == nil {
		h.writeRPCError(ac, request, "invalid_request", "unknown thread")
		return
	}
	threadID := request.Thread
	if threadID == "" {
		threadID = ac.current
	}
	s := h.srv
	s.agentSessionsMu.Lock()
	th := ac.entry.threads[threadID]
	if th == nil {
		s.agentSessionsMu.Unlock()
		h.writeRPCError(ac, request, "invalid_request", "unknown thread")
		return
	}
	var removed *agentQueuedChat
	for i, item := range th.queue {
		// System background-delivery messages are not user-editable queue entries.
		if !item.steering && len(item.display) > 0 {
			continue
		}
		if item.steering != (queue == "steering") || item.text != request.Message {
			continue
		}
		if request.QueueID != "" && item.id != request.QueueID {
			continue
		}
		value := takeQueuedChat(&th.queue, i)
		removed = &value
		ac.entry.lastActive = time.Now()
		break
	}
	s.agentSessionsMu.Unlock()
	result := rpcRemoveQueuedResult{Removed: removed != nil}
	if removed != nil {
		result.Images = rpcImageContents(removed.images)
	}
	raw, _ := json.Marshal(result)
	success := true
	ac.writeJSON(agentMsg{Type: "response", Command: request.Command, ID: request.ID, Success: &success, Data: raw})
	if removed != nil {
		s.broadcastQueue(ac.entry, th)
	}
}

type rpcImageContent struct {
	Type     string `json:"type"`
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type rpcRemoveQueuedResult struct {
	Removed bool              `json:"removed"`
	Images  []rpcImageContent `json:"images,omitempty"`
}

func rpcImageContents(images []agent.MessageImage) []rpcImageContent {
	result := make([]rpcImageContent, 0, len(images))
	for _, image := range images {
		result = append(result, rpcImageContent{Type: "image", MimeType: image.Mime, Data: image.Data})
	}
	return result
}

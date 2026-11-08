package server

import "encoding/json"

// queueAgentSteeringLocked inserts guidance before ordinary follow-ups. Caller
// holds agentSessionsMu.
func queueAgentSteeringLocked(th *agentThread, item agentQueuedChat) {
	index := 0
	for index < len(th.queue) && th.queue[index].steering {
		index++
	}
	item.steering = true
	item.display = json.RawMessage(`{"steering":true}`)
	th.queue = append(th.queue, agentQueuedChat{})
	copy(th.queue[index+1:], th.queue[index:])
	th.queue[index] = item
	if th.steeringWake != nil {
		close(th.steeringWake)
		th.steeringWake = nil
	}
}

// agentSteeringSignal observes queue ownership without draining it. A pending
// steer releases passive job waits immediately; otherwise admission wakes the
// current waiters. Regular tools and the background jobs themselves keep running.
func (s *Server) agentSteeringSignal(th *agentThread) <-chan struct{} {
	s.agentSessionsMu.Lock()
	defer s.agentSessionsMu.Unlock()
	for _, queued := range th.queue {
		if queued.steering {
			ready := make(chan struct{})
			close(ready)
			return ready
		}
	}
	if th.steeringWake == nil {
		th.steeringWake = make(chan struct{})
	}
	return th.steeringWake
}

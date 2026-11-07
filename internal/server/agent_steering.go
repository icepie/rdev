package server

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

package server

// Agent workgroup: server-wide accounting for asynchronous agent turns and
// jobs. Kept apart from the WebSocket layer, which is only one producer.

func (s *Server) goAgent(fn func()) {
	s.agentWG.Add(1)
	go func() {
		defer s.agentWG.Done()
		fn()
	}()
}

// waitAgentWork is intentionally small and safe for tests; production callers
// may use it while draining the server.
func (s *Server) waitAgentWork() { s.agentWG.Wait() }

package server

// These are rdev RPC extensions for shell jobs as well as subagents. Unlike
// prompt commands they do not acquire a foreground turn or mutate its history.
func (h *agentWSHandler) handleRPCJobs(ac *agentConn, request agentMsg) {
	threadID := request.Thread
	if threadID == "" {
		threadID = ac.current
	}
	var jobs *agentJobs
	if ac.entry != nil {
		h.srv.agentSessionsMu.Lock()
		if th := ac.entry.threads[threadID]; th != nil {
			jobs = th.jobs
		}
		h.srv.agentSessionsMu.Unlock()
	}
	if jobs == nil {
		h.writeRPCError(ac, request, "invalid_request", "unknown thread")
		return
	}
	var data any
	switch request.Command {
	case "get_jobs":
		data = struct {
			Jobs []agentJobSnapshot `json:"jobs"`
		}{jobs.List()}
	case "get_job":
		job, err := jobs.Read(request.JobID)
		if err != nil {
			h.writeRPCError(ac, request, "invalid_request", err.Error())
			return
		}
		data = struct {
			Job agentJobSnapshot `json:"job"`
		}{job}
	case "cancel_job":
		if request.JobID == "all" {
			jobs.CancelAll()
		} else if err := jobs.Cancel(request.JobID); err != nil {
			h.writeRPCError(ac, request, "invalid_request", err.Error())
			return
		}
		// Acceptance is not completion: workers retain their slots until exit.
		data = struct {
			CancellationRequested bool `json:"cancellationRequested"`
		}{true}
	}
	(&rpcTurnCompletion{conn: ac, request: request}).write(true, data, "", "")
}

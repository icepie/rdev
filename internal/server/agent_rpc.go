package server

import (
	"encoding/json"
	"fmt"
)

// rpcTurnCompletion keeps a native RPC request correlated with the one turn
// that fulfills it. It is intentionally connection-scoped: turn frames are
// broadcast, but the RPC response must never leak to another browser.
type rpcTurnCompletion struct {
	conn    *agentConn
	request agentMsg
}

func (r *rpcTurnCompletion) write(ok bool, data any, code, message string) {
	if r == nil || r.conn == nil {
		return
	}
	var success = ok
	frame := agentMsg{Type: "response", Command: r.request.Command, ID: r.request.ID, Success: &success}
	if ok {
		if raw, err := json.Marshal(data); err == nil {
			frame.Data = raw
		}
	} else {
		frame.Code, frame.Error = code, message
	}
	r.conn.writeJSON(frame)
}

// Keep RPC commands separate from browser operations. In particular, an
// unsupported RPC name must not fall through to a mutating browser operation
// that neither validates OMP fields nor returns a correlated RPC response.
func (h *agentWSHandler) handleAgentRPCFrame(ac *agentConn, raw []byte) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return false
	}
	kind, present := fields["type"]
	if !present {
		return false
	}
	// RPC uses the same authentication gate as native browser commands.
	if !ac.authed {
		return true
	}
	request := agentMsg{RPC: true}
	// Preserve a usable correlation ID even when another field is malformed.
	_ = json.Unmarshal(fields["id"], &request.ID)
	fail := func(code, message string) {
		success := false
		ac.writeJSON(agentMsg{Type: "response", Command: request.Command, ID: request.ID, Success: &success, Code: code, Error: message})
	}
	var name string
	if len(kind) == 0 || kind[0] != '"' || json.Unmarshal(kind, &name) != nil || name == "" {
		fail("invalid_request", "type must be a non-empty string")
		return true
	}
	request.Command = name
	if name == "request" { // Previously shipped rdev wrapper, not OMP's native frame.
		if err := rpcStringField(fields, "command", &request.Command, true); err != nil {
			fail("invalid_request", err.Error())
			return true
		}
	}
	if err := rpcStringField(fields, "id", &request.ID, false); err != nil {
		request.ID = ""
		fail("invalid_request", err.Error())
		return true
	}
	if err := rpcStringField(fields, "thread", &request.Thread, false); err != nil {
		fail("invalid_request", err.Error())
		return true
	}
	// Reject mixed envelopes, rather than silently choosing between operations.
	if _, ok := fields["op"]; ok {
		fail("invalid_request", "RPC frames must not contain op")
		return true
	}
	switch request.Command {
	case "get_subagents":
		h.handleGetSubagents(ac, request)
	case "get_subagent_messages":
		if err := rpcStringField(fields, "subagentId", &request.SubagentID, false); err != nil {
			fail("invalid_request", err.Error())
			return true
		}
		if err := rpcStringField(fields, "sessionFile", &request.SessionFile, false); err != nil {
			fail("invalid_request", err.Error())
			return true
		}
		if raw, ok := fields["fromByte"]; ok {
			var value int64
			if json.Unmarshal(raw, &value) != nil || value < 0 {
				fail("invalid_request", "fromByte must be a non-negative integer")
				return true
			}
			request.FromByte = &value
		}
		h.handleGetSubagentMessages(ac, request)
	case "steer_subagent", "cancel_subagent":
		if err := rpcStringField(fields, "subagentId", &request.SubagentID, true); err != nil {
			fail("invalid_request", err.Error())
			return true
		}
		if request.Command == "steer_subagent" {
			if err := rpcStringField(fields, "message", &request.Message, true); err != nil {
				fail("invalid_request", err.Error())
				return true
			}
			h.handleJobGuidance(ac, request)
		} else {
			h.handleCancelSubagent(ac, request)
		}
	case "compact":
		if err := rpcStringField(fields, "customInstructions", &request.CustomInstructions, false); err != nil {
			fail("invalid_request", err.Error())
			return true
		}
		h.handleRPCCompact(ac, request)
	default:
		fail("unsupported_command", fmt.Sprintf("unsupported RPC command %q", request.Command))
	}
	return true
}

func (h *agentWSHandler) handleRPCCompact(ac *agentConn, request agentMsg) {
	threadID := request.Thread
	if threadID == "" {
		threadID = ac.current
	}
	if ac.entry == nil {
		h.writeRPCError(ac, request, "invalid_request", "unknown thread")
		return
	}
	s := h.srv
	s.agentSessionsMu.Lock()
	th := ac.entry.threads[threadID]
	if th == nil {
		s.agentSessionsMu.Unlock()
		h.writeRPCError(ac, request, "invalid_request", "unknown thread")
		return
	}
	if th.running {
		s.agentSessionsMu.Unlock()
		h.writeRPCError(ac, request, "invalid_request", "conversation is busy")
		return
	}
	text := "/compact"
	if request.CustomInstructions != "" {
		text += " " + request.CustomInstructions
	}
	start := s.startAgentTurnLocked(ac.entry, th, text, nil)
	turn := snapshotAgentTurnLocked(th)
	turn.rpc = &rpcTurnCompletion{conn: ac, request: request}
	s.agentSessionsMu.Unlock()
	s.agentBroadcastTurn(ac.entry, th, start)
	s.goAgent(func() { s.runAgentTurn(ac.entry, th, turn, text, nil) })
}

func (h *agentWSHandler) writeRPCError(ac *agentConn, request agentMsg, code, message string) {
	success := false
	ac.writeJSON(agentMsg{Type: "response", Command: request.Command, ID: request.ID, Success: &success, Code: code, Error: message})
}

func rpcStringField(fields map[string]json.RawMessage, key string, dest *string, required bool) error {
	raw, exists := fields[key]
	if !exists && !required {
		return nil
	}
	if !exists || len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, dest) != nil || (required && *dest == "") {
		if required {
			return fmt.Errorf("%s must be a non-empty string", key)
		}
		return fmt.Errorf("%s must be a string", key)
	}
	return nil
}

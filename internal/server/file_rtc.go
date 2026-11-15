package server

import (
	"encoding/json"
	"sync/atomic"
	"time"

	"rdev/internal/protocol"
)

// File RTC never grants new operations: the authorized start owns the peer and
// every subsequent signal is bound to that browser and device connection.
func validFileRTCSignal(kind string, data json.RawMessage) bool {
	if kind == "close" {
		return len(data) == 0 || string(data) == "null" || string(data) == "{}"
	}
	if len(data) == 0 || len(data) > 64*1024 {
		return false
	}
	var signal struct {
		Type      string  `json:"type"`
		SDP       string  `json:"sdp"`
		Candidate *string `json:"candidate"`
	}
	if json.Unmarshal(data, &signal) != nil {
		return false
	}
	switch kind {
	case "offer", "answer":
		return signal.Type == kind && signal.SDP != ""
	case "candidate":
		return len(data) <= 8*1024 && signal.Candidate != nil
	default:
		return false
	}
}

func (h *filesWSHandler) prepareFileRTC(client *ClientConn, msg *fileMsg) {
	if h.srv.WebRTCEnabled && client.FileRTC && msg.RTCType == "offer" && validFileRTCSignal(msg.RTCType, msg.RTCData) {
		return
	}
	msg.RTCType, msg.RTCData = "", nil
}

func (s *Server) fileRouteAuthorized(route *fileTaskRoute) bool {
	if route == nil || route.client == nil {
		return false
	}
	current, ok := s.GetClient(route.deviceID)
	if !ok || current != route.client {
		return false
	}
	h := filesWSHandler{srv: s, adminBypass: route.socket.adminBypass}
	return h.isAuthorized(route.socket, current)
}

func (h *filesWSHandler) forwardFileRTCSignal(socket *fileSocket, msg fileMsg) {
	route := h.srv.getFileTask(msg.TaskID)
	if route == nil || route.socket != socket || !route.rtc || !h.srv.WebRTCEnabled || !h.srv.fileRouteAuthorized(route) {
		return
	}
	if (msg.RTCType != "candidate" && msg.RTCType != "close") || !validFileRTCSignal(msg.RTCType, msg.RTCData) {
		return
	}
	if msg.RTCType == "candidate" && atomic.AddUint32(&route.candidates, 1) > 128 {
		return
	}
	_ = route.client.Send(&protocol.Message{Type: protocol.MsgFileRTCSignal, TaskID: msg.TaskID, RTCType: msg.RTCType, RTCData: msg.RTCData})
	if msg.RTCType == "close" {
		h.srv.removeFileTask(msg.TaskID)
	}
}

func (s *Server) handleFileRTCSignal(client *ClientConn, msg *protocol.Message) {
	route := s.getFileTask(msg.TaskID)
	if route == nil || route.client != client || !route.rtc || !s.WebRTCEnabled || !s.fileRouteAuthorized(route) {
		return
	}
	if (msg.RTCType != "answer" && msg.RTCType != "candidate" && msg.RTCType != "close") || !validFileRTCSignal(msg.RTCType, msg.RTCData) {
		return
	}
	if msg.RTCType == "candidate" && atomic.AddUint32(&route.deviceCandidates, 1) > 128 {
		return
	}
	route.socket.writeText(fileMsg{Op: "rtc_signal", TaskID: msg.TaskID, RTCType: msg.RTCType, RTCData: msg.RTCData})
	if msg.RTCType == "close" {
		// Keep the route until explicit cancel: the browser still needs to stop
		// the old operation before starting its relay-only resume attempt.
		s.expireFileRTC(msg.TaskID, route)
	}
}

// Control text can overtake the direct ordered binary end marker. Keep the
// route until the browser acknowledges by closing, with bounded orphan cleanup.
func (s *Server) expireFileRTC(id string, route *fileTaskRoute) {
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	if s.fileTasks[id] != route || route.timer != nil {
		return
	}
	route.timer = time.AfterFunc(30*time.Second, func() {
		s.fileMu.Lock()
		owned := s.fileTasks[id] == route
		if owned {
			delete(s.fileTasks, id)
		}
		s.fileMu.Unlock()
		if owned {
			_ = route.client.Send(&protocol.Message{Type: protocol.MsgFileTransferCancel, TaskID: id})
		}
	})
}

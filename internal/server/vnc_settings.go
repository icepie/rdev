package server

import (
	"encoding/json"
	"net/http"

	"rdev/internal/protocol"
)

type vncSettingsRequest struct {
	Device       string `json:"device"`
	Mode         string `json:"mode,omitempty"`
	Source       string `json:"source,omitempty"`
	FPS          int    `json:"fps,omitempty"`
	Quality      int    `json:"quality,omitempty"`
	Width        int    `json:"width,omitempty"`
	Height       int    `json:"height,omitempty"`
	InputBackend string `json:"inputBackend,omitempty"`
	ShowCursor   bool   `json:"showCursor,omitempty"`
}

type vncSettingsResponse struct {
	OK             bool             `json:"ok"`
	Device         string           `json:"device"`
	VNCAddr        string           `json:"vncAddr,omitempty"`
	ClosedSessions int              `json:"closedSessions,omitempty"`
	Request        protocol.Message `json:"request"`
}

func (s *Server) HandleVNCSettingsAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuth(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req vncSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if req.Device == "" {
		http.Error(w, "missing device", http.StatusBadRequest)
		return
	}
	s.mu.RLock()
	_, ok := s.clients[req.Device]
	s.mu.RUnlock()
	if !ok {
		http.Error(w, "device not connected", http.StatusNotFound)
		return
	}
	request := protocol.Message{
		FPS:          req.FPS,
		Quality:      req.Quality,
		Width:        req.Width,
		Height:       req.Height,
		Source:       req.Source,
		InputBackend: req.InputBackend,
		ShowCursor:   req.ShowCursor,
	}
	if req.Mode != "manual" {
		base := defaultVNCDesktopRequest()
		base.Source = request.Source
		base.InputBackend = request.InputBackend
		base.ShowCursor = request.ShowCursor
		request = base
	}
	s.updateVNCDesktopRequest(req.Device, request)
	closedSessions := s.closeVNCForClient(req.Device)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(vncSettingsResponse{OK: true, Device: req.Device, VNCAddr: s.VNCAddr, ClosedSessions: closedSessions, Request: s.vncDesktopRequest(req.Device)})
}

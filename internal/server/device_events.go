package server

import (
	"fmt"
	"net/http"
	"time"
)

// Changes are invalidations rather than credentials or duplicate snapshots.
// A slow subscriber has at most one pending event and never blocks devices.
// Caller holds s.mu, which also owns registration and subscription lifetimes.
func (s *Server) notifyDeviceWatchersLocked() {
	for ch := range s.deviceWatchers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (s *Server) subscribeDeviceEvents() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	if s.deviceWatchers == nil {
		s.deviceWatchers = make(map[chan struct{}]struct{})
	}
	s.deviceWatchers[ch] = struct{}{}
	ch <- struct{}{} // Every reconnect refreshes the authoritative snapshot.
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.deviceWatchers, ch)
		s.mu.Unlock()
	}
}

func (s *Server) HandleDeviceEvents(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuth(w, r) || !requireOrigin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if _, ok := w.(http.Flusher); !ok {
		http.Error(w, "streaming unavailable", http.StatusInternalServerError)
		return
	}
	events, unsubscribe := s.subscribeDeviceEvents()
	defer unsubscribe()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	for {
		payload := "event: devices\ndata: {}\n\n"
		select {
		case <-r.Context().Done():
			return
		case <-events:
		case <-heartbeat.C:
			payload = ": keepalive\n\n"
		}
		_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := fmt.Fprint(w, payload); err != nil {
			return
		}
		if err := controller.Flush(); err != nil {
			return
		}
	}
}

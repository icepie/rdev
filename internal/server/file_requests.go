package server

import (
	"fmt"
	"time"
)

const fileRequestTimeout = 25 * time.Second
const fileRequestsPerSocket = 128
const fileRequestsMax = 4096

type fileRequestRoute struct {
	client *ClientConn
	socket *fileSocket
	device string
	timer  *time.Timer
}

// Keep request ownership until reply, deadline, or disconnect. The timer checks
// route identity so a late callback cannot delete a later request with the same ID.
func (s *Server) registerFileRequest(id string, socket *fileSocket, device string) error {
	client, _ := s.GetClient(device)
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	if s.fileRequests == nil {
		s.fileRequests = make(map[string]*fileRequestRoute)
	}
	if _, exists := s.fileRequests[id]; exists {
		return fmt.Errorf("request already pending")
	}
	count := 0
	for _, route := range s.fileRequests {
		if route.socket == socket {
			count++
		}
	}
	if count >= fileRequestsPerSocket || len(s.fileRequests) >= fileRequestsMax {
		return fmt.Errorf("too many pending file requests")
	}
	route := &fileRequestRoute{socket: socket, device: device, client: client}
	s.fileRequests[id] = route
	route.timer = time.AfterFunc(fileRequestTimeout, func() { s.expireFileRequest(id, route) })
	return nil
}
func (s *Server) expireFileRequest(id string, route *fileRequestRoute) {
	s.fileMu.Lock()
	if s.fileRequests[id] != route {
		s.fileMu.Unlock()
		return
	}
	delete(s.fileRequests, id)
	s.fileMu.Unlock()
	route.socket.writeText(fileMsg{Op: "error", Code: "timeout", DeviceID: route.device, RequestID: id, Message: "file request timed out"})
}
func (s *Server) takeFileRequest(id, device string) *fileRequestRoute {
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	route := s.fileRequests[id]
	if route == nil || (device != "" && route.device != device) {
		return nil
	}
	delete(s.fileRequests, id)
	if route.timer != nil {
		route.timer.Stop()
	}
	return route
}
func (s *Server) removeFileRequest(id string) { s.takeFileRequest(id, "") }

func (s *Server) failClientFileRequests(client *ClientConn) {
	pending := make(map[string]*fileRequestRoute)
	s.fileMu.Lock()
	for id, route := range s.fileRequests {
		if route.client == client {
			pending[id] = route
			delete(s.fileRequests, id)
			if route.timer != nil {
				route.timer.Stop()
			}
		}
	}
	s.fileMu.Unlock()
	for id, route := range pending {
		route.socket.writeText(fileMsg{Op: "error", Code: "disconnected", RequestID: id, DeviceID: route.device, Message: "device disconnected"})
	}
}

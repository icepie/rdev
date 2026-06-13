package server

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/lxzan/gws"
	"rdev/internal/protocol"
)

// TerminalHandler bridges browser xterm.js ↔ device shell over WebSocket
//
// Browser connects to /terminal?device=<deviceID>
//
// Protocol between server and browser (simplified JSON):
//
//	Browser → Server:  { "op": "input",  "data": "<base64>" }
//	                   { "op": "resize", "rows": N, "cols": N }
//	Server → Browser:  { "op": "output", "data": "<base64>" }
//	                   { "op": "exit",   "code": N }
//	                   { "op": "error",  "message": "..." }

// terminalMsg is the JSON protocol between server and browser terminal
type terminalMsg struct {
	Op      string `json:"op"`                // "input", "resize", "output", "exit", "error"
	Data    string `json:"data,omitempty"`    // base64 encoded for input/output
	Rows    int    `json:"rows,omitempty"`    // for resize
	Cols    int    `json:"cols,omitempty"`    // for resize
	Code    int    `json:"code,omitempty"`    // for exit
	Message string `json:"message,omitempty"` // for error
}

// terminalConn tracks a browser terminal WebSocket connection
type terminalConn struct {
	deviceID  string
	sessionID string
	socket    *gws.Conn
	sess      *ProxySession
	done      chan struct{}
	once      sync.Once
}

// terminalWSHandler implements gws.Event for browser terminal connections
type terminalWSHandler struct {
	gws.BuiltinEventHandler
	srv *Server
}

func (h *terminalWSHandler) OnOpen(socket *gws.Conn) {
	deviceIDI, _ := socket.Session().Load("deviceID")
	if deviceIDI == nil {
		h.sendError(socket, "missing device parameter")
		socket.WriteClose(1000, nil)
		return
	}
	deviceID := deviceIDI.(string)

	h.srv.mu.RLock()
	client, ok := h.srv.clients[deviceID]
	h.srv.mu.RUnlock()
	if !ok {
		h.sendError(socket, "device '"+deviceID+"' is not connected")
		socket.WriteClose(1000, nil)
		return
	}

	sessionID := generateID()

	// Request a PTY interactive shell on the device
	if err := client.Send(&protocol.Message{
		Type:      protocol.MsgNewSession,
		ClientID:  deviceID,
		SessionID: sessionID,
		Pty:       true,
		Term:      "xterm-256color",
		Rows:      24,
		Cols:      80,
	}); err != nil {
		h.sendError(socket, "failed to reach device")
		socket.WriteClose(1000, nil)
		return
	}

	proxySess := &ProxySession{
		ID:       sessionID,
		ClientID: deviceID,
		WriteCh:  make(chan []byte, 4096),
		StderrCh: make(chan []byte, 256),
		CloseCh:  make(chan struct{}, 1),
		Done:     make(chan struct{}),
		CloseSSH: func() {},
		ExitSSH:  func(code int) {},
	}

	h.srv.RegisterSession(proxySess, client)

	tc := &terminalConn{
		deviceID:  deviceID,
		sessionID: sessionID,
		socket:    socket,
		sess:      proxySess,
		done:      make(chan struct{}),
	}

	socket.Session().Store("terminalConn", tc)

	// ProxySession output → browser
	go tc.pumpOutput()
}

func (h *terminalWSHandler) OnClose(socket *gws.Conn, err error) {
	tcRaw, _ := socket.Session().Load("terminalConn")
	if tcRaw == nil {
		return
	}
	tc := tcRaw.(*terminalConn)

	// Tell device to close the session
	h.srv.mu.RLock()
	client, ok := h.srv.clients[tc.deviceID]
	h.srv.mu.RUnlock()
	if ok {
		client.Send(&protocol.Message{Type: protocol.MsgClose, SessionID: tc.sessionID})
	}

	h.srv.removeSession(tc.sessionID)
	client.mu.Lock()
	delete(client.Sessions, tc.sessionID)
	client.mu.Unlock()

	tc.close()
}

func (h *terminalWSHandler) OnMessage(socket *gws.Conn, message *gws.Message) {
	defer message.Close()

	tcRaw, _ := socket.Session().Load("terminalConn")
	if tcRaw == nil {
		return
	}
	tc := tcRaw.(*terminalConn)

	var tmsg terminalMsg
	if err := json.Unmarshal(message.Bytes(), &tmsg); err != nil {
		return
	}

	h.srv.mu.RLock()
	client, ok := h.srv.clients[tc.deviceID]
	h.srv.mu.RUnlock()
	if !ok {
		h.sendError(socket, "device disconnected")
		return
	}

	switch tmsg.Op {
	case "input":
		data, err := protocol.DecodeData(tmsg.Data)
		if err != nil || len(data) == 0 {
			return
		}
		client.Send(&protocol.Message{
			Type:      protocol.MsgData,
			SessionID: tc.sessionID,
			Data:      protocol.EncodeData(data),
		})

	case "resize":
		client.Send(&protocol.Message{
			Type:      protocol.MsgResize,
			SessionID: tc.sessionID,
			Rows:      tmsg.Rows,
			Cols:      tmsg.Cols,
		})
	}
}

func (h *terminalWSHandler) sendError(socket *gws.Conn, msg string) {
	data, _ := json.Marshal(terminalMsg{Op: "error", Message: msg})
	socket.WriteMessage(gws.OpcodeText, data)
}

// pumpOutput forwards ProxySession output to the browser
func (tc *terminalConn) pumpOutput() {
	for {
		select {
		case data, ok := <-tc.sess.WriteCh:
			if !ok {
				tc.sendExit(tc.sess.GetExitCode())
				return
			}
			msg, _ := json.Marshal(terminalMsg{Op: "output", Data: protocol.EncodeData(data)})
			tc.socket.WriteMessage(gws.OpcodeText, msg)

		case data, ok := <-tc.sess.StderrCh:
			if !ok {
				return
			}
			msg, _ := json.Marshal(terminalMsg{Op: "output", Data: protocol.EncodeData(data)})
			tc.socket.WriteMessage(gws.OpcodeText, msg)

		case <-tc.sess.CloseCh:
			// Device said close — drain remaining output then exit
			close(tc.sess.WriteCh)
			close(tc.sess.StderrCh)
			tc.sendExit(tc.sess.GetExitCode())
			return

		case <-tc.done:
			return
		}
	}
}

func (tc *terminalConn) sendExit(code int) {
	msg, _ := json.Marshal(terminalMsg{Op: "exit", Code: code})
	tc.socket.WriteMessage(gws.OpcodeText, msg)
}

func (tc *terminalConn) close() {
	tc.once.Do(func() { close(tc.done) })
}

// HandleTerminalWS handles browser terminal WebSocket connections
// URL: /terminal?device=<deviceID>
func (s *Server) HandleTerminalWS(w http.ResponseWriter, r *http.Request) {
	deviceID := r.URL.Query().Get("device")
	if deviceID == "" {
		http.Error(w, "missing device parameter", http.StatusBadRequest)
		return
	}

	upgrader := gws.NewUpgrader(&terminalWSHandler{srv: s}, &gws.ServerOption{
		ReadMaxPayloadSize: 16 * 1024 * 1024,
		Authorize: func(r *http.Request, session gws.SessionStorage) bool {
			session.Store("deviceID", deviceID)
			return true
		},
	})

	socket, err := upgrader.Upgrade(w, r)
	if err != nil {
		log.Printf("terminal ws upgrade error: %v", err)
		return
	}
	socket.ReadLoop()
}

// HandleTerminalAPI returns available devices for the terminal page
func (s *Server) HandleTerminalAPI(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	type deviceInfo struct {
		ID          string `json:"id"`
		ConnectedAt string `json:"connectedAt"`
		HasPassword bool   `json:"hasPassword"`
	}

	var devices []deviceInfo
	for _, c := range s.clients {
		devices = append(devices, deviceInfo{
			ID:          c.ID,
			ConnectedAt: c.ConnectedAt.Format(time.RFC3339),
			HasPassword: c.Password != "",
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(devices)
}

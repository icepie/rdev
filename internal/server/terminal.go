package server

import (
	"encoding/json"
	"log"
	"net/http"
	"runtime"
	"sync"

	"github.com/lxzan/gws"
	"rdev/internal/protocol"
)

// Terminal WebSocket protocol (optimized):
//
//	Server → Browser:  Binary frame = raw terminal output (zero overhead)
//	                   Text frame   = JSON {"op":"exit","code":N} or {"op":"error","message":"..."}
//	Browser → Server:  Binary frame = raw input data (zero overhead)
//	                   Text frame   = JSON {"op":"resize","rows":N,"cols":N}

// terminalMsg is the JSON control message for terminal (text frames only)
type terminalMsg struct {
	Op      string `json:"op"`
	Rows    int    `json:"rows,omitempty"`
	Cols    int    `json:"cols,omitempty"`
	Code    int    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

type terminalConn struct {
	deviceID  string
	sessionID string
	socket    *gws.Conn
	sess      *ProxySession
	done      chan struct{}
	once      sync.Once
}

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
		WriteCh:  make(chan []byte, 8192),
		StderrCh: make(chan []byte, 2048),
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
	go tc.pumpOutput()
}

func (h *terminalWSHandler) OnClose(socket *gws.Conn, err error) {
	tcRaw, _ := socket.Session().Load("terminalConn")
	if tcRaw == nil {
		return
	}
	tc := tcRaw.(*terminalConn)

	h.srv.mu.RLock()
	client, ok := h.srv.clients[tc.deviceID]
	h.srv.mu.RUnlock()
	if ok {
		client.Send(&protocol.Message{Type: protocol.MsgClose, SessionID: tc.sessionID})
	}

	h.srv.removeSession(tc.sessionID)
	if ok {
		client.mu.Lock()
		delete(client.Sessions, tc.sessionID)
		client.mu.Unlock()
	}

	tc.close()
}

func (h *terminalWSHandler) OnMessage(socket *gws.Conn, message *gws.Message) {
	defer message.Close()

	tcRaw, _ := socket.Session().Load("terminalConn")
	if tcRaw == nil {
		return
	}
	tc := tcRaw.(*terminalConn)

	h.srv.mu.RLock()
	client, ok := h.srv.clients[tc.deviceID]
	h.srv.mu.RUnlock()
	if !ok {
		h.sendError(socket, "device disconnected")
		return
	}

	if message.Opcode == gws.OpcodeBinary {
		// Binary frame = raw input data → forward to device
		raw := message.Bytes()
		data := make([]byte, len(raw))
		copy(data, raw)
		client.SendBinary(protocol.BinData, tc.sessionID, data)
		return
	}

	// Text frame = JSON control
	var tmsg terminalMsg
	if err := json.Unmarshal(message.Bytes(), &tmsg); err != nil {
		return
	}

	if tmsg.Op == "resize" {
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

// pumpOutput forwards ProxySession output to browser as binary frames
func (tc *terminalConn) pumpOutput() {
	for {
		select {
		case data, ok := <-tc.sess.WriteCh:
			if !ok {
				tc.sendExit(tc.sess.GetExitCode())
				return
			}
			// Binary frame = raw terminal output (no header needed, xterm.js handles raw bytes)
			tc.socket.WriteMessage(gws.OpcodeBinary, data)

		case data, ok := <-tc.sess.StderrCh:
			if !ok {
				return
			}
			tc.socket.WriteMessage(gws.OpcodeBinary, data)

		case <-tc.sess.CloseCh:
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
func (s *Server) HandleTerminalWS(w http.ResponseWriter, r *http.Request) {
	deviceID := r.URL.Query().Get("device")
	if deviceID == "" {
		http.Error(w, "missing device parameter", http.StatusBadRequest)
		return
	}

	upgrader := gws.NewUpgrader(&terminalWSHandler{srv: s}, &gws.ServerOption{
		ReadMaxPayloadSize: 16 * 1024 * 1024,
		ParallelGolimit:    runtime.GOMAXPROCS(0),
		Authorize: func(r *http.Request, session gws.SessionStorage) bool {
			session.Store("deviceID", deviceID)
			return true
		},
		PermessageDeflate: gws.PermessageDeflate{
			Enabled:               true,
			ServerContextTakeover:  true,
			ClientContextTakeover: true,
			Threshold:             128,
		},
	})

	socket, err := upgrader.Upgrade(w, r)
	if err != nil {
		log.Printf("terminal ws upgrade error: %v", err)
		return
	}
	socket.ReadLoop()
}

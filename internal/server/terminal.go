package server

import (
	"encoding/json"
	"log"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/lxzan/gws"
	"rdev/internal/protocol"
)

// Terminal WebSocket protocol (optimized):
//
//	Server → Browser:  Binary frame = raw terminal output (zero overhead)
//	                   Text frame   = JSON {"op":"ready"} (session route established; send current size)
//	                                   or {"op":"auth","message":"..."} (password required)
//	                                   or {"op":"exit","code":N} or {"op":"error","message":"..."}
//	Browser → Server:  Binary frame = raw input data (zero overhead)
//	                   Text frame   = JSON {"op":"resize","rows":N,"cols":N}
//	                                   or {"op":"auth","password":"..."}

// terminalMsg is the JSON control message for terminal (text frames only)
type terminalMsg struct {
	Op         string               `json:"op"`
	Rows       int                  `json:"rows,omitempty"`
	Cols       int                  `json:"cols,omitempty"`
	Code       int                  `json:"code,omitempty"`
	Message    string               `json:"message,omitempty"`
	Password   string               `json:"password,omitempty"`
	SessionID  string               `json:"sessionId,omitempty"`
	RTCType    string               `json:"rtcType,omitempty"`
	RTCData    json.RawMessage      `json:"rtcData,omitempty"`
	IceServers []protocol.IceServer `json:"iceServers,omitempty"`
}

type terminalConn struct {
	deviceID        string
	sessionID       string
	socket          *gws.Conn
	writeMu         sync.Mutex
	stateMu         sync.Mutex
	sess            *ProxySession
	client          *ClientConn
	closed          bool
	fallbackPending bool
	timer           *time.Timer
	offered         bool
	candidates      int
	done            chan struct{}
	once            sync.Once
	rtc             bool
	started         bool
	browserReady    bool
	deviceReady     bool
}

type terminalWSHandler struct {
	gws.BuiltinEventHandler
	srv          *Server
	authed       bool
	passOK       bool // true if device has no password (skip auth)
	adminBypass  bool // valid admin token: skip device password
	rtcRequested bool
}

const terminalImageProtocols = "sixel,iterm2,kitty"

func terminalSessionEnv() []string {
	return []string{
		"COLORTERM=truecolor",
		"TERM_PROGRAM=RDev",
		"RDEV_IMAGE_PROTOCOLS=" + terminalImageProtocols,
	}
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

	// If device has a password, require auth before creating session
	if client.Password != "" && !h.adminBypass {
		h.authed = false
		h.sendJSON(socket, terminalMsg{Op: "auth", Message: "Device '" + deviceID + "' requires password"})
		return
	}

	// No password → skip auth, create session immediately
	h.passOK = true
	h.createSession(socket, deviceID)
}

func (h *terminalWSHandler) createSession(socket *gws.Conn, deviceID string) {
	h.srv.mu.RLock()
	client, ok := h.srv.clients[deviceID]
	h.srv.mu.RUnlock()
	if !ok {
		h.sendError(socket, "device '"+deviceID+"' disconnected during auth")
		socket.WriteClose(1000, nil)
		return
	}

	sessionID := generateID()
	tc := &terminalConn{deviceID: deviceID, sessionID: sessionID, socket: socket, client: client, done: make(chan struct{}), rtc: h.rtcRequested && client.TerminalRTC && h.srv.WebRTCEnabled}
	proxySess := &ProxySession{
		ID: sessionID, ClientID: deviceID, terminal: tc,
		WriteCh: make(chan []byte, 8192), StderrCh: make(chan []byte, 2048),
		CloseCh: make(chan struct{}, 1), Done: make(chan struct{}), exitDone: make(chan struct{}),
		CloseSSH: func() {}, ExitSSH: func(code int) {},
	}
	tc.sess = proxySess
	proxySess.SetSessionMeta(true, "xterm-256color", "", "", 24, 80)
	if !h.srv.RegisterSession(proxySess, client) {
		h.sendError(socket, "too many active sessions on device")
		socket.WriteClose(1013, nil)
		return
	}
	socket.Session().Store("terminalConn", tc)
	go tc.pumpOutput()
	if tc.rtc {
		tc.sendControl(terminalMsg{Op: "rtc_start", SessionID: sessionID, IceServers: h.srv.webRTCICEServers()})
		tc.stateMu.Lock()
		tc.timer = time.AfterFunc(6*time.Second, func() {
			tc.stateMu.Lock()
			pending := !tc.started && !tc.closed
			tc.stateMu.Unlock()
			if pending {
				h.fallbackTerminalRTC(tc, client)
			}
		})
		tc.stateMu.Unlock()
		return
	}
	h.startTerminalDeviceSession(tc, client, false)
}

func (h *terminalWSHandler) startTerminalDeviceSession(tc *terminalConn, client *ClientConn, directRTC bool) bool {
	tc.stateMu.Lock()
	if tc.closed || tc.started || tc.rtc != directRTC || h.srv.clientByID(tc.deviceID) != client {
		tc.stateMu.Unlock()
		return false
	}
	tc.started = true
	if tc.timer != nil {
		tc.timer.Stop()
	}
	err := client.Send(&protocol.Message{
		Type: protocol.MsgNewSession, ClientID: tc.deviceID, SessionID: tc.sessionID,
		Pty: true, Term: "xterm-256color", Rows: 24, Cols: 80,
		Env: terminalSessionEnv(), TerminalRTC: directRTC,
	})
	tc.stateMu.Unlock()
	if err != nil {
		h.srv.removeSession(tc.sessionID)
		client.mu.Lock()
		delete(client.Sessions, tc.sessionID)
		client.mu.Unlock()
		tc.close()
		h.sendError(tc.socket, "failed to reach device")
		tc.socket.WriteClose(1000, nil)
		return false
	}
	tc.sendControl(terminalMsg{Op: "ready"})
	return true
}

func (h *terminalWSHandler) fallbackTerminalRTC(tc *terminalConn, client *ClientConn) {
	tc.stateMu.Lock()
	if tc.closed || !tc.rtc || tc.fallbackPending {
		tc.stateMu.Unlock()
		return
	}
	started := tc.started
	if tc.timer != nil {
		tc.timer.Stop()
	}
	if started {
		tc.fallbackPending = true
	} else {
		tc.rtc = false
	}
	if started {
		tc.timer = time.AfterFunc(6*time.Second, func() {
			tc.stateMu.Lock()
			pending := tc.fallbackPending && !tc.closed
			tc.stateMu.Unlock()
			if pending {
				tc.sendControl(terminalMsg{Op: "error", Message: "Terminal relay recovery timed out"})
				tc.socket.WriteClose(1011, nil)
			}
		})
	}
	tc.stateMu.Unlock()
	if started {
		tc.sendControl(terminalMsg{Op: "rtc_signal", RTCType: "close"})
		if err := client.Send(&protocol.Message{Type: protocol.MsgTerminalRTCSignal, SessionID: tc.sessionID, RTCType: "fallback"}); err != nil {
			tc.socket.WriteClose(1011, []byte("terminal fallback failed"))
		}
		return
	}
	_ = client.Send(&protocol.Message{Type: protocol.MsgTerminalRTCSignal, SessionID: tc.sessionID, RTCType: "close"})
	tc.sendControl(terminalMsg{Op: "rtc_fallback", SessionID: tc.sessionID})
	h.startTerminalDeviceSession(tc, client, false)
}

func (h *terminalWSHandler) terminalRTCReady(tc *terminalConn, client *ClientConn, browser bool) {
	tc.stateMu.Lock()
	if tc.closed || !tc.rtc || tc.started {
		tc.stateMu.Unlock()
		return
	}
	if browser {
		tc.browserReady = true
	} else {
		tc.deviceReady = true
	}
	ready := tc.browserReady && tc.deviceReady
	tc.stateMu.Unlock()
	if ready {
		h.startTerminalDeviceSession(tc, client, true)
	}
}

func (h *terminalWSHandler) OnClose(socket *gws.Conn, err error) {
	tcRaw, _ := socket.Session().Load("terminalConn")
	if tcRaw == nil {
		return
	}
	tc := tcRaw.(*terminalConn)

	tc.close()
	client := tc.client
	_ = client.Send(&protocol.Message{Type: protocol.MsgTerminalRTCSignal, SessionID: tc.sessionID, RTCType: "close"})
	_ = client.Send(&protocol.Message{Type: protocol.MsgClose, SessionID: tc.sessionID})
	h.srv.removeSession(tc.sessionID)
	client.mu.Lock()
	delete(client.Sessions, tc.sessionID)
	client.mu.Unlock()
}

func (h *terminalWSHandler) OnMessage(socket *gws.Conn, message *gws.Message) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC in terminal OnMessage: %v", r)
		}
	}()
	defer message.Close()

	// Handle auth flow before session is created
	if !h.authed && !h.passOK {
		if message.Opcode == gws.OpcodeText {
			var tmsg terminalMsg
			if err := json.Unmarshal(message.Bytes(), &tmsg); err != nil {
				return
			}
			if tmsg.Op != "auth" {
				return
			}

			// Validate password
			deviceIDI, _ := socket.Session().Load("deviceID")
			deviceID := deviceIDI.(string)

			h.srv.mu.RLock()
			client, ok := h.srv.clients[deviceID]
			h.srv.mu.RUnlock()

			if !ok {
				h.sendError(socket, "device '"+deviceID+"' disconnected")
				socket.WriteClose(1000, nil)
				return
			}

			// Brute-force throttle: reject during cooldown without comparing.
			if h.srv.authLimiter.wait(deviceID) > 0 {
				h.sendJSON(socket, terminalMsg{Op: "auth_fail", Message: "Too many attempts, retry later"})
				return
			}
			if client.Password != "" && constantTimeEqual(client.Password, tmsg.Password) {
				h.srv.authLimiter.success(deviceID)
				h.authed = true
				h.sendJSON(socket, terminalMsg{Op: "auth_ok"})
				h.createSession(socket, deviceID)
			} else {
				h.srv.authLimiter.fail(deviceID)
				h.sendJSON(socket, terminalMsg{Op: "auth_fail", Message: "Wrong password"})
			}
		}
		return
	}

	tcRaw, _ := socket.Session().Load("terminalConn")
	if tcRaw == nil {
		return
	}
	tc := tcRaw.(*terminalConn)

	client := tc.client
	if h.srv.clientByID(tc.deviceID) != client {
		h.sendError(socket, "device disconnected")
		return
	}

	tc.stateMu.Lock()
	directRTC := tc.rtc
	tc.stateMu.Unlock()
	if message.Opcode == gws.OpcodeBinary {
		if directRTC {
			return
		}
		raw := message.Bytes()
		data := make([]byte, len(raw))
		copy(data, raw)
		_ = client.SendBinary(protocol.BinData, tc.sessionID, data)
		return
	}

	var tmsg terminalMsg
	if err := json.Unmarshal(message.Bytes(), &tmsg); err != nil {
		return
	}
	switch tmsg.Op {
	case "resize":
		_ = client.Send(&protocol.Message{Type: protocol.MsgResize, SessionID: tc.sessionID, Rows: tmsg.Rows, Cols: tmsg.Cols})
	case "rtc_ready":
		if tmsg.SessionID == tc.sessionID {
			h.terminalRTCReady(tc, client, true)
		}
	case "rtc_fallback":
		if tmsg.SessionID == tc.sessionID {
			h.fallbackTerminalRTC(tc, client)
		}
	case "rtc_signal":
		if !directRTC || tmsg.SessionID != tc.sessionID || !validDesktopRTCSignal(tmsg.RTCType, tmsg.RTCData) {
			return
		}
		switch tmsg.RTCType {
		case "offer", "candidate":
			tc.stateMu.Lock()
			allowed := !tc.closed && tc.rtc && !tc.started
			if tmsg.RTCType == "offer" {
				allowed = allowed && !tc.offered
				tc.offered = true
			} else {
				tc.candidates++
				allowed = allowed && tc.candidates <= 128
			}
			tc.stateMu.Unlock()
			if allowed {
				_ = client.Send(&protocol.Message{Type: protocol.MsgTerminalRTCSignal, SessionID: tc.sessionID, RTCType: tmsg.RTCType, RTCData: tmsg.RTCData, IceServers: h.srv.rtcOfferICEServers(tmsg.RTCType)})
			}
		case "close":
			h.fallbackTerminalRTC(tc, client)
		}
	}
}

func (s *Server) handleTerminalRTCSignal(client *ClientConn, msg *protocol.Message) {
	if client == nil || msg == nil || len(msg.RTCData) > 128*1024 {
		return
	}
	sess := s.getSession(msg.SessionID)
	if sess == nil || sess.terminal == nil || sess.terminal.client != client || s.clientByID(client.ID) != client {
		return
	}
	tc := sess.terminal
	switch msg.RTCType {
	case "ready":
		(&terminalWSHandler{srv: s}).terminalRTCReady(tc, client, false)
	case "fallback_ready":
		tc.stateMu.Lock()
		pending := tc.fallbackPending && !tc.closed
		if pending {
			tc.rtc = false
			tc.fallbackPending = false
			if tc.timer != nil {
				tc.timer.Stop()
			}
		}
		tc.stateMu.Unlock()
		if pending {
			tc.sendControl(terminalMsg{Op: "rtc_fallback", SessionID: tc.sessionID, Message: "Direct connection interrupted; in-flight terminal bytes may be missing. Continuing the same shell via relay."})
		}
	case "answer", "candidate":
		if !validDesktopRTCSignal(msg.RTCType, msg.RTCData) {
			return
		}
		tc.stateMu.Lock()
		active := tc.rtc && !tc.closed
		tc.stateMu.Unlock()
		if active {
			tc.sendControl(terminalMsg{Op: "rtc_signal", RTCType: msg.RTCType, RTCData: msg.RTCData})
		}
	case "error", "close":
		(&terminalWSHandler{srv: s}).fallbackTerminalRTC(tc, client)
	}
}

func (h *terminalWSHandler) sendError(socket *gws.Conn, msg string) {
	h.sendJSON(socket, terminalMsg{Op: "error", Message: msg})
}

func (h *terminalWSHandler) sendJSON(socket *gws.Conn, msg terminalMsg) {
	data, _ := json.Marshal(msg)
	socket.WriteMessage(gws.OpcodeText, data)
}

func (tc *terminalConn) sendControl(msg terminalMsg) {
	data, _ := json.Marshal(msg)
	tc.writeMessage(gws.OpcodeText, data)
}

func (tc *terminalConn) writeMessage(opcode gws.Opcode, data []byte) bool {
	tc.writeMu.Lock()
	defer tc.writeMu.Unlock()
	if err := tc.socket.WriteMessage(opcode, data); err != nil {
		log.Printf("terminal write failed: device=%s session=%s opcode=%d bytes=%d err=%v", tc.deviceID, tc.sessionID, opcode, len(data), err)
		return false
	}
	return true
}

// pumpOutput forwards ProxySession output to browser as binary frames
func (tc *terminalConn) pumpOutput() {
	defer tc.close()
	for {
		select {
		case data, ok := <-tc.sess.WriteCh:
			if !ok {
				tc.sendExit(tc.sess.WaitExitCode(500 * time.Millisecond))
				return
			}
			// Binary frame = raw terminal output (no header needed, xterm.js handles raw bytes)
			if !tc.writeMessage(gws.OpcodeBinary, data) {
				return
			}

		case data, ok := <-tc.sess.StderrCh:
			if !ok {
				return
			}
			if !tc.writeMessage(gws.OpcodeBinary, data) {
				return
			}

		case <-tc.sess.CloseCh:
			tc.sess.NotifyObserversClose()
			tc.sess.CloseOutput()
			tc.sendExit(tc.sess.WaitExitCode(500 * time.Millisecond))
			return

		case <-tc.done:
			return
		}
	}
}

func (tc *terminalConn) sendExit(code int) {
	msg, _ := json.Marshal(terminalMsg{Op: "exit", Code: code})
	tc.writeMessage(gws.OpcodeText, msg)
}

func (tc *terminalConn) close() {
	tc.stateMu.Lock()
	tc.closed = true
	if tc.timer != nil {
		tc.timer.Stop()
	}
	tc.stateMu.Unlock()
	tc.once.Do(func() { close(tc.done) })
}

// HandleTerminalWS handles browser terminal WebSocket connections
func (s *Server) HandleTerminalWS(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuth(w, r) {
		return
	}
	if !requireOrigin(w, r) {
		return
	}
	deviceID := r.URL.Query().Get("device")
	if deviceID == "" {
		http.Error(w, "missing device parameter", http.StatusBadRequest)
		return
	}

	upgrader := gws.NewUpgrader(&terminalWSHandler{srv: s, adminBypass: s.deviceAuthBypassed(r), rtcRequested: r.URL.Query().Get("rtc") == "1"}, &gws.ServerOption{
		ReadMaxPayloadSize: 16 * 1024 * 1024,
		ParallelGolimit:    runtime.GOMAXPROCS(0),
		Authorize: func(r *http.Request, session gws.SessionStorage) bool {
			session.Store("deviceID", deviceID)
			return true
		},
		PermessageDeflate: gws.PermessageDeflate{Enabled: false},
	})

	socket, err := upgrader.Upgrade(w, r)
	if err != nil {
		log.Printf("terminal ws upgrade error: %v", err)
		return
	}
	socket.ReadLoop()
}

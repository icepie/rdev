package server

import (
	"encoding/json"
	"log"
	"net/http"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/lxzan/gws"
	"rdev/internal/protocol"
)

// HandleSessionsAPI returns all active sessions across all devices.
func (s *Server) HandleSessionsAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuth(w, r) {
		return
	}
	s.sessMu.RLock()
	defer s.sessMu.RUnlock()

	type sessionDetail struct {
		ID        string `json:"id"`
		ClientID  string `json:"clientId"`
		Type      string `json:"type"` // "shell", "exec", "sftp"
		Command   string `json:"command"`
		Pty       bool   `json:"pty"`
		Term      string `json:"term"`
		Rows      int    `json:"rows"`
		Cols      int    `json:"cols"`
		CreatedAt string `json:"createdAt"`
		Observers int    `json:"observers"`
	}

	var sessions []sessionDetail
	for _, sess := range s.sessions {
		pty, term, cmd, subsystem, rows, cols, createdAt := sess.SessionMeta()
		sessType := "exec"
		if pty && cmd == "" && subsystem == "" {
			sessType = "shell"
		} else if subsystem == "sftp" {
			sessType = "sftp"
		}
		sessions = append(sessions, sessionDetail{
			ID:        sess.ID,
			ClientID:  sess.ClientID,
			Type:      sessType,
			Command:   cmd,
			Pty:       pty,
			Term:      term,
			Rows:      rows,
			Cols:      cols,
			CreatedAt: createdAt.Format(time.RFC3339),
			Observers: sess.ObserverCount(),
		})
	}

	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].ClientID != sessions[j].ClientID {
			return sessions[i].ClientID < sessions[j].ClientID
		}
		if sessions[i].CreatedAt != sessions[j].CreatedAt {
			return sessions[i].CreatedAt < sessions[j].CreatedAt
		}
		return sessions[i].ID < sessions[j].ID
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sessions)
}

// --- Session Attach WebSocket ---

// sessionAttachMsg is the JSON control message for session attach.
//
//	Server → Browser:  Binary frame = raw session output (same as terminal)
//	                   Text frame   = JSON {"op":"exit","code":N} or {"op":"error","message":"..."}
//	Browser → Server:  Binary frame = raw input data
//	                   Text frame   = JSON {"op":"resize","rows":N,"cols":N}

type sessionAttachMsg struct {
	Op       string `json:"op"`
	Rows     int    `json:"rows,omitempty"`
	Cols     int    `json:"cols,omitempty"`
	Code     int    `json:"code,omitempty"`
	Password string `json:"password,omitempty"`
	Message  string `json:"message,omitempty"`
}

type sessionAttachHandler struct {
	gws.BuiltinEventHandler
	srv *Server
}

type sessionAttachConn struct {
	sessionID string
	socket    *gws.Conn
	sess      *ProxySession
	obsID     string
	writeMu   sync.Mutex
	authOK    bool
	done      chan struct{}
	once      sync.Once
}

func (h *sessionAttachHandler) OnOpen(socket *gws.Conn) {
	sessionIDI, _ := socket.Session().Load("sessionID")
	if sessionIDI == nil {
		h.sendError(socket, "missing session parameter")
		socket.WriteClose(1000, nil)
		return
	}
	sessionID := sessionIDI.(string)

	h.srv.sessMu.RLock()
	sess, ok := h.srv.sessions[sessionID]
	h.srv.sessMu.RUnlock()
	if !ok {
		h.sendError(socket, "session not found")
		socket.WriteClose(1000, nil)
		return
	}

	ac := &sessionAttachConn{
		sessionID: sessionID,
		socket:    socket,
		sess:      sess,
		done:      make(chan struct{}),
	}
	socket.Session().Store("attachConn", ac)

	h.srv.mu.RLock()
	client, ok := h.srv.clients[sess.ClientID]
	h.srv.mu.RUnlock()
	if !ok {
		h.sendError(socket, "device not connected")
		socket.WriteClose(1000, nil)
		return
	}
	if client.Password != "" {
		h.sendJSON(socket, sessionAttachMsg{Op: "auth", Message: "Device '" + sess.ClientID + "' requires password"})
		return
	}
	h.attach(ac)
}

func (h *sessionAttachHandler) attach(ac *sessionAttachConn) {
	if ac.authOK {
		return
	}
	obsID := generateID()
	history, writeCh, stderrCh, done := ac.sess.AddObserver(obsID)
	ac.obsID = obsID
	ac.authOK = true
	h.sendJSON(ac.socket, sessionAttachMsg{Op: "auth_ok"})
	go ac.pumpOutput(history, writeCh, stderrCh, done)
}

func (h *sessionAttachHandler) OnClose(socket *gws.Conn, err error) {
	acRaw, _ := socket.Session().Load("attachConn")
	if acRaw == nil {
		return
	}
	ac := acRaw.(*sessionAttachConn)
	if ac.obsID != "" {
		ac.sess.RemoveObserver(ac.obsID)
	}
	ac.close()
}

func (h *sessionAttachHandler) OnMessage(socket *gws.Conn, message *gws.Message) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC in sessionAttach OnMessage: %v", r)
		}
	}()
	defer message.Close()

	acRaw, _ := socket.Session().Load("attachConn")
	if acRaw == nil {
		return
	}
	ac := acRaw.(*sessionAttachConn)

	if message.Opcode == gws.OpcodeBinary {
		if !ac.authOK {
			return
		}
		// Binary input → forward to the device via the existing session's client.
		h.srv.mu.RLock()
		client, ok := h.srv.clients[ac.sess.ClientID]
		h.srv.mu.RUnlock()
		if !ok {
			return
		}
		raw := message.Bytes()
		data := make([]byte, len(raw))
		copy(data, raw)
		client.SendBinary(protocol.BinData, ac.sess.ID, data)
		return
	}

	// Text = JSON control
	var msg sessionAttachMsg
	if err := json.Unmarshal(message.Bytes(), &msg); err != nil {
		return
	}

	switch msg.Op {
	case "auth":
		if ac.authOK {
			return
		}
		h.srv.mu.RLock()
		client, ok := h.srv.clients[ac.sess.ClientID]
		h.srv.mu.RUnlock()
		if !ok {
			h.sendError(socket, "device not connected")
			return
		}
		if client.Password == "" || constantTimeEqual(client.Password, msg.Password) {
			h.attach(ac)
			return
		}
		h.sendJSON(socket, sessionAttachMsg{Op: "auth_fail", Message: "Wrong password"})
	case "resize":
		if !ac.authOK {
			return
		}
		// Forward resize to device.
		h.srv.mu.RLock()
		client, ok := h.srv.clients[ac.sess.ClientID]
		h.srv.mu.RUnlock()
		if !ok {
			return
		}
		client.Send(&protocol.Message{
			Type:      protocol.MsgResize,
			SessionID: ac.sess.ID,
			Rows:      msg.Rows,
			Cols:      msg.Cols,
		})
	}
}

func (h *sessionAttachHandler) sendError(socket *gws.Conn, msg string) {
	h.sendJSON(socket, sessionAttachMsg{Op: "error", Message: msg})
}

func (h *sessionAttachHandler) sendJSON(socket *gws.Conn, msg sessionAttachMsg) {
	data, _ := json.Marshal(msg)
	_ = socket.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_ = socket.WriteMessage(gws.OpcodeText, data)
	_ = socket.SetWriteDeadline(time.Time{})
}

func (ac *sessionAttachConn) pumpOutput(history [][]byte, writeCh, stderrCh <-chan []byte, done <-chan struct{}) {
	for _, data := range history {
		if err := ac.writeMessage(gws.OpcodeBinary, data); err != nil {
			ac.close()
			return
		}
	}

	for {
		select {
		case data, ok := <-writeCh:
			if !ok {
				ac.sendExit(-1)
				return
			}
			if err := ac.writeMessage(gws.OpcodeBinary, data); err != nil {
				ac.close()
				return
			}

		case data, ok := <-stderrCh:
			if !ok {
				return
			}
			if err := ac.writeMessage(gws.OpcodeBinary, data); err != nil {
				ac.close()
				return
			}

		case <-done:
			ac.sendExit(ac.sess.WaitExitCode(2 * time.Second))
			return

		case <-ac.done:
			return
		}
	}
}

func (ac *sessionAttachConn) writeMessage(opcode gws.Opcode, data []byte) error {
	ac.writeMu.Lock()
	defer ac.writeMu.Unlock()
	_ = ac.socket.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err := ac.socket.WriteMessage(opcode, data)
	_ = ac.socket.SetWriteDeadline(time.Time{})
	return err
}

func (ac *sessionAttachConn) sendExit(code int) {
	msg, _ := json.Marshal(sessionAttachMsg{Op: "exit", Code: code})
	_ = ac.writeMessage(gws.OpcodeText, msg)
}

func (ac *sessionAttachConn) close() {
	ac.once.Do(func() { close(ac.done) })
}

// HandleSessionAttachWS handles browser session-attach WebSocket connections
func (s *Server) HandleSessionAttachWS(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuth(w, r) {
		return
	}
	sessionID := r.URL.Query().Get("session")
	if sessionID == "" {
		http.Error(w, "missing session parameter", http.StatusBadRequest)
		return
	}

	upgrader := gws.NewUpgrader(&sessionAttachHandler{srv: s}, &gws.ServerOption{
		ReadMaxPayloadSize: 16 * 1024 * 1024,
		ParallelGolimit:    runtime.GOMAXPROCS(0),
		Authorize: func(r *http.Request, session gws.SessionStorage) bool {
			session.Store("sessionID", sessionID)
			return true
		},
		PermessageDeflate: gws.PermessageDeflate{
			Enabled:               true,
			ServerContextTakeover: true,
			ClientContextTakeover: true,
			Threshold:             128,
		},
	})

	socket, err := upgrader.Upgrade(w, r)
	if err != nil {
		log.Printf("session attach ws upgrade error: %v", err)
		return
	}
	socket.ReadLoop()
}

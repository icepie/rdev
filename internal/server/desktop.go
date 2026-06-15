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

type desktopMsg struct {
	Op      string                        `json:"op"`
	Message string                        `json:"message,omitempty"`
	Device  string                        `json:"device,omitempty"`
	Session string                        `json:"session,omitempty"`
	Code    int                           `json:"code,omitempty"`
	Width   int                           `json:"width,omitempty"`
	Height  int                           `json:"height,omitempty"`
	Format  string                        `json:"format,omitempty"`
	Desktop *protocol.DesktopCapabilities `json:"desktop,omitempty"`
	Pass    string                        `json:"password,omitempty"`
}

type desktopRoute struct {
	id       string
	clientID string
	conn     *desktopBrowserConn
}

type desktopBrowserConn struct {
	srv      *Server
	socket   *gws.Conn
	deviceID string
	session  string
	client   *ClientConn
	authOK   bool
	writeMu  sync.Mutex
	done     chan struct{}
	once     sync.Once
}

type desktopWSHandler struct {
	gws.BuiltinEventHandler
	srv *Server
}

func (h *desktopWSHandler) OnOpen(socket *gws.Conn) {
	deviceID, _ := socket.Session().Load("deviceID")
	if deviceID == nil || deviceID.(string) == "" {
		h.sendJSON(socket, desktopMsg{Op: "error", Message: "missing device"})
		socket.WriteClose(1000, nil)
		return
	}
	h.srv.mu.RLock()
	client, ok := h.srv.clients[deviceID.(string)]
	h.srv.mu.RUnlock()
	if !ok {
		h.sendJSON(socket, desktopMsg{Op: "error", Message: "device not connected"})
		socket.WriteClose(1000, nil)
		return
	}

	bc := &desktopBrowserConn{srv: h.srv, socket: socket, deviceID: client.ID, client: client, done: make(chan struct{})}
	socket.Session().Store("desktopConn", bc)
	if client.Password != "" {
		h.sendJSON(socket, desktopMsg{Op: "auth", Device: client.ID, Message: "device password required"})
		return
	}
	h.start(bc)
}

func (h *desktopWSHandler) OnClose(socket *gws.Conn, err error) {
	bc := desktopConnFromSocket(socket)
	if bc == nil {
		return
	}
	bc.close()
}

func (h *desktopWSHandler) OnMessage(socket *gws.Conn, message *gws.Message) {
	defer message.Close()
	if message.Opcode != gws.OpcodeText {
		return
	}
	bc := desktopConnFromSocket(socket)
	if bc == nil {
		return
	}
	var msg desktopMsg
	if err := json.Unmarshal(message.Bytes(), &msg); err != nil {
		return
	}
	switch msg.Op {
	case "auth":
		if bc.authOK {
			return
		}
		if bc.client.Password == "" || passwordFingerprint(msg.Pass) == passwordFingerprint(bc.client.Password) {
			h.start(bc)
			return
		}
		bc.writeJSON(desktopMsg{Op: "auth_fail", Device: bc.deviceID, Message: "wrong password"})
	case "close":
		bc.close()
	}
}

func (h *desktopWSHandler) start(bc *desktopBrowserConn) {
	if bc.authOK {
		return
	}
	bc.authOK = true
	bc.session = generateID()
	bc.srv.desktopMu.Lock()
	bc.srv.desktops[bc.session] = &desktopRoute{id: bc.session, clientID: bc.deviceID, conn: bc}
	bc.srv.desktopMu.Unlock()
	bc.writeJSON(desktopMsg{Op: "auth_ok", Device: bc.deviceID, Session: bc.session})
	bc.writeJSON(desktopMsg{Op: "starting", Device: bc.deviceID, Session: bc.session})
	bc.client.Send(&protocol.Message{Type: protocol.MsgDesktopStart, SessionID: bc.session, FPS: 2, Quality: 60})
}

func (h *desktopWSHandler) sendJSON(socket *gws.Conn, msg desktopMsg) {
	data, _ := json.Marshal(msg)
	_ = socket.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_ = socket.WriteMessage(gws.OpcodeText, data)
	_ = socket.SetWriteDeadline(time.Time{})
}

func desktopConnFromSocket(socket *gws.Conn) *desktopBrowserConn {
	raw, _ := socket.Session().Load("desktopConn")
	if raw == nil {
		return nil
	}
	bc, _ := raw.(*desktopBrowserConn)
	return bc
}

func (bc *desktopBrowserConn) writeJSON(msg desktopMsg) error {
	data, _ := json.Marshal(msg)
	return bc.write(gws.OpcodeText, data)
}

func (bc *desktopBrowserConn) writeBinary(data []byte) error {
	return bc.write(gws.OpcodeBinary, data)
}

func (bc *desktopBrowserConn) write(opcode gws.Opcode, data []byte) error {
	bc.writeMu.Lock()
	defer bc.writeMu.Unlock()
	_ = bc.socket.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err := bc.socket.WriteMessage(opcode, data)
	_ = bc.socket.SetWriteDeadline(time.Time{})
	return err
}

func (bc *desktopBrowserConn) close() {
	bc.once.Do(func() {
		close(bc.done)
		if bc.session != "" {
			bc.srv.desktopMu.Lock()
			delete(bc.srv.desktops, bc.session)
			bc.srv.desktopMu.Unlock()
			bc.client.Send(&protocol.Message{Type: protocol.MsgDesktopClose, SessionID: bc.session})
		}
		bc.writeMu.Lock()
		_ = bc.socket.SetWriteDeadline(time.Now().Add(2 * time.Second))
		_ = bc.socket.WriteClose(1000, nil)
		_ = bc.socket.SetWriteDeadline(time.Time{})
		bc.writeMu.Unlock()
	})
}

func (s *Server) handleDesktopMessage(msg *protocol.Message) {
	if msg.SessionID == "" {
		return
	}
	s.desktopMu.RLock()
	route := s.desktops[msg.SessionID]
	s.desktopMu.RUnlock()
	if route == nil || route.conn == nil {
		return
	}
	switch msg.Type {
	case protocol.MsgDesktopReady:
		op := "ready"
		if msg.Error != "" {
			op = "error"
		}
		route.conn.writeJSON(desktopMsg{Op: op, Session: msg.SessionID, Width: msg.Width, Height: msg.Height, Format: msg.Format, Desktop: msg.DesktopCapabilities, Message: msg.Error})
	case protocol.MsgDesktopClose:
		route.conn.writeJSON(desktopMsg{Op: "closed", Session: msg.SessionID, Message: msg.Error})
		route.conn.close()
	}
}

func (s *Server) handleDesktopFrame(sessionID string, data []byte) {
	s.desktopMu.RLock()
	route := s.desktops[sessionID]
	s.desktopMu.RUnlock()
	if route == nil || route.conn == nil || len(data) == 0 {
		return
	}
	if err := route.conn.writeBinary(data); err != nil {
		route.conn.close()
	}
}

func (s *Server) closeDesktopForClient(clientID string) {
	s.desktopMu.Lock()
	var routes []*desktopRoute
	for id, route := range s.desktops {
		if route.clientID == clientID {
			routes = append(routes, route)
			delete(s.desktops, id)
		}
	}
	s.desktopMu.Unlock()
	for _, route := range routes {
		if route.conn != nil {
			route.conn.writeJSON(desktopMsg{Op: "closed", Session: route.id, Message: "device disconnected"})
			route.conn.close()
		}
	}
}

// HandleDesktopWS handles browser remote desktop WebSocket connections.
func (s *Server) HandleDesktopWS(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuth(w, r) {
		return
	}
	deviceID := r.URL.Query().Get("device")
	upgrader := gws.NewUpgrader(&desktopWSHandler{srv: s}, &gws.ServerOption{
		ReadMaxPayloadSize: 1024 * 1024,
		ParallelGolimit:    runtime.GOMAXPROCS(0),
		Authorize: func(r *http.Request, session gws.SessionStorage) bool {
			session.Store("deviceID", deviceID)
			return true
		},
		PermessageDeflate: gws.PermessageDeflate{
			Enabled:               true,
			ServerContextTakeover: true,
			ClientContextTakeover: true,
			Threshold:             256,
		},
	})
	socket, err := upgrader.Upgrade(w, r)
	if err != nil {
		log.Printf("desktop ws upgrade error: %v", err)
		return
	}
	socket.ReadLoop()
}

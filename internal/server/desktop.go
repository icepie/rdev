package server

import (
	"encoding/json"
	"log"
	"net/http"
	"runtime"
	"strconv"
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
	Mode    string                        `json:"mode,omitempty"`
	Source  string                        `json:"source,omitempty"`
	Quality int                           `json:"quality,omitempty"`
	FPS     int                           `json:"fps,omitempty"`
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
	request  protocol.Message
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

	bc := &desktopBrowserConn{srv: h.srv, socket: socket, deviceID: client.ID, client: client, request: desktopRequestFromSession(socket), done: make(chan struct{})}
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
	case "auth", "start":
		bc.request = mergeDesktopRequest(bc.request, msg)
		if bc.authOK {
			return
		}
		if msg.Op == "start" && bc.client.Password != "" {
			bc.writeJSON(desktopMsg{Op: "auth", Device: bc.deviceID, Message: "device password required"})
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
	startMsg := bc.request
	startMsg.Type = protocol.MsgDesktopStart
	startMsg.SessionID = bc.session
	bc.client.Send(&startMsg)
}

func desktopRequestFromSession(socket *gws.Conn) protocol.Message {
	raw, _ := socket.Session().Load("desktopRequest")
	if request, ok := raw.(protocol.Message); ok {
		return normalizeDesktopRequest(request)
	}
	return defaultDesktopRequest()
}

func desktopRequestFromQuery(r *http.Request) protocol.Message {
	q := r.URL.Query()
	request := protocol.Message{
		FPS:     parseDesktopInt(q.Get("fps")),
		Quality: parseDesktopInt(q.Get("quality")),
		Width:   parseDesktopInt(q.Get("width")),
		Height:  parseDesktopInt(q.Get("height")),
		Source:  q.Get("source"),
	}
	return normalizeDesktopRequest(request)
}

func mergeDesktopRequest(base protocol.Message, msg desktopMsg) protocol.Message {
	if msg.FPS != 0 {
		base.FPS = msg.FPS
	}
	if msg.Quality != 0 {
		base.Quality = msg.Quality
	}
	if msg.Width != 0 {
		base.Width = msg.Width
	}
	if msg.Height != 0 {
		base.Height = msg.Height
	}
	if msg.Source != "" {
		base.Source = msg.Source
	}
	if msg.Mode != "" && msg.Mode != "manual" {
		base = defaultDesktopRequest()
		base.Source = msg.Source
	}
	return normalizeDesktopRequest(base)
}

func defaultDesktopRequest() protocol.Message {
	return protocol.Message{FPS: 4, Quality: 50, Width: 1600, Height: 1000, Source: "auto"}
}

func normalizeDesktopRequest(request protocol.Message) protocol.Message {
	if request.FPS <= 0 {
		request.FPS = 4
	}
	if request.FPS > 12 {
		request.FPS = 12
	}
	if request.Quality <= 0 {
		request.Quality = 50
	}
	if request.Quality > 90 {
		request.Quality = 90
	}
	if request.Quality < 25 {
		request.Quality = 25
	}
	if request.Width <= 0 {
		request.Width = 1600
	}
	if request.Height <= 0 {
		request.Height = 1000
	}
	if request.Width > 3840 {
		request.Width = 3840
	}
	if request.Height > 2160 {
		request.Height = 2160
	}
	if request.Source == "" {
		request.Source = "auto"
	}
	return request
}

func parseDesktopInt(value string) int {
	if value == "" {
		return 0
	}
	n, _ := strconv.Atoi(value)
	return n
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
		route.conn.writeJSON(desktopMsg{Op: op, Session: msg.SessionID, Width: msg.Width, Height: msg.Height, Format: msg.Format, Source: msg.Source, Desktop: msg.DesktopCapabilities, Message: msg.Error})
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
	request := desktopRequestFromQuery(r)
	upgrader := gws.NewUpgrader(&desktopWSHandler{srv: s}, &gws.ServerOption{
		ReadMaxPayloadSize: 1024 * 1024,
		ParallelGolimit:    runtime.GOMAXPROCS(0),
		Authorize: func(r *http.Request, session gws.SessionStorage) bool {
			session.Store("deviceID", deviceID)
			session.Store("desktopRequest", request)
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

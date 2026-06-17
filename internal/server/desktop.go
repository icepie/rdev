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
	Op           string                        `json:"op"`
	Message      string                        `json:"message,omitempty"`
	Device       string                        `json:"device,omitempty"`
	Session      string                        `json:"session,omitempty"`
	StatusCode   int                           `json:"statusCode,omitempty"`
	Width        int                           `json:"width,omitempty"`
	Height       int                           `json:"height,omitempty"`
	Format       string                        `json:"format,omitempty"`
	Mode         string                        `json:"mode,omitempty"`
	Source       string                        `json:"source,omitempty"`
	Quality      int                           `json:"quality,omitempty"`
	FPS          int                           `json:"fps,omitempty"`
	InputBackend string                        `json:"inputBackend,omitempty"`
	ShowCursor   *bool                         `json:"showCursor,omitempty"`
	Desktop      *protocol.DesktopCapabilities `json:"desktop,omitempty"`
	Pass         string                        `json:"password,omitempty"`
	InputType    string                        `json:"inputType,omitempty"`
	X            int                           `json:"x,omitempty"`
	Y            int                           `json:"y,omitempty"`
	Button       int                           `json:"button,omitempty"`
	DeltaX       int                           `json:"deltaX,omitempty"`
	DeltaY       int                           `json:"deltaY,omitempty"`
	Key          string                        `json:"key,omitempty"`
	KeyCode      string                        `json:"code,omitempty"`
	CtrlKey      bool                          `json:"ctrlKey,omitempty"`
	AltKey       bool                          `json:"altKey,omitempty"`
	ShiftKey     bool                          `json:"shiftKey,omitempty"`
	MetaKey      bool                          `json:"metaKey,omitempty"`
	PointerType  string                        `json:"pointerType,omitempty"`
	PointerID    int                           `json:"pointerId,omitempty"`
	Pressure     float64                       `json:"pressure,omitempty"`
}

type desktopRoute struct {
	id       string
	clientID string
	conn     *desktopBrowserConn
	vnc      *vncConn
	stream   *vncDesktopStream
}

type desktopBrowserConn struct {
	srv            *Server
	socket         *gws.Conn
	deviceID       string
	session        string
	client         *ClientConn
	request        protocol.Message
	authOK         bool
	writeMu        sync.Mutex
	frameCh        chan []byte
	frameOnce      sync.Once
	inputMu        sync.Mutex
	lastMouseMove  time.Time
	lastInputError time.Time
	frameWidth     int
	frameHeight    int
	done           chan struct{}
	once           sync.Once
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

	bc := &desktopBrowserConn{srv: h.srv, socket: socket, deviceID: client.ID, client: client, request: desktopRequestFromSession(socket), frameCh: make(chan []byte, 1), done: make(chan struct{})}
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
	case "input":
		input, ok := bc.prepareInput(msg)
		if !ok {
			return
		}
		bc.client.Send(input)
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
	bc.startFrameWriter()
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
		FPS:          parseDesktopInt(q.Get("fps")),
		Quality:      parseDesktopInt(q.Get("quality")),
		Width:        parseDesktopInt(q.Get("width")),
		Height:       parseDesktopInt(q.Get("height")),
		Source:       q.Get("source"),
		InputBackend: q.Get("inputBackend"),
		ShowCursor:   parseDesktopBool(q.Get("showCursor"), false),
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
	if msg.InputBackend != "" {
		base.InputBackend = msg.InputBackend
	}
	if msg.ShowCursor != nil {
		base.ShowCursor = *msg.ShowCursor
	}
	if msg.Mode != "" && msg.Mode != "manual" {
		base = defaultDesktopRequest()
		base.Source = msg.Source
		base.InputBackend = msg.InputBackend
		if msg.ShowCursor != nil {
			base.ShowCursor = *msg.ShowCursor
		}
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
	if request.InputBackend == "" {
		request.InputBackend = "auto"
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

func parseDesktopBool(value string, fallback bool) bool {
	if value == "" {
		return fallback
	}
	b, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return b
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

func (bc *desktopBrowserConn) startFrameWriter() {
	bc.frameOnce.Do(func() {
		go func() {
			for {
				select {
				case <-bc.done:
					return
				case frame := <-bc.frameCh:
					if len(frame) == 0 {
						continue
					}
					if err := bc.writeBinary(frame); err != nil {
						bc.close()
						return
					}
				}
			}
		}()
	})
}

func (bc *desktopBrowserConn) enqueueFrame(data []byte) {
	if len(data) == 0 {
		return
	}
	frame := append([]byte(nil), data...)
	select {
	case bc.frameCh <- frame:
		return
	default:
	}
	select {
	case <-bc.frameCh:
	default:
	}
	select {
	case bc.frameCh <- frame:
	default:
	}
}

func (bc *desktopBrowserConn) prepareInput(msg desktopMsg) (*protocol.Message, bool) {
	if !bc.authOK || bc.session == "" {
		return nil, false
	}
	if bc.client.Desktop == nil || !bc.client.Desktop.Input {
		if msg.InputType != "cursor_move" {
			bc.inputError("desktop input is not available")
			return nil, false
		}
	}
	bc.inputMu.Lock()
	defer bc.inputMu.Unlock()
	now := time.Now()
	switch msg.InputType {
	case "cursor_move":
		if now.Sub(bc.lastMouseMove) < 16*time.Millisecond {
			return nil, false
		}
		bc.lastMouseMove = now
		msg.X, msg.Y = bc.clampPoint(msg.X, msg.Y)
	case "mouse_move":
		msg.PointerType = normalizePointerType(msg.PointerType)
		msg.Pressure = normalizePressure(msg.Pressure)
		if now.Sub(bc.lastMouseMove) < 16*time.Millisecond {
			return nil, false
		}
		bc.lastMouseMove = now
		msg.X, msg.Y = bc.clampPoint(msg.X, msg.Y)
	case "mouse_down", "mouse_up":
		msg.PointerType = normalizePointerType(msg.PointerType)
		msg.Pressure = normalizePressure(msg.Pressure)
		msg.X, msg.Y = bc.clampPoint(msg.X, msg.Y)
		if msg.Button < 0 || msg.Button > 4 {
			bc.inputError("unsupported mouse button")
			return nil, false
		}
	case "wheel":
		msg.PointerType = normalizePointerType(msg.PointerType)
		msg.X, msg.Y = bc.clampPoint(msg.X, msg.Y)
		if msg.DeltaX > 2000 {
			msg.DeltaX = 2000
		}
		if msg.DeltaX < -2000 {
			msg.DeltaX = -2000
		}
		if msg.DeltaY > 2000 {
			msg.DeltaY = 2000
		}
		if msg.DeltaY < -2000 {
			msg.DeltaY = -2000
		}
	case "key_down", "key_up":
		if len(msg.Key) > 64 || len(msg.KeyCode) > 64 {
			bc.inputError("invalid key event")
			return nil, false
		}
	default:
		bc.inputError("unsupported input event")
		return nil, false
	}
	return &protocol.Message{
		Type: protocol.MsgDesktopInput, SessionID: bc.session, InputType: msg.InputType,
		X: msg.X, Y: msg.Y, Button: msg.Button, DeltaX: msg.DeltaX, DeltaY: msg.DeltaY,
		Key: msg.Key, Code: msg.KeyCode, CtrlKey: msg.CtrlKey, AltKey: msg.AltKey, ShiftKey: msg.ShiftKey, MetaKey: msg.MetaKey, InputBackend: bc.request.InputBackend,
		PointerType: msg.PointerType, PointerID: msg.PointerID, Pressure: msg.Pressure,
	}, true
}

func normalizePointerType(pointerType string) string {
	switch pointerType {
	case "touch", "pen":
		return pointerType
	default:
		return "mouse"
	}
}

func normalizePressure(pressure float64) float64 {
	if pressure < 0 {
		return 0
	}
	if pressure > 1 {
		return 1
	}
	return pressure
}

func (bc *desktopBrowserConn) clampPoint(x, y int) (int, int) {
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	if bc.frameWidth > 0 && x >= bc.frameWidth {
		x = bc.frameWidth - 1
	}
	if bc.frameHeight > 0 && y >= bc.frameHeight {
		y = bc.frameHeight - 1
	}
	return x, y
}

func (bc *desktopBrowserConn) inputError(message string) {
	now := time.Now()
	if now.Sub(bc.lastInputError) < 2*time.Second {
		return
	}
	bc.lastInputError = now
	bc.writeJSON(desktopMsg{Op: "input_error", Session: bc.session, Message: message})
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
	if route == nil {
		return
	}
	switch msg.Type {
	case protocol.MsgDesktopReady:
		if route.vnc != nil {
			route.vnc.handleReady(msg)
			return
		}
		if route.stream != nil {
			route.stream.handleReady(msg)
			return
		}
		if route.conn == nil {
			return
		}
		op := "ready"
		if msg.Error != "" {
			op = "error"
		}
		route.conn.inputMu.Lock()
		route.conn.frameWidth = msg.Width
		route.conn.frameHeight = msg.Height
		route.conn.inputMu.Unlock()
		route.conn.writeJSON(desktopMsg{Op: op, Session: msg.SessionID, Width: msg.Width, Height: msg.Height, Format: msg.Format, Source: msg.Source, InputBackend: msg.InputBackend, Desktop: msg.DesktopCapabilities, Message: msg.Error})
	case protocol.MsgDesktopClose:
		if route.vnc != nil {
			route.vnc.handleClose(msg)
			return
		}
		if route.stream != nil {
			route.stream.handleClose(msg)
			return
		}
		if route.conn == nil {
			return
		}
		route.conn.writeJSON(desktopMsg{Op: "closed", Session: msg.SessionID, Message: msg.Error})
		route.conn.close()
	}
}

func (s *Server) handleDesktopFrame(sessionID string, data []byte) {
	s.desktopMu.RLock()
	route := s.desktops[sessionID]
	s.desktopMu.RUnlock()
	if route == nil || len(data) == 0 {
		return
	}
	if route.vnc != nil {
		route.vnc.enqueueFrame(data)
		return
	}
	if route.stream != nil {
		route.stream.enqueueFrame(data)
		return
	}
	if route.conn == nil {
		return
	}
	route.conn.enqueueFrame(data)
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
		if route.stream != nil {
			route.stream.closeDeviceDisconnected()
			continue
		}
		if route.vnc != nil {
			route.vnc.close()
			continue
		}
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

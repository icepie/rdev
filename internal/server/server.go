package server

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/lxzan/gws"
	"rdev/internal/protocol"
)

//go:embed static
var templateFS embed.FS

// ClientConn represents a connected client device
type ClientConn struct {
	ID          string
	Conn        *gws.Conn
	ConnectedAt time.Time
	Password    string
	Sessions    map[string]*ProxySession
	Forwards    map[string]*ProxyForward
	mu          sync.Mutex
}

// Send sends a JSON control message to the client (text frame)
func (c *ClientConn) Send(msg *protocol.Message) error {
	data, err := protocol.Encode(msg)
	if err != nil {
		return err
	}
	return c.Conn.WriteMessage(gws.OpcodeText, data)
}

// SendBinary sends a binary data frame to the client
func (c *ClientConn) SendBinary(typ byte, id string, data []byte) error {
	frame := protocol.EncodeBinFrame(typ, id, data)
	return c.Conn.WriteMessage(gws.OpcodeBinary, frame)
}

// SendFilePut sends a file to the client device (binary frame)
func (c *ClientConn) SendFilePut(id, path string, mode int32, fileData []byte) error {
	frame := protocol.EncodeBinFilePut(id, path, mode, fileData)
	return c.Conn.WriteMessage(gws.OpcodeBinary, frame)
}

// ProxySession represents a proxied SSH session (shell/exec/sftp)
type ProxySession struct {
	ID       string
	ClientID string
	WriteCh  chan []byte // client device -> SSH stdout / terminal
	StderrCh chan []byte // client device -> SSH stderr
	CloseCh  chan struct{}
	Done     chan struct{}
	exitCode int
	exitDone chan struct{} // closed when exit code is set
	exitMu   sync.Mutex
	CloseSSH func()
	ExitSSH  func(code int)
}

func (s *ProxySession) SetExitCode(code int) {
	s.exitMu.Lock()
	s.exitCode = code
	s.exitMu.Unlock()
	// Signal that exit code is available
	select {
	case <-s.exitDone:
		// already closed
	default:
		close(s.exitDone)
	}
}

func (s *ProxySession) GetExitCode() int {
	s.exitMu.Lock()
	defer s.exitMu.Unlock()
	return s.exitCode
}

// WaitExitCode blocks until an exit code is set or timeout expires
func (s *ProxySession) WaitExitCode(timeout time.Duration) int {
	select {
	case <-s.exitDone:
		s.exitMu.Lock()
		code := s.exitCode
		s.exitMu.Unlock()
		return code
	case <-time.After(timeout):
		return -1
	}
}

// ProxyForward represents a proxied TCP connection (port forwarding)
type ProxyForward struct {
	ID       string
	ClientID string
	WriteCh  chan []byte
	CloseCh  chan struct{}
	Done     chan struct{}
	CloseSSH func()
}

// Server manages WebSocket clients and SSH proxy
type Server struct {
	clients     map[string]*ClientConn
	mu          sync.RWMutex
	sessions    map[string]*ProxySession
	sessMu      sync.RWMutex
	forwards    map[string]*ProxyForward
	fwdMu       sync.RWMutex
	fileResults map[string]chan *protocol.Message
	fileMu      sync.RWMutex
	upgrader    *gws.Upgrader
}

// NewServer creates a new Server
func NewServer() *Server {
	s := &Server{
		clients:     make(map[string]*ClientConn),
		sessions:    make(map[string]*ProxySession),
		forwards:    make(map[string]*ProxyForward),
		fileResults: make(map[string]chan *protocol.Message),
	}
	s.upgrader = gws.NewUpgrader(&wsHandler{srv: s}, &gws.ServerOption{
		ReadMaxPayloadSize: 16 * 1024 * 1024,
		ParallelGolimit:    runtime.GOMAXPROCS(0),
		PermessageDeflate: gws.PermessageDeflate{
			Enabled:               true,
			ServerContextTakeover:  true,
			ClientContextTakeover: true,
			Threshold:             256,
		},
	})
	return s
}

// wsHandler implements gws.Event for server-side WebSocket connections
type wsHandler struct {
	gws.BuiltinEventHandler
	srv *Server
}

func (h *wsHandler) OnOpen(socket *gws.Conn) {}

func (h *wsHandler) OnClose(socket *gws.Conn, err error) {
	clientID, _ := socket.Session().Load("clientID")
	if clientID == nil {
		return
	}
	id := clientID.(string)

	h.srv.mu.Lock()
	client, ok := h.srv.clients[id]
	if ok {
		delete(h.srv.clients, id)
	}
	h.srv.mu.Unlock()

	if ok {
		client.mu.Lock()
		for sid, sess := range client.Sessions {
			close(sess.CloseCh)
			h.srv.sessMu.Lock()
			delete(h.srv.sessions, sid)
			h.srv.sessMu.Unlock()
		}
		for fid, fwd := range client.Forwards {
			close(fwd.CloseCh)
			h.srv.fwdMu.Lock()
			delete(h.srv.forwards, fid)
			h.srv.fwdMu.Unlock()
		}
		client.mu.Unlock()
	}

	log.Printf("client unregistered: %s", id)
}

func (h *wsHandler) OnMessage(socket *gws.Conn, message *gws.Message) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC in wsHandler.OnMessage: %v", r)
		}
	}()
	defer message.Close()

	// Binary frame = raw data message
	if message.Opcode == gws.OpcodeBinary {
		h.handleBinaryMessage(socket, message.Bytes())
		return
	}

	// Text frame = JSON control message
	msg, err := protocol.Decode(message.Bytes())
	if err != nil {
		return
	}


	// First message must be register
	if msg.Type == protocol.MsgRegister {
		h.handleRegister(socket, msg)
		return
	}

	clientID, _ := socket.Session().Load("clientID")
	if clientID == nil {
		return
	}

	h.srv.mu.RLock()
	client, ok := h.srv.clients[clientID.(string)]
	h.srv.mu.RUnlock()
	if !ok {
		return
	}

	h.srv.handleClientMessage(client, msg)
}

func (h *wsHandler) handleRegister(socket *gws.Conn, msg *protocol.Message) {
	if msg.ClientID == "" {
		socket.WriteClose(1000, nil)
		return
	}

	client := &ClientConn{
		ID:          msg.ClientID,
		Conn:        socket,
		ConnectedAt: time.Now(),
		Password:    msg.Password,
		Sessions:    make(map[string]*ProxySession),
		Forwards:    make(map[string]*ProxyForward),
	}

	socket.Session().Store("clientID", msg.ClientID)

	h.srv.mu.Lock()
	if old, ok := h.srv.clients[msg.ClientID]; ok {
		old.Conn.WriteClose(1000, []byte("replaced by new connection"))
		for sid, sess := range old.Sessions {
			close(sess.CloseCh)
			h.srv.sessMu.Lock()
			delete(h.srv.sessions, sid)
			h.srv.sessMu.Unlock()
		}
		for fid, fwd := range old.Forwards {
			close(fwd.CloseCh)
			h.srv.fwdMu.Lock()
			delete(h.srv.forwards, fid)
			h.srv.fwdMu.Unlock()
		}
	}
	h.srv.clients[msg.ClientID] = client
	h.srv.mu.Unlock()

	log.Printf("client registered: %s", msg.ClientID)
	client.Send(&protocol.Message{Type: protocol.MsgRegister, ClientID: msg.ClientID})
}

// handleBinaryMessage processes binary data frames from device clients
func (h *wsHandler) handleBinaryMessage(socket *gws.Conn, raw []byte) {
	typ, id, payload, err := protocol.DecodeBinFrame(raw)
	if err != nil {
		return
	}

	log.Printf("binary data: type=0x%02x id=%s len=%d", typ, id, len(payload))

	clientID, _ := socket.Session().Load("clientID")
	if clientID == nil {
		return
	}

	// Copy payload since message buffer will be recycled by gws
	data := make([]byte, len(payload))
	copy(data, payload)

	switch typ {
	case protocol.BinData:
		sess := h.srv.getSession(id)
		if sess != nil && len(data) > 0 {
			select {
			case sess.WriteCh <- data:
			default:
			}
		}

	case protocol.BinStderr:
		sess := h.srv.getSession(id)
		if sess != nil && len(data) > 0 {
			select {
			case sess.StderrCh <- data:
			default:
			}
		}

	case protocol.BinTCPData:
		fwd := h.srv.getForward(id)
		if fwd != nil && len(data) > 0 {
			select {
			case fwd.WriteCh <- data:
			default:
			}
		}
	}
}

// HandleWS handles a WebSocket connection from a client device
func (s *Server) HandleWS(w http.ResponseWriter, r *http.Request) {
	socket, err := s.upgrader.Upgrade(w, r)
	if err != nil {
		log.Printf("ws upgrade error: %v", err)
		return
	}
	socket.ReadLoop()
}

func (s *Server) handleClientMessage(client *ClientConn, msg *protocol.Message) {
	switch msg.Type {
	case protocol.MsgExitCode:
		sess := s.getSession(msg.SessionID)
		if sess != nil {
			sess.SetExitCode(msg.ExitCode)
		}

	case protocol.MsgClose:
		sess := s.getSession(msg.SessionID)
		if sess != nil {
			select {
			case sess.CloseCh <- struct{}{}:
			default:
			}
		}

	// TCP forwarding control
	case protocol.MsgTCPOpen:
		// Connection succeeded on client device

	case protocol.MsgTCPFail:
		fwd := s.getForward(msg.ForwardID)
		if fwd != nil && fwd.CloseSSH != nil {
			fwd.CloseSSH()
		}
		s.removeForward(msg.ForwardID)

	case protocol.MsgTCPClose:
		fwd := s.getForward(msg.ForwardID)
		if fwd != nil {
			select {
			case fwd.CloseCh <- struct{}{}:
			default:
			}
		}

	// File distribution
	case protocol.MsgFileResult:
		s.handleFileResult(msg)
	}
}

// GetClient returns a connected client by ID
func (s *Server) GetClient(clientID string) (*ClientConn, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.clients[clientID]
	return c, ok
}

// Session management
func (s *Server) RegisterSession(sess *ProxySession, client *ClientConn) {
	s.sessMu.Lock()
	s.sessions[sess.ID] = sess
	s.sessMu.Unlock()
	client.mu.Lock()
	client.Sessions[sess.ID] = sess
	client.mu.Unlock()
}

func (s *Server) getSession(id string) *ProxySession {
	s.sessMu.RLock()
	defer s.sessMu.RUnlock()
	return s.sessions[id]
}

func (s *Server) removeSession(id string) {
	s.sessMu.Lock()
	delete(s.sessions, id)
	s.sessMu.Unlock()
}

// Forward management
func (s *Server) RegisterForward(fwd *ProxyForward, client *ClientConn) {
	s.fwdMu.Lock()
	s.forwards[fwd.ID] = fwd
	s.fwdMu.Unlock()
	client.mu.Lock()
	client.Forwards[fwd.ID] = fwd
	client.mu.Unlock()
}

func (s *Server) getForward(id string) *ProxyForward {
	s.fwdMu.RLock()
	defer s.fwdMu.RUnlock()
	return s.forwards[id]
}

func (s *Server) removeForward(id string) {
	s.fwdMu.Lock()
	delete(s.forwards, id)
	s.fwdMu.Unlock()
}

// File distribution
func (s *Server) RegisterFileResult(id string, ch chan *protocol.Message) {
	s.fileMu.Lock()
	s.fileResults[id] = ch
	s.fileMu.Unlock()
}

func (s *Server) unregisterFileResult(id string) {
	s.fileMu.Lock()
	delete(s.fileResults, id)
	s.fileMu.Unlock()
}

func (s *Server) handleFileResult(msg *protocol.Message) {
	s.fileMu.RLock()
	ch, ok := s.fileResults[msg.SessionID]
	s.fileMu.RUnlock()
	if ok {
		select {
		case ch <- msg:
		default:
		}
	}
}

// HandleAPI returns the list of connected clients as JSON
func (s *Server) HandleAPI(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	type clientInfo struct {
		ID          string `json:"id"`
		ConnectedAt string `json:"connectedAt"`
		Sessions    int    `json:"sessions"`
		Forwards    int    `json:"forwards"`
		HasPassword bool   `json:"hasPassword"`
	}

	var clients []clientInfo
	for _, c := range s.clients {
		c.mu.Lock()
		n := len(c.Sessions)
		f := len(c.Forwards)
		c.mu.Unlock()
		clients = append(clients, clientInfo{
			ID:          c.ID,
			ConnectedAt: c.ConnectedAt.Format(time.RFC3339),
			Sessions:    n,
			Forwards:    f,
			HasPassword: c.Password != "",
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(clients)
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

// StaticHandler returns an http.Handler for the embedded web UI
func (s *Server) StaticHandler() http.Handler {
	sub, err := fs.Sub(templateFS, "static")
	if err != nil {
		return http.NotFoundHandler()
	}
	return http.FileServer(http.FS(sub))
}

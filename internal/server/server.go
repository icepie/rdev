package server

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
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

// Send sends a protocol message to the client over WebSocket
func (c *ClientConn) Send(msg *protocol.Message) error {
	data, err := protocol.Encode(msg)
	if err != nil {
		return err
	}
	return c.Conn.WriteMessage(gws.OpcodeText, data)
}

// ProxySession represents a proxied SSH session (shell/exec/sftp)
type ProxySession struct {
	ID       string
	ClientID string
	WriteCh  chan []byte
	StderrCh chan []byte
	CloseCh  chan struct{}
	Done     chan struct{}
	exitCode int
	exitMu   sync.Mutex
	CloseSSH func()
	ExitSSH  func(code int)
}

func (s *ProxySession) SetExitCode(code int) {
	s.exitMu.Lock()
	s.exitCode = code
	s.exitMu.Unlock()
}

func (s *ProxySession) GetExitCode() int {
	s.exitMu.Lock()
	defer s.exitMu.Unlock()
	return s.exitCode
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
	clients  map[string]*ClientConn
	mu       sync.RWMutex
	sessions map[string]*ProxySession
	sessMu   sync.RWMutex
	forwards map[string]*ProxyForward
	fwdMu    sync.RWMutex
	upgrader *gws.Upgrader
}

// NewServer creates a new Server
func NewServer() *Server {
	s := &Server{
		clients:  make(map[string]*ClientConn),
		sessions: make(map[string]*ProxySession),
		forwards: make(map[string]*ProxyForward),
	}
	s.upgrader = gws.NewUpgrader(&wsHandler{srv: s}, &gws.ServerOption{
		ReadMaxPayloadSize: 16 * 1024 * 1024,
	})
	return s
}

// wsHandler implements gws.Event for server-side WebSocket connections
type wsHandler struct {
	gws.BuiltinEventHandler
	srv *Server
}

func (h *wsHandler) OnOpen(socket *gws.Conn) {
	// Will register when first message (register) arrives
}

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
	defer message.Close()

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

	// Store clientID in session for later lookup
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
	// --- Session ---
	case protocol.MsgData:
		sess := s.getSession(msg.SessionID)
		if sess == nil {
			return
		}
		data, err := protocol.DecodeData(msg.Data)
		if err != nil || len(data) == 0 {
			return
		}
		select {
		case sess.WriteCh <- data:
		default:
		}

	case protocol.MsgStderrData:
		sess := s.getSession(msg.SessionID)
		if sess == nil {
			return
		}
		data, err := protocol.DecodeData(msg.Stderr)
		if err != nil || len(data) == 0 {
			return
		}
		select {
		case sess.StderrCh <- data:
		default:
		}

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

	// --- TCP forwarding ---
	case protocol.MsgTCPOpen:
		fwd := s.getForward(msg.ForwardID)
		if fwd == nil {
			return
		}

	case protocol.MsgTCPFail:
		fwd := s.getForward(msg.ForwardID)
		if fwd != nil && fwd.CloseSSH != nil {
			fwd.CloseSSH()
		}
		s.removeForward(msg.ForwardID)

	case protocol.MsgTCPData:
		fwd := s.getForward(msg.ForwardID)
		if fwd == nil {
			return
		}
		data, err := protocol.DecodeData(msg.Data)
		if err != nil || len(data) == 0 {
			return
		}
		select {
		case fwd.WriteCh <- data:
		default:
		}

	case protocol.MsgTCPClose:
		fwd := s.getForward(msg.ForwardID)
		if fwd != nil {
			select {
			case fwd.CloseCh <- struct{}{}:
			default:
			}
		}
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

// StaticHandler returns an http.Handler for the embedded web UI
func (s *Server) StaticHandler() http.Handler {
	sub, err := fs.Sub(templateFS, "static")
	if err != nil {
		return http.NotFoundHandler()
	}
	return http.FileServer(http.FS(sub))
}

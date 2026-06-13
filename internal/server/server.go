package server

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"rdev/internal/protocol"
)

//go:embed static
var templateFS embed.FS

// ClientConn represents a connected client device
type ClientConn struct {
	ID          string
	Conn        *websocket.Conn
	ConnectedAt time.Time
	Password    string
	Sessions    map[string]*ProxySession
	Forwards    map[string]*ProxyForward // TCP forwarding channels
	mu          sync.Mutex
	sendMu      sync.Mutex
}

// Send sends a protocol message to the client over WebSocket
func (c *ClientConn) Send(msg *protocol.Message) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	data, err := protocol.Encode(msg)
	if err != nil {
		return err
	}
	return c.Conn.WriteMessage(websocket.TextMessage, data)
}

// ProxySession represents a proxied SSH session (shell/exec/sftp)
type ProxySession struct {
	ID       string
	ClientID string
	WriteCh  chan []byte // client device -> SSH stdout
	StderrCh chan []byte // client device -> SSH stderr
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
	ID        string
	ClientID  string
	WriteCh   chan []byte // client device -> SSH channel (outgoing data to SSH client)
	CloseCh   chan struct{}
	Done      chan struct{}
	CloseSSH  func() // close the SSH channel
}

// Server manages WebSocket clients and SSH proxy
type Server struct {
	clients  map[string]*ClientConn
	mu       sync.RWMutex
	sessions map[string]*ProxySession
	sessMu   sync.RWMutex
	forwards map[string]*ProxyForward
	fwdMu    sync.RWMutex
	upgrader websocket.Upgrader
}

// NewServer creates a new Server
func NewServer() *Server {
	return &Server{
		clients:  make(map[string]*ClientConn),
		sessions: make(map[string]*ProxySession),
		forwards: make(map[string]*ProxyForward),
		upgrader: websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
	}
}

// HandleWS handles a WebSocket connection from a client device
func (s *Server) HandleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade error: %v", err)
		return
	}
	defer conn.Close()

	_, raw, err := conn.ReadMessage()
	if err != nil {
		return
	}
	msg, err := protocol.Decode(raw)
	if err != nil {
		return
	}
	if msg.Type != protocol.MsgRegister || msg.ClientID == "" {
		return
	}

	client := &ClientConn{
		ID:          msg.ClientID,
		Conn:        conn,
		ConnectedAt: time.Now(),
		Password:    msg.Password,
		Sessions:    make(map[string]*ProxySession),
		Forwards:    make(map[string]*ProxyForward),
	}

	s.mu.Lock()
	if old, ok := s.clients[msg.ClientID]; ok {
		old.Conn.Close()
		for sid, sess := range old.Sessions {
			close(sess.CloseCh)
			s.sessMu.Lock()
			delete(s.sessions, sid)
			s.sessMu.Unlock()
		}
		for fid, fwd := range old.Forwards {
			close(fwd.CloseCh)
			s.fwdMu.Lock()
			delete(s.forwards, fid)
			s.fwdMu.Unlock()
		}
	}
	s.clients[msg.ClientID] = client
	s.mu.Unlock()

	log.Printf("client registered: %s", msg.ClientID)
	client.Send(&protocol.Message{Type: protocol.MsgRegister, ClientID: msg.ClientID})

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			log.Printf("client %s disconnected: %v", msg.ClientID, err)
			break
		}
		msg, err := protocol.Decode(raw)
		if err != nil {
			continue
		}
		s.handleClientMessage(client, msg)
	}

	// Cleanup
	s.mu.Lock()
	delete(s.clients, client.ID)
	s.mu.Unlock()

	client.mu.Lock()
	for sid, sess := range client.Sessions {
		close(sess.CloseCh)
		s.sessMu.Lock()
		delete(s.sessions, sid)
		s.sessMu.Unlock()
	}
	for fid, fwd := range client.Forwards {
		close(fwd.CloseCh)
		s.fwdMu.Lock()
		delete(s.forwards, fid)
		s.fwdMu.Unlock()
	}
	client.mu.Unlock()

	log.Printf("client unregistered: %s", client.ID)
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
		// Connection succeeded on client device, data will flow

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
			HasPassword:  c.Password != "",
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

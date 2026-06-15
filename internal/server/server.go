package server

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lxzan/gws"
	gossh "golang.org/x/crypto/ssh"
	"rdev/internal/protocol"
)

//go:embed static
var templateFS embed.FS

const sessionHistoryLimit = 1024 * 1024

// ClientConn represents a connected client device
type ClientConn struct {
	ID          string
	Conn        *gws.Conn
	ConnectedAt time.Time
	Password    string
	Sessions    map[string]*ProxySession
	Forwards    map[string]*ProxyForward
	Desktop     *protocol.DesktopCapabilities
	writeMu     sync.Mutex
	mu          sync.Mutex
}

// Send sends a JSON control message to the client (text frame)
func (c *ClientConn) Send(msg *protocol.Message) error {
	data, err := protocol.Encode(msg)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.Conn.WriteMessage(gws.OpcodeText, data)
}

// SendBinary sends a binary data frame to the client
func (c *ClientConn) SendBinary(typ byte, id string, data []byte) error {
	frame := protocol.EncodeBinFrame(typ, id, data)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.Conn.WriteMessage(gws.OpcodeBinary, frame)
}

// SendFilePut sends a file to the client device (binary frame)
func (c *ClientConn) SendFilePut(id, path string, mode int32, fileData []byte) error {
	frame := protocol.EncodeBinFilePut(id, path, mode, fileData)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.Conn.WriteMessage(gws.OpcodeBinary, frame)
}

func (c *ClientConn) SendFileStart(id, path string, mode int32) error {
	frame := protocol.EncodeBinFileStart(id, path, mode)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.Conn.WriteMessage(gws.OpcodeBinary, frame)
}

func (c *ClientConn) SendFileChunk(id string, data []byte) error {
	frame := protocol.EncodeBinFrame(protocol.BinFileChunk, id, data)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.Conn.WriteMessage(gws.OpcodeBinary, frame)
}

func (c *ClientConn) SendFileEnd(id string) error {
	frame := protocol.EncodeBinFrame(protocol.BinFileEnd, id, nil)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.Conn.WriteMessage(gws.OpcodeBinary, frame)
}

func (c *ClientConn) SendBinaryOffset(typ byte, id string, offset int64, data []byte) error {
	frame := protocol.EncodeBinFrameOffset(typ, id, offset, data)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.Conn.WriteMessage(gws.OpcodeBinary, frame)
}

// ProxySession represents a proxied SSH session (shell/exec/sftp)
type ProxySession struct {
	ID         string
	ClientID   string
	WriteCh    chan []byte // client device -> SSH stdout / terminal
	StderrCh   chan []byte // client device -> SSH stderr
	CloseCh    chan struct{}
	Done       chan struct{}
	closeOnce  sync.Once
	outputOnce sync.Once
	exitCode   int
	exitDone   chan struct{} // closed when exit code is set
	exitMu     sync.Mutex
	CloseSSH   func()
	ExitSSH    func(code int)

	// Session management metadata
	createdAt time.Time
	pty       bool
	term      string
	rows      int
	cols      int
	command   string // original command (empty for shell)
	subsystem string // "", "sftp"

	// Recent output history for late session attach viewers.
	historyMu    sync.Mutex
	history      [][]byte
	historyBytes int

	// Observers for session attach.
	obsMu     sync.RWMutex
	observers map[string]*sessionObserver // id -> observer
}

type sessionObserver struct {
	id       string
	writeCh  chan []byte // copy of session output -> observer
	stderrCh chan []byte // copy of session stderr -> observer
	done     chan struct{}
	once     sync.Once
}

func (o *sessionObserver) close() {
	o.once.Do(func() { close(o.done) })
}

func (s *ProxySession) SignalClose() {
	s.closeOnce.Do(func() { close(s.CloseCh) })
}

func (s *ProxySession) CloseOutput() {
	s.outputOnce.Do(func() {
		close(s.WriteCh)
		close(s.StderrCh)
	})
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

// --- Observer (session attach) support ---

// AddObserver registers an observer that receives a copy of all session output.
func (s *ProxySession) AddObserver(id string) (history [][]byte, writeCh, stderrCh <-chan []byte, done <-chan struct{}) {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	s.obsMu.Lock()
	defer s.obsMu.Unlock()
	if s.observers == nil {
		s.observers = make(map[string]*sessionObserver)
	}
	obs := &sessionObserver{
		id:       id,
		writeCh:  make(chan []byte, 4096),
		stderrCh: make(chan []byte, 1024),
		done:     make(chan struct{}),
	}
	s.observers[id] = obs
	if len(s.history) > 0 {
		history = make([][]byte, len(s.history))
		copy(history, s.history)
	}
	return history, obs.writeCh, obs.stderrCh, obs.done
}

func (s *ProxySession) recordHistoryLocked(data []byte) {
	if len(data) == 0 || sessionHistoryLimit <= 0 {
		return
	}
	chunk := data
	if len(chunk) > sessionHistoryLimit {
		chunk = chunk[len(chunk)-sessionHistoryLimit:]
	}
	copyChunk := make([]byte, len(chunk))
	copy(copyChunk, chunk)

	s.history = append(s.history, copyChunk)
	s.historyBytes += len(copyChunk)
	for s.historyBytes > sessionHistoryLimit && len(s.history) > 0 {
		s.historyBytes -= len(s.history[0])
		s.history[0] = nil
		s.history = s.history[1:]
	}
}

func (s *ProxySession) HistorySnapshot() [][]byte {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	if len(s.history) == 0 {
		return nil
	}
	snapshot := make([][]byte, len(s.history))
	copy(snapshot, s.history)
	return snapshot
}

// RemoveObserver unregisters an observer.
func (s *ProxySession) RemoveObserver(id string) {
	s.obsMu.Lock()
	defer s.obsMu.Unlock()
	if obs, ok := s.observers[id]; ok {
		obs.close()
		close(obs.writeCh)
		close(obs.stderrCh)
		delete(s.observers, id)
	}
}

// BroadcastOutput sends session output to all observers.
func (s *ProxySession) BroadcastOutput(data []byte) {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	s.recordHistoryLocked(data)
	s.obsMu.RLock()
	defer s.obsMu.RUnlock()
	for _, obs := range s.observers {
		select {
		case obs.writeCh <- data:
		default:
			// drop if observer is slow
		}
	}
}

// BroadcastStderr sends session stderr to all observers.
func (s *ProxySession) BroadcastStderr(data []byte) {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	s.recordHistoryLocked(data)
	s.obsMu.RLock()
	defer s.obsMu.RUnlock()
	for _, obs := range s.observers {
		select {
		case obs.stderrCh <- data:
		default:
		}
	}
}

// NotifyObserversClose signals all observers that the session is closing.
func (s *ProxySession) NotifyObserversClose() {
	s.obsMu.RLock()
	defer s.obsMu.RUnlock()
	for _, obs := range s.observers {
		obs.close()
	}
}

// ObserverCount returns the number of observers.
func (s *ProxySession) ObserverCount() int {
	s.obsMu.RLock()
	defer s.obsMu.RUnlock()
	return len(s.observers)
}

// SetSessionMeta stores session creation metadata.
func (s *ProxySession) SetSessionMeta(pty bool, term, command, subsystem string, rows, cols int) {
	s.pty = pty
	s.term = term
	s.command = command
	s.subsystem = subsystem
	s.rows = rows
	s.cols = cols
	s.createdAt = time.Now()
}

// SessionMeta returns session metadata for API listing.
func (s *ProxySession) SessionMeta() (pty bool, term, command, subsystem string, rows, cols int, createdAt time.Time) {
	return s.pty, s.term, s.command, s.subsystem, s.rows, s.cols, s.createdAt
}

// ProxyForward represents a proxied TCP connection (port forwarding)
type ProxyForward struct {
	ID         string
	ClientID   string
	WriteCh    chan []byte
	CloseCh    chan struct{}
	OpenCh     chan struct{}
	Done       chan struct{}
	closeOnce  sync.Once
	outputOnce sync.Once
	openOnce   sync.Once
	failMu     sync.Mutex
	failErr    string
	CloseSSH   func()
}

func (f *ProxyForward) SignalOpen() {
	if f.OpenCh == nil {
		return
	}
	f.openOnce.Do(func() { close(f.OpenCh) })
}

func (f *ProxyForward) SignalFail(errText string) {
	f.failMu.Lock()
	f.failErr = errText
	f.failMu.Unlock()
	f.SignalOpen()
}

func (f *ProxyForward) FailError() string {
	f.failMu.Lock()
	defer f.failMu.Unlock()
	return f.failErr
}

func (f *ProxyForward) SignalClose() {
	f.closeOnce.Do(func() { close(f.CloseCh) })
}

func (f *ProxyForward) CloseOutput() {
	f.outputOnce.Do(func() { close(f.WriteCh) })
}

type ReverseForward struct {
	ID       string
	ClientID string
	BindAddr string
	BindPort uint32
	SSHConn  *gossh.ServerConn
	OpenCh   chan struct{}
	Cancel   func()

	mu      sync.Mutex
	port    uint32
	errText string
	once    sync.Once
}

func (f *ReverseForward) SignalOpen(port uint32) {
	f.mu.Lock()
	f.port = port
	f.mu.Unlock()
	f.once.Do(func() { close(f.OpenCh) })
}

func (f *ReverseForward) SignalFail(errText string) {
	f.mu.Lock()
	f.errText = errText
	f.mu.Unlock()
	f.once.Do(func() { close(f.OpenCh) })
}

func (f *ReverseForward) Result() (uint32, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.port, f.errText
}

// Server manages WebSocket clients and SSH proxy
type Server struct {
	clients      map[string]*ClientConn
	mu           sync.RWMutex
	sessions     map[string]*ProxySession
	sessMu       sync.RWMutex
	forwards     map[string]*ProxyForward
	fwdMu        sync.RWMutex
	revForwards  map[string]*ReverseForward
	revMu        sync.RWMutex
	fileResults  map[string]chan *protocol.Message
	fileRequests map[string]*fileSocket
	fileTasks    map[string]*fileTaskRoute
	fileMu       sync.RWMutex
	desktops     map[string]*desktopRoute
	desktopMu    sync.RWMutex
	upgrader     *gws.Upgrader

	// Public config (set by main) for API/UI
	SSHPort          string // e.g. "2222"
	HTTPHost         string // e.g. "192.168.1.100:8080"
	AdminToken       string // optional token for web APIs and browser WebSockets
	MaxSessions      int    // maximum concurrent sessions per device
	MaxForwards      int    // maximum concurrent forwards per device
	BatchConcurrency int    // maximum concurrent batch operations
}

// NewServer creates a new Server
func NewServer() *Server {
	s := &Server{
		clients:          make(map[string]*ClientConn),
		sessions:         make(map[string]*ProxySession),
		forwards:         make(map[string]*ProxyForward),
		revForwards:      make(map[string]*ReverseForward),
		fileResults:      make(map[string]chan *protocol.Message),
		fileRequests:     make(map[string]*fileSocket),
		fileTasks:        make(map[string]*fileTaskRoute),
		desktops:         make(map[string]*desktopRoute),
		MaxSessions:      256,
		MaxForwards:      1024,
		BatchConcurrency: runtime.GOMAXPROCS(0) * 8,
	}
	s.upgrader = gws.NewUpgrader(&wsHandler{srv: s}, &gws.ServerOption{
		ReadMaxPayloadSize: 16 * 1024 * 1024,
		ParallelGolimit:    runtime.GOMAXPROCS(0),
		PermessageDeflate: gws.PermessageDeflate{
			Enabled:               true,
			ServerContextTakeover: true,
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

func closeClientResources(s *Server, client *ClientConn) {
	client.mu.Lock()
	defer client.mu.Unlock()
	for sid, sess := range client.Sessions {
		sess.SignalClose()
		s.sessMu.Lock()
		delete(s.sessions, sid)
		s.sessMu.Unlock()
	}
	for fid, fwd := range client.Forwards {
		fwd.SignalClose()
		s.fwdMu.Lock()
		delete(s.forwards, fid)
		s.fwdMu.Unlock()
	}
	var cancels []func()
	s.revMu.Lock()
	for id, fwd := range s.revForwards {
		if fwd.ClientID == client.ID {
			if fwd.Cancel != nil {
				cancels = append(cancels, fwd.Cancel)
			}
			delete(s.revForwards, id)
		}
	}
	s.revMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	s.closeDesktopForClient(client.ID)
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
		closeClientResources(h.srv, client)
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

	h.srv.mu.Lock()
	clientID := h.srv.allocateClientIDLocked(msg.ClientID)

	client := &ClientConn{
		ID:          clientID,
		Conn:        socket,
		ConnectedAt: time.Now(),
		Password:    msg.Password,
		Desktop:     cloneDesktopCapabilities(msg.DesktopCapabilities),
		Sessions:    make(map[string]*ProxySession),
		Forwards:    make(map[string]*ProxyForward),
	}

	socket.Session().Store("clientID", clientID)
	h.srv.clients[clientID] = client
	h.srv.mu.Unlock()

	if clientID != msg.ClientID {
		log.Printf("client registered: %s (requested %s)", clientID, msg.ClientID)
	} else {
		log.Printf("client registered: %s", clientID)
	}
	client.Send(&protocol.Message{
		Type:     protocol.MsgRegister,
		ClientID: clientID,
		SSHPort:  h.srv.SSHPort,
		HTTPHost: h.srv.HTTPHost,
	})
}

func cloneDesktopCapabilities(caps *protocol.DesktopCapabilities) *protocol.DesktopCapabilities {
	if caps == nil {
		return nil
	}
	clone := *caps
	if caps.Backends != nil {
		clone.Backends = append([]string(nil), caps.Backends...)
	}
	if caps.Sources != nil {
		clone.Sources = append([]protocol.DesktopSource(nil), caps.Sources...)
	}
	return &clone
}

func (s *Server) allocateClientIDLocked(base string) string {
	if _, ok := s.clients[base]; !ok {
		return base
	}
	for n := 2; ; n++ {
		candidate := base + "-" + strconv.Itoa(n)
		if _, ok := s.clients[candidate]; !ok {
			return candidate
		}
	}
}

func sendBytes(ch chan []byte, data []byte, label string) {
	select {
	case ch <- data:
	case <-time.After(30 * time.Second):
		log.Printf("dropping %s data after backpressure timeout", label)
	}
}

// handleBinaryMessage processes binary data frames from device clients
func (h *wsHandler) handleBinaryMessage(socket *gws.Conn, raw []byte) {
	if h.srv.handleFileManagerBinary(raw) {
		return
	}
	typ, id, payload, err := protocol.DecodeBinFrame(raw)
	if err != nil {
		return
	}

	// Avoid per-frame logs on hot data paths; this handler can run thousands of times per second.

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
			sendBytes(sess.WriteCh, data, "session stdout")
			sess.BroadcastOutput(data) // notify observers
		}

	case protocol.BinStderr:
		sess := h.srv.getSession(id)
		if sess != nil && len(data) > 0 {
			sendBytes(sess.StderrCh, data, "session stderr")
			sess.BroadcastStderr(data) // notify observers
		}

	case protocol.BinTCPData:
		fwd := h.srv.getForward(id)
		if fwd != nil && len(data) > 0 {
			sendBytes(fwd.WriteCh, data, "tcp forward")
		}
	case protocol.BinDesktopFrame:
		h.srv.handleDesktopFrame(id, data)
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
			sess.SignalClose()
		}

	// TCP forwarding control
	case protocol.MsgTCPOpen:
		// Connection succeeded on client device
		fwd := s.getForward(msg.ForwardID)
		if fwd != nil {
			fwd.SignalOpen()
		}

	case protocol.MsgTCPFail:
		fwd := s.getForward(msg.ForwardID)
		if fwd != nil {
			fwd.SignalFail(msg.Error)
			if fwd.CloseSSH != nil {
				fwd.CloseSSH()
			}
		}
		s.removeForward(msg.ForwardID)

	case protocol.MsgTCPClose:
		fwd := s.getForward(msg.ForwardID)
		if fwd != nil {
			fwd.SignalClose()
		}

	case protocol.MsgTCPListenOK:
		fwd := s.getReverseForward(msg.ListenID)
		if fwd != nil {
			if msg.Error != "" {
				fwd.SignalFail(msg.Error)
			} else {
				fwd.SignalOpen(uint32(msg.Port))
			}
		}

	case protocol.MsgTCPAccept:
		fwd := s.getReverseForward(msg.ListenID)
		if fwd != nil {
			go s.openReverseForwardChannel(fwd, client, msg)
		}

	// File distribution
	case protocol.MsgFileResult:
		s.handleFileResult(msg)
	case protocol.MsgFileListResult, protocol.MsgFileUploadReady, protocol.MsgFileDownloadStart, protocol.MsgFileTransferEnd, protocol.MsgFileTransferError:
		s.handleFileManagerMessage(msg)
	case protocol.MsgDesktopReady, protocol.MsgDesktopClose:
		s.handleDesktopMessage(msg)
	}
}

// GetClient returns a connected client by ID
func (s *Server) GetClient(clientID string) (*ClientConn, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.clients[clientID]
	return c, ok
}

// ConnectedDeviceCount returns the number of connected device clients.
func (s *Server) ConnectedDeviceCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.clients)
}

// Session management
// RegisterSession atomically reserves a session slot for a device.
func (s *Server) RegisterSession(sess *ProxySession, client *ClientConn) bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	if s.MaxSessions > 0 && len(client.Sessions) >= s.MaxSessions {
		return false
	}
	s.sessMu.Lock()
	s.sessions[sess.ID] = sess
	s.sessMu.Unlock()
	client.Sessions[sess.ID] = sess
	return true
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
// RegisterForward atomically reserves a forward slot for a device.
func (s *Server) RegisterForward(fwd *ProxyForward, client *ClientConn) bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	if s.MaxForwards > 0 && len(client.Forwards) >= s.MaxForwards {
		return false
	}
	s.fwdMu.Lock()
	s.forwards[fwd.ID] = fwd
	s.fwdMu.Unlock()
	client.Forwards[fwd.ID] = fwd
	return true
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

func (s *Server) RegisterReverseForward(fwd *ReverseForward) {
	s.revMu.Lock()
	s.revForwards[fwd.ID] = fwd
	s.revMu.Unlock()
}

func (s *Server) getReverseForward(id string) *ReverseForward {
	s.revMu.RLock()
	defer s.revMu.RUnlock()
	return s.revForwards[id]
}

func (s *Server) removeReverseForward(id string) {
	s.revMu.Lock()
	delete(s.revForwards, id)
	s.revMu.Unlock()
}

func (s *Server) openReverseForwardChannel(rev *ReverseForward, client *ClientConn, msg *protocol.Message) {
	if rev.SSHConn == nil {
		return
	}
	originHost, originPort := splitHostPort(msg.SourceAddr)
	payload := gossh.Marshal(&remoteForwardChannelData{
		DestAddr:   rev.BindAddr,
		DestPort:   rev.BindPort,
		OriginAddr: originHost,
		OriginPort: uint32(originPort),
	})
	ch, reqs, err := rev.SSHConn.OpenChannel("forwarded-tcpip", payload)
	if err != nil {
		log.Printf("ssh fwd -R(device): open channel failed: %v", err)
		client.Send(&protocol.Message{Type: protocol.MsgTCPClose, ForwardID: msg.ForwardID})
		return
	}
	go gossh.DiscardRequests(reqs)

	proxy := &ProxyForward{
		ID:       msg.ForwardID,
		ClientID: rev.ClientID,
		WriteCh:  make(chan []byte, 16384),
		CloseCh:  make(chan struct{}, 1),
		OpenCh:   make(chan struct{}),
		Done:     make(chan struct{}),
		CloseSSH: func() { ch.Close() },
	}
	proxy.SignalOpen()
	if !s.RegisterForward(proxy, client) {
		ch.Close()
		client.Send(&protocol.Message{Type: protocol.MsgTCPClose, ForwardID: msg.ForwardID})
		return
	}
	client.Send(&protocol.Message{Type: protocol.MsgTCPOpen, ForwardID: msg.ForwardID})
	defer func() {
		s.removeForward(msg.ForwardID)
		client.mu.Lock()
		delete(client.Forwards, msg.ForwardID)
		client.mu.Unlock()
	}()

	var once sync.Once
	cleanup := func() { once.Do(func() { close(proxy.Done) }) }

	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := ch.Read(buf)
			if n > 0 {
				client.SendBinary(protocol.BinTCPData, msg.ForwardID, buf[:n])
			}
			if err != nil {
				client.Send(&protocol.Message{Type: protocol.MsgTCPClose, ForwardID: msg.ForwardID})
				cleanup()
				return
			}
		}
	}()

	go func() {
		for data := range proxy.WriteCh {
			if _, err := ch.Write(data); err != nil {
				cleanup()
				return
			}
		}
		ch.Close()
		cleanup()
	}()

	go func() {
		<-proxy.CloseCh
		proxy.CloseOutput()
	}()

	<-proxy.Done
}

func splitHostPort(addr string) (string, int) {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return addr, 0
	}
	port, _ := strconv.Atoi(portText)
	return host, port
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

func (s *Server) authOK(r *http.Request) bool {
	if s.AdminToken == "" {
		return true
	}
	token := r.Header.Get("X-RDev-Token")
	if token == "" {
		token = r.URL.Query().Get("token")
	}
	if token == "" {
		auth := r.Header.Get("Authorization")
		token = strings.TrimPrefix(auth, "Bearer ")
	}
	return token == s.AdminToken
}

func (s *Server) requireAuth(w http.ResponseWriter, r *http.Request) bool {
	if s.authOK(r) {
		return true
	}
	http.Error(w, "unauthorized", http.StatusUnauthorized)
	return false
}

// HandleAPI returns the list of connected clients as JSON
func (s *Server) HandleAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuth(w, r) {
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	type clientInfo struct {
		ID          string                        `json:"id"`
		ConnectedAt string                        `json:"connectedAt"`
		Sessions    int                           `json:"sessions"`
		Forwards    int                           `json:"forwards"`
		HasPassword bool                          `json:"hasPassword"`
		Desktop     *protocol.DesktopCapabilities `json:"desktop,omitempty"`
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
			Desktop:     cloneDesktopCapabilities(c.Desktop),
		})
	}

	sort.Slice(clients, func(i, j int) bool {
		if clients[i].ID != clients[j].ID {
			return clients[i].ID < clients[j].ID
		}
		return clients[i].ConnectedAt < clients[j].ConnectedAt
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(clients)
}

// HandleConfigAPI returns server configuration for the web UI
func (s *Server) HandleConfigAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"sshPort":      s.SSHPort,
		"httpHost":     s.HTTPHost,
		"authRequired": map[bool]string{true: "true", false: "false"}[s.AdminToken != ""],
	})
}

// HandleTerminalAPI returns available devices for the terminal page
func (s *Server) HandleTerminalAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuth(w, r) {
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	type deviceInfo struct {
		ID          string                        `json:"id"`
		ConnectedAt string                        `json:"connectedAt"`
		HasPassword bool                          `json:"hasPassword"`
		Desktop     *protocol.DesktopCapabilities `json:"desktop,omitempty"`
	}

	var devices []deviceInfo
	for _, c := range s.clients {
		devices = append(devices, deviceInfo{
			ID:          c.ID,
			ConnectedAt: c.ConnectedAt.Format(time.RFC3339),
			HasPassword: c.Password != "",
			Desktop:     cloneDesktopCapabilities(c.Desktop),
		})
	}

	sort.Slice(devices, func(i, j int) bool {
		if devices[i].ID != devices[j].ID {
			return devices[i].ID < devices[j].ID
		}
		return devices[i].ConnectedAt < devices[j].ConnectedAt
	})

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

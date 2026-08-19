package server

import (
	"context"
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lxzan/gws"
	kcp "github.com/xtaci/kcp-go/v5"
	gossh "golang.org/x/crypto/ssh"
	"rdev/internal/protocol"
	tframe "rdev/internal/transport"
)

//go:embed static
var templateFS embed.FS

const sessionHistoryLimit = 1024 * 1024

const (
	wsWriteWait      = 10 * time.Second
	wsReadWait       = 75 * time.Second
	wsPingPeriod     = 25 * time.Second
	releaseLatestTTL = 5 * time.Minute

	releaseDownloadProbeBytes   = 64 * 1024
	releaseDownloadProbeTimeout = 4 * time.Second
	releaseDownloadChoiceTTL    = 10 * time.Minute
)

var releaseDownloadMirrors = []string{
	"gh.idayer.com",
	"gh.ddlc.top",
	"gh-proxy.com",
	"ghfast.top",
	"ghproxy.net",
	"ghproxy.cc",
	"gh-proxy.net",
	"ghproxy.cfd",
	"github.moeyy.xyz",
	"hub.gitmirror.com",
	"ghproxy.1888866.xyz",
	"ghproxy.sakuramoe.dev",
}

type releaseDownloadChoice struct {
	url       string
	expiresAt time.Time
}

var (
	releaseDownloadChoiceMu sync.Mutex
	releaseDownloadChoices  = make(map[string]releaseDownloadChoice)
)

// ClientConn represents a connected client device
type ClientConn struct {
	ID           string
	RequestedID  string
	InstanceID   string
	Version      string
	Conn         *gws.Conn
	Transport    DeviceTransport
	ConnectedAt  time.Time
	Password     string
	Sessions     map[string]*ProxySession
	Forwards     map[string]*ProxyForward
	Desktop      *protocol.DesktopCapabilities
	LogSupported bool
	writeMu      sync.Mutex
	mu           sync.Mutex
}

type DeviceTransport interface {
	WriteJSON([]byte) error
	WriteBinary([]byte) error
	WritePing([]byte) error
	Close(string) error
	RemoteAddr() string
}

type wsDeviceTransport struct{ conn *gws.Conn }

func (t *wsDeviceTransport) WriteJSON(data []byte) error {
	_ = t.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
	defer t.conn.SetWriteDeadline(time.Time{})
	return t.conn.WriteMessage(gws.OpcodeText, data)
}

func (t *wsDeviceTransport) WriteBinary(data []byte) error {
	_ = t.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
	defer t.conn.SetWriteDeadline(time.Time{})
	return t.conn.WriteMessage(gws.OpcodeBinary, data)
}

func (t *wsDeviceTransport) WritePing(payload []byte) error {
	_ = t.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
	defer t.conn.SetWriteDeadline(time.Time{})
	return t.conn.WritePing(payload)
}

func (t *wsDeviceTransport) Close(reason string) error {
	return t.conn.WriteClose(1000, []byte(reason))
}

func (t *wsDeviceTransport) RemoteAddr() string { return "" }

type streamDeviceTransport struct {
	conn net.Conn
	mu   sync.Mutex
}

func (t *streamDeviceTransport) write(kind byte, payload []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	_ = t.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
	defer t.conn.SetWriteDeadline(time.Time{})
	return tframe.WriteFrame(t.conn, kind, payload)
}

func (t *streamDeviceTransport) WriteJSON(data []byte) error { return t.write(tframe.KindJSON, data) }
func (t *streamDeviceTransport) WriteBinary(data []byte) error {
	return t.write(tframe.KindBinary, data)
}
func (t *streamDeviceTransport) WritePing(payload []byte) error {
	return t.write(tframe.KindPing, payload)
}
func (t *streamDeviceTransport) Close(reason string) error {
	_ = t.write(tframe.KindClose, []byte(reason))
	return t.conn.Close()
}
func (t *streamDeviceTransport) RemoteAddr() string { return t.conn.RemoteAddr().String() }

// Send sends a JSON control message to the client (text frame)
func (c *ClientConn) Send(msg *protocol.Message) error {
	data, err := protocol.Encode(msg)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.Transport == nil {
		return net.ErrClosed
	}
	return c.Transport.WriteJSON(data)
}

// SendBinary sends a binary data frame to the client
func (c *ClientConn) SendBinary(typ byte, id string, data []byte) error {
	frame := protocol.EncodeBinFrame(typ, id, data)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.Transport == nil {
		return net.ErrClosed
	}
	return c.Transport.WriteBinary(frame)
}

// SendFilePut sends a file to the client device (binary frame)
func (c *ClientConn) SendFilePut(id, path string, mode int32, fileData []byte) error {
	frame := protocol.EncodeBinFilePut(id, path, mode, fileData)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.Transport == nil {
		return net.ErrClosed
	}
	return c.Transport.WriteBinary(frame)
}

func (c *ClientConn) SendFileStart(id, path string, mode int32) error {
	frame := protocol.EncodeBinFileStart(id, path, mode)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.Transport == nil {
		return net.ErrClosed
	}
	return c.Transport.WriteBinary(frame)
}

func (c *ClientConn) SendFileChunk(id string, data []byte) error {
	frame := protocol.EncodeBinFrame(protocol.BinFileChunk, id, data)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.Transport == nil {
		return net.ErrClosed
	}
	return c.Transport.WriteBinary(frame)
}

func (c *ClientConn) SendFileEnd(id string) error {
	frame := protocol.EncodeBinFrame(protocol.BinFileEnd, id, nil)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.Transport == nil {
		return net.ErrClosed
	}
	return c.Transport.WriteBinary(frame)
}

func (c *ClientConn) SendBinaryOffset(typ byte, id string, offset int64, data []byte) error {
	frame := protocol.EncodeBinFrameOffset(typ, id, offset, data)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.Transport == nil {
		return net.ErrClosed
	}
	return c.Transport.WriteBinary(frame)
}

func (c *ClientConn) SendPing() error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.Transport == nil {
		return net.ErrClosed
	}
	return c.Transport.WritePing(nil)
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
	clients           map[string]*ClientConn
	mu                sync.RWMutex
	sessions          map[string]*ProxySession
	sessMu            sync.RWMutex
	forwards          map[string]*ProxyForward
	fwdMu             sync.RWMutex
	revForwards       map[string]*ReverseForward
	revMu             sync.RWMutex
	fileResults       map[string]chan *protocol.Message
	fileRequests      map[string]*fileSocket
	fileTasks         map[string]*fileTaskRoute
	fileMu            sync.RWMutex
	desktops          map[string]*desktopRoute
	desktopMu         sync.RWMutex
	vncMu             sync.RWMutex
	vncSettings       map[string]protocol.Message
	vncStreams        map[string]*vncDesktopStream
	gpuDesktopMu      sync.RWMutex
	gpuDesktopTunnels map[string]*gpuDesktopTunnel
	releaseLatestMu   sync.Mutex
	releaseLatestTag  string
	releaseLatestAt   time.Time
	upgrader          *gws.Upgrader

	// Public config (set by main) for API/UI
	SSHPort          string // e.g. "2222"
	HTTPHost         string // e.g. "192.168.1.100:8080"
	TCPPort          string
	KCPPort          string
	AdminToken       string // optional token for web APIs and browser WebSockets
	VNCAddr          string // optional VNC/RFB listen address
	MaxSessions      int    // maximum concurrent sessions per device
	MaxForwards      int    // maximum concurrent forwards per device
	BatchConcurrency int    // maximum concurrent batch operations
	ReleaseVersion   string // server release version, injected by main
	ClientLogs       *ClientLogManager
}

// NewServer creates a new Server
func NewServer() *Server {
	s := &Server{
		clients:           make(map[string]*ClientConn),
		sessions:          make(map[string]*ProxySession),
		forwards:          make(map[string]*ProxyForward),
		revForwards:       make(map[string]*ReverseForward),
		fileResults:       make(map[string]chan *protocol.Message),
		fileRequests:      make(map[string]*fileSocket),
		fileTasks:         make(map[string]*fileTaskRoute),
		desktops:          make(map[string]*desktopRoute),
		vncSettings:       make(map[string]protocol.Message),
		vncStreams:        make(map[string]*vncDesktopStream),
		gpuDesktopTunnels: make(map[string]*gpuDesktopTunnel),
		MaxSessions:       256,
		MaxForwards:       1024,
		BatchConcurrency:  runtime.GOMAXPROCS(0) * 8,
		ClientLogs:        NewClientLogManager("", 0, 0),
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
	s.closeGPUDesktopTunnelForClient(client.ID)
}

func (s *Server) clientByID(id string) *ClientConn {
	s.mu.RLock()
	client := s.clients[id]
	s.mu.RUnlock()
	return client
}

func (h *wsHandler) OnOpen(socket *gws.Conn) {
	_ = socket.SetDeadline(time.Now().Add(wsReadWait))
}

func (h *wsHandler) OnPing(socket *gws.Conn, payload []byte) {
	_ = socket.SetDeadline(time.Now().Add(wsReadWait))
	clientID, _ := socket.Session().Load("clientID")
	if clientID == nil {
		_ = socket.WritePong(payload)
		return
	}
	client := h.srv.clientByID(clientID.(string))
	if client == nil || client.Conn != socket {
		return
	}
	_ = socket.WritePong(payload)
}

func (h *wsHandler) OnPong(socket *gws.Conn, payload []byte) {
	_ = socket.SetDeadline(time.Now().Add(wsReadWait))
}

func (h *wsHandler) OnClose(socket *gws.Conn, err error) {
	clientID, _ := socket.Session().Load("clientID")
	if clientID == nil {
		return
	}
	id := clientID.(string)

	client, ok := h.srv.unregisterClient(id, socket)
	if ok {
		closeClientResources(h.srv, client)
		log.Printf("client unregistered: %s", id)
	}
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
	if !ok || client.Conn != socket {
		return
	}

	h.srv.handleClientMessage(client, msg)
}

func (h *wsHandler) handleRegister(socket *gws.Conn, msg *protocol.Message) {
	if msg.ClientID == "" {
		socket.WriteClose(1000, nil)
		return
	}

	clientID := strings.TrimSpace(msg.ClientID)
	if clientID == "" {
		socket.WriteClose(1000, nil)
		return
	}
	instanceID := strings.TrimSpace(msg.InstanceID)
	client := &ClientConn{
		ID:           clientID,
		RequestedID:  clientID,
		InstanceID:   instanceID,
		Version:      msg.ClientVersion,
		Conn:         socket,
		Transport:    &wsDeviceTransport{conn: socket},
		ConnectedAt:  time.Now(),
		Password:     msg.Password,
		Desktop:      cloneDesktopCapabilities(msg.DesktopCapabilities),
		LogSupported: msg.LogSupported,
		Sessions:     make(map[string]*ProxySession),
		Forwards:     make(map[string]*ProxyForward),
	}

	socket.Session().Store("clientID", clientID)
	old, assignedID, duplicate := h.srv.registerClient(client)
	socket.Session().Store("clientID", assignedID)

	if old != nil && old.Transport != client.Transport {
		log.Printf("client reconnected: requested=%s assigned=%s", clientID, assignedID)
		closeClientResources(h.srv, old)
		if old.Transport != nil {
			_ = old.Transport.Close("connection replaced")
		}
	} else if duplicate {
		log.Printf("client duplicate ID assigned: requested=%s assigned=%s", clientID, assignedID)
	} else {
		log.Printf("client registered: %s", assignedID)
	}
	client.Send(&protocol.Message{
		Type:       protocol.MsgRegister,
		ClientID:   assignedID,
		InstanceID: instanceID,
		SSHPort:    h.srv.SSHPort,
		HTTPHost:   h.srv.HTTPHost,
	})
	h.srv.sendClientLogConfig(client)
	go h.srv.clientPingLoop(client)
}

func cloneDesktopCapabilities(caps *protocol.DesktopCapabilities) *protocol.DesktopCapabilities {
	if caps == nil {
		return nil
	}
	clone := *caps
	if caps.Backends != nil {
		clone.Backends = append([]string(nil), caps.Backends...)
	}
	if caps.InputBackends != nil {
		clone.InputBackends = append([]string(nil), caps.InputBackends...)
	}
	if caps.InputOptions != nil {
		clone.InputOptions = append([]protocol.DesktopInputBackend(nil), caps.InputOptions...)
		for i := range clone.InputOptions {
			clone.InputOptions[i].Kinds = append([]string(nil), caps.InputOptions[i].Kinds...)
			clone.InputOptions[i].Requires = append([]string(nil), caps.InputOptions[i].Requires...)
		}
	}
	if caps.VideoCodecs != nil {
		clone.VideoCodecs = append([]string(nil), caps.VideoCodecs...)
	}
	if caps.EncoderBackends != nil {
		clone.EncoderBackends = append([]string(nil), caps.EncoderBackends...)
	}
	if caps.Sources != nil {
		clone.Sources = append([]protocol.DesktopSource(nil), caps.Sources...)
	}
	return &clone
}

func (s *Server) clientGPUDesktopAvailable(client *ClientConn) bool {
	if client == nil {
		return false
	}
	if s.gpuDesktopTunnel(client.ID) != nil {
		return true
	}
	return desktopCapabilitiesIncludeGPUTunnel(client.Desktop)
}

func desktopCapabilitiesIncludeGPUTunnel(caps *protocol.DesktopCapabilities) bool {
	if caps == nil || !caps.Supported {
		return false
	}
	for _, backend := range caps.Backends {
		switch strings.ToLower(strings.TrimSpace(backend)) {
		case "gpu-desktop-tunnel", "rdev-desktop", "pipewire":
			return true
		}
	}
	return false
}

func (s *Server) registerClient(client *ClientConn) (*ClientConn, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	requestedID := strings.TrimSpace(client.RequestedID)
	if requestedID == "" {
		requestedID = strings.TrimSpace(client.ID)
	}
	client.RequestedID = requestedID

	if client.InstanceID != "" {
		for id, old := range s.clients {
			if old.InstanceID == client.InstanceID {
				client.ID = id
				s.clients[id] = client
				return old, id, id != requestedID
			}
		}
	}

	if old := s.clients[requestedID]; old == nil || (client.InstanceID != "" && old.InstanceID == client.InstanceID) {
		client.ID = requestedID
		s.clients[requestedID] = client
		return old, requestedID, false
	}

	assignedID := s.nextAvailableClientID(requestedID)
	client.ID = assignedID
	s.clients[assignedID] = client
	return nil, assignedID, true
}

func (s *Server) nextAvailableClientID(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "device"
	}
	for i := 2; ; i++ {
		candidate := base + "-" + strconv.Itoa(i)
		if _, exists := s.clients[candidate]; !exists {
			return candidate
		}
	}
}

func (s *Server) unregisterClient(id string, conn *gws.Conn) (*ClientConn, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	client, ok := s.clients[id]
	if !ok || client.Conn != conn {
		return nil, false
	}
	delete(s.clients, id)
	return client, true
}

func (s *Server) unregisterClientTransport(id string, transport DeviceTransport) (*ClientConn, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	client, ok := s.clients[id]
	if !ok || client.Transport != transport {
		return nil, false
	}
	delete(s.clients, id)
	return client, true
}

func (s *Server) clientPingLoop(client *ClientConn) {
	ticker := time.NewTicker(wsPingPeriod)
	defer ticker.Stop()
	for range ticker.C {
		if current := s.clientByID(client.ID); current != client {
			return
		}
		if err := client.SendPing(); err != nil {
			if client.Transport != nil {
				_ = client.Transport.Close("ping failed")
			}
			return
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
	clientID, _ := socket.Session().Load("clientID")
	if clientID == nil {
		return
	}

	h.srv.mu.RLock()
	client, ok := h.srv.clients[clientID.(string)]
	h.srv.mu.RUnlock()
	if !ok || client.Conn != socket {
		return
	}
	h.srv.handleClientBinary(client, raw)
}

func (s *Server) ServeTCP(addr string) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handleStreamDeviceConn(conn, "tcp")
		}
	}()
	return ln, nil
}

func (s *Server) ServeKCP(addr string) (*kcp.Listener, error) {
	ln, err := kcp.ListenWithOptions(addr, nil, 0, 0)
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			conn, err := ln.AcceptKCP()
			if err != nil {
				return
			}
			conn.SetNoDelay(1, 20, 2, 1)
			conn.SetWindowSize(256, 256)
			conn.SetMtu(1200)
			conn.SetStreamMode(true)
			conn.SetWriteDelay(false)
			go s.handleStreamDeviceConn(conn, "kcp")
		}
	}()
	return ln, nil
}

func (s *Server) handleStreamDeviceConn(conn net.Conn, label string) {
	transport := &streamDeviceTransport{conn: conn}
	var clientID string
	var registered bool
	defer func() {
		if registered {
			if client, ok := s.unregisterClientTransport(clientID, transport); ok {
				closeClientResources(s, client)
				log.Printf("client unregistered: %s", clientID)
			}
		}
		conn.Close()
	}()

	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		frame, err := tframe.ReadFrame(conn)
		if err != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(wsReadWait))
		switch frame.Kind {
		case tframe.KindJSON:
			msg, err := protocol.Decode(frame.Payload)
			if err != nil {
				continue
			}
			if !registered {
				if msg.Type == protocol.MsgGPUDesktopTunnel {
					s.handleGPUDesktopStreamTunnel(conn, msg)
					return
				}
				if msg.Type != protocol.MsgRegister {
					return
				}
				assigned, ok := s.registerStreamClient(transport, msg, label)
				if !ok {
					return
				}
				clientID = assigned
				registered = true
				continue
			}
			client := s.clientByID(clientID)
			if client == nil || client.Transport != transport {
				return
			}
			s.handleClientMessage(client, msg)
		case tframe.KindBinary:
			if !registered {
				continue
			}
			client := s.clientByID(clientID)
			if client == nil || client.Transport != transport {
				return
			}
			s.handleClientBinary(client, frame.Payload)
		case tframe.KindPing:
			_ = transport.write(tframe.KindPong, frame.Payload)
		case tframe.KindPong:
		case tframe.KindClose:
			return
		}
	}
}

func (s *Server) registerStreamClient(transport DeviceTransport, msg *protocol.Message, label string) (string, bool) {
	clientID := strings.TrimSpace(msg.ClientID)
	if clientID == "" {
		_ = transport.Close("missing client id")
		return "", false
	}
	instanceID := strings.TrimSpace(msg.InstanceID)
	client := &ClientConn{
		ID:           clientID,
		RequestedID:  clientID,
		InstanceID:   instanceID,
		Version:      msg.ClientVersion,
		Transport:    transport,
		ConnectedAt:  time.Now(),
		Password:     msg.Password,
		Desktop:      cloneDesktopCapabilities(msg.DesktopCapabilities),
		LogSupported: msg.LogSupported,
		Sessions:     make(map[string]*ProxySession),
		Forwards:     make(map[string]*ProxyForward),
	}
	old, assignedID, duplicate := s.registerClient(client)
	if old != nil && old.Transport != transport {
		log.Printf("client reconnected via %s: requested=%s assigned=%s", label, clientID, assignedID)
		closeClientResources(s, old)
		if old.Transport != nil {
			_ = old.Transport.Close("connection replaced")
		}
	} else if duplicate {
		log.Printf("client duplicate ID assigned via %s: requested=%s assigned=%s", label, clientID, assignedID)
	} else {
		log.Printf("client registered via %s: %s", label, assignedID)
	}
	_ = client.Send(&protocol.Message{Type: protocol.MsgRegister, ClientID: assignedID, InstanceID: instanceID, SSHPort: s.SSHPort, HTTPHost: s.HTTPHost})
	s.sendClientLogConfig(client)
	go s.clientPingLoop(client)
	return assignedID, true
}

func (s *Server) handleClientBinary(client *ClientConn, raw []byte) {
	if s.handleFileManagerBinary(raw) {
		return
	}
	typ, id, payload, err := protocol.DecodeBinFrame(raw)
	if err != nil {
		return
	}
	data := append([]byte(nil), payload...)
	switch typ {
	case protocol.BinData:
		sess := s.getSession(id)
		if sess != nil && len(data) > 0 {
			sendBytes(sess.WriteCh, data, "session stdout")
			sess.BroadcastOutput(data)
		}
	case protocol.BinStderr:
		sess := s.getSession(id)
		if sess != nil && len(data) > 0 {
			sendBytes(sess.StderrCh, data, "session stderr")
			sess.BroadcastStderr(data)
		}
	case protocol.BinTCPData:
		fwd := s.getForward(id)
		if fwd != nil && len(data) > 0 {
			sendBytes(fwd.WriteCh, data, "tcp forward")
		}
	case protocol.BinDesktopFrame:
		s.handleDesktopFrame(id, data)
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
	case protocol.MsgFileListResult, protocol.MsgFileUploadReady, protocol.MsgFileDownloadStart, protocol.MsgFileTransferEnd, protocol.MsgFileTransferError,
		"file_stat_result", "file_mkdir_result", "file_delete_result", "file_rename_result", "file_copy_result":
		s.handleFileManagerMessage(msg)
	case protocol.MsgDesktopReady, protocol.MsgDesktopClose, protocol.MsgDesktopClipboard:
		s.handleDesktopMessage(msg)
	case protocol.MsgLogBatch:
		s.handleClientLogBatch(client, msg)
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
		ID           string                        `json:"id"`
		RequestedID  string                        `json:"requestedId,omitempty"`
		InstanceID   string                        `json:"instanceId,omitempty"`
		Version      string                        `json:"version,omitempty"`
		ConnectedAt  string                        `json:"connectedAt"`
		Sessions     int                           `json:"sessions"`
		Forwards     int                           `json:"forwards"`
		HasPassword  bool                          `json:"hasPassword"`
		Desktop      *protocol.DesktopCapabilities `json:"desktop,omitempty"`
		GPUDesktop   bool                          `json:"gpuDesktop,omitempty"`
		LogSupported bool                          `json:"logSupported,omitempty"`
	}

	clients := make([]clientInfo, 0, len(s.clients))
	for _, c := range s.clients {
		c.mu.Lock()
		n := len(c.Sessions)
		f := len(c.Forwards)
		c.mu.Unlock()
		clients = append(clients, clientInfo{
			ID:           c.ID,
			RequestedID:  c.RequestedID,
			InstanceID:   c.InstanceID,
			Version:      c.Version,
			ConnectedAt:  c.ConnectedAt.Format(time.RFC3339),
			Sessions:     n,
			Forwards:     f,
			HasPassword:  c.Password != "",
			Desktop:      cloneDesktopCapabilities(c.Desktop),
			GPUDesktop:   s.clientGPUDesktopAvailable(c),
			LogSupported: c.LogSupported,
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

func releaseDownloadDirectURL(asset, tag string) string {
	asset = url.PathEscape(asset)
	if tag == "" || tag == "latest" {
		return "https://github.com/icepie/rdev/releases/latest/download/" + asset
	}
	return "https://github.com/icepie/rdev/releases/download/" + url.PathEscape(tag) + "/" + asset
}

func releaseDownloadParams(r *http.Request) (asset, tag string, ok bool) {
	asset = strings.TrimSpace(r.URL.Query().Get("asset"))
	tag = strings.TrimSpace(r.URL.Query().Get("tag"))
	if tag == "" {
		tag = "latest"
	}
	if asset == "" || strings.Contains(asset, "/") || strings.Contains(asset, "\\") || strings.Contains(asset, "..") {
		return "", "", false
	}
	if tag != "latest" {
		clean := strings.TrimPrefix(tag, "v")
		if clean == "" || strings.ContainsAny(clean, "/\\?") || strings.Contains(tag, "..") {
			return "", "", false
		}
		if !strings.HasPrefix(tag, "v") {
			tag = "v" + tag
		}
	}
	return asset, tag, true
}

func releaseDownloadCandidates(asset, tag string) []string {
	direct := releaseDownloadDirectURL(asset, tag)
	candidates := make([]string, 0, len(releaseDownloadMirrors)+1)
	for _, mirror := range releaseDownloadMirrors {
		candidates = append(candidates, "https://"+mirror+"/"+direct)
	}
	return append(candidates, direct)
}

func releaseDownloadProxyCandidates(asset, tag string) []string {
	direct := releaseDownloadDirectURL(asset, tag)
	candidates := []string{direct}
	for _, mirror := range releaseDownloadMirrors {
		candidates = append(candidates, "https://"+mirror+"/"+direct)
	}
	return candidates
}

func releaseDownloadChoiceKey(asset, tag string) string {
	return tag + "\x00" + asset
}

func cachedReleaseDownloadChoice(key string, now time.Time) string {
	releaseDownloadChoiceMu.Lock()
	defer releaseDownloadChoiceMu.Unlock()
	choice, ok := releaseDownloadChoices[key]
	if !ok || !choice.expiresAt.After(now) {
		delete(releaseDownloadChoices, key)
		return ""
	}
	return choice.url
}

func cacheReleaseDownloadChoice(key, candidate string, now time.Time) {
	releaseDownloadChoiceMu.Lock()
	releaseDownloadChoices[key] = releaseDownloadChoice{url: candidate, expiresAt: now.Add(releaseDownloadChoiceTTL)}
	releaseDownloadChoiceMu.Unlock()
}

type releaseDownloadProbeResult struct {
	candidate string
	bytes     int64
	duration  time.Duration
}

// fastestReleaseDownloadCandidate measures a small range from every source.
// The selected URL is cached so ordinary client installs redirect immediately.
func fastestReleaseDownloadCandidate(ctx context.Context, candidates []string) string {
	ctx, cancel := context.WithTimeout(ctx, releaseDownloadProbeTimeout)
	defer cancel()

	results := make(chan releaseDownloadProbeResult, len(candidates))
	var wg sync.WaitGroup
	for _, candidate := range candidates {
		candidate := candidate
		wg.Add(1)
		go func() {
			defer wg.Done()
			started := time.Now()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, candidate, nil)
			if err != nil {
				return
			}
			req.Header.Set("Range", "bytes=0-65535")
			req.Header.Set("User-Agent", "rdev-server")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
				return
			}
			n, _ := io.CopyN(io.Discard, resp.Body, releaseDownloadProbeBytes)
			if n > 0 {
				results <- releaseDownloadProbeResult{candidate: candidate, bytes: n, duration: time.Since(started)}
			}
		}()
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	best := ""
	var bestRate float64
	for result := range results {
		if result.duration <= 0 {
			continue
		}
		rate := float64(result.bytes) / result.duration.Seconds()
		if rate > bestRate {
			best, bestRate = result.candidate, rate
		}
	}
	return best
}

// HandleReleaseDownload redirects clients to the fastest reachable release source.
// Unlike the streaming proxy, the client downloads directly from the selected source.
func (s *Server) HandleReleaseDownload(w http.ResponseWriter, r *http.Request) {
	asset, tag, ok := releaseDownloadParams(r)
	if !ok {
		http.Error(w, "bad asset", http.StatusBadRequest)
		return
	}
	key := releaseDownloadChoiceKey(asset, tag)
	if candidate := cachedReleaseDownloadChoice(key, time.Now()); candidate != "" {
		http.Redirect(w, r, candidate, http.StatusFound)
		return
	}
	candidate := fastestReleaseDownloadCandidate(r.Context(), releaseDownloadCandidates(asset, tag))
	if candidate == "" {
		candidate = releaseDownloadDirectURL(asset, tag)
	}
	cacheReleaseDownloadChoice(key, candidate, time.Now())
	http.Redirect(w, r, candidate, http.StatusFound)
}

// HandleReleaseDownloadProxy streams a release asset through the RDev server as a last-resort fallback.
func (s *Server) HandleReleaseDownloadProxy(w http.ResponseWriter, r *http.Request) {
	asset, tag, ok := releaseDownloadParams(r)
	if !ok {
		http.Error(w, "bad asset", http.StatusBadRequest)
		return
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   8 * time.Second,
		ResponseHeaderTimeout: 12 * time.Second,
		IdleConnTimeout:       30 * time.Second,
	}
	client := &http.Client{Transport: transport}
	candidates := releaseDownloadProxyCandidates(asset, tag)
	selected := ""
	for _, candidate := range candidates {
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, candidate, nil)
		if err == nil {
			req.Header.Set("User-Agent", "rdev-server")
			resp, err := client.Do(req)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode >= 200 && resp.StatusCode < 400 {
					selected = candidate
				}
			}
		}
		cancel()
		if selected != "" {
			break
		}
	}
	if selected == "" {
		selected = releaseDownloadDirectURL(asset, tag)
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, selected, nil)
	if err != nil {
		http.Error(w, "download failed", http.StatusBadGateway)
		return
	}
	req.Header.Set("User-Agent", "rdev-server")
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "download failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		http.Error(w, "download failed", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+asset+"\"")
	if length := resp.Header.Get("Content-Length"); length != "" {
		w.Header().Set("Content-Length", length)
	}
	_, _ = io.Copy(w, resp.Body)
}

func normalizeReleaseTag(tag string) string {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return ""
	}
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	return tag
}

func fetchLatestReleaseTag(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/icepie/rdev/releases/latest", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "rdev-server")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", io.ErrUnexpectedEOF
	}
	var data struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&data); err != nil {
		return "", err
	}
	tag := normalizeReleaseTag(data.TagName)
	if tag == "" {
		return "", io.ErrUnexpectedEOF
	}
	return tag, nil
}

func (s *Server) latestReleaseTag(ctx context.Context) (string, bool) {
	s.releaseLatestMu.Lock()
	if s.releaseLatestTag != "" && time.Since(s.releaseLatestAt) < releaseLatestTTL {
		tag := s.releaseLatestTag
		s.releaseLatestMu.Unlock()
		return tag, true
	}
	s.releaseLatestMu.Unlock()

	tag, err := fetchLatestReleaseTag(ctx)
	if err != nil {
		fallback := normalizeReleaseTag(s.ReleaseVersion)
		if fallback == "" {
			fallback = "latest"
		}
		return fallback, false
	}

	s.releaseLatestMu.Lock()
	s.releaseLatestTag = tag
	s.releaseLatestAt = time.Now()
	s.releaseLatestMu.Unlock()
	return tag, true
}

// HandleConfigAPI returns server configuration for the web UI
func (s *Server) HandleConfigAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"sshPort":      s.SSHPort,
		"httpHost":     s.HTTPHost,
		"tcpPort":      s.TCPPort,
		"kcpPort":      s.KCPPort,
		"vncAddr":      s.VNCAddr,
		"authRequired": map[bool]string{true: "true", false: "false"}[s.AdminToken != ""],
	})
}

// HandleReleaseLatestAPI returns the resolved latest release tag for runner cache keys.
func (s *Server) HandleReleaseLatestAPI(w http.ResponseWriter, r *http.Request) {
	tag, fresh := s.latestReleaseTag(r.Context())
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"tag":   tag,
		"fresh": fresh,
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
		ID           string                        `json:"id"`
		RequestedID  string                        `json:"requestedId,omitempty"`
		ConnectedAt  string                        `json:"connectedAt"`
		Version      string                        `json:"version,omitempty"`
		HasPassword  bool                          `json:"hasPassword"`
		Desktop      *protocol.DesktopCapabilities `json:"desktop,omitempty"`
		GPUDesktop   bool                          `json:"gpuDesktop,omitempty"`
		LogSupported bool                          `json:"logSupported,omitempty"`
	}

	devices := make([]deviceInfo, 0, len(s.clients))
	for _, c := range s.clients {
		devices = append(devices, deviceInfo{
			ID:           c.ID,
			RequestedID:  c.RequestedID,
			ConnectedAt:  c.ConnectedAt.Format(time.RFC3339),
			Version:      c.Version,
			HasPassword:  c.Password != "",
			Desktop:      cloneDesktopCapabilities(c.Desktop),
			GPUDesktop:   s.clientGPUDesktopAvailable(c),
			LogSupported: c.LogSupported,
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

// StaticPageHandler serves a named embedded UI page without exposing the .html suffix.
func (s *Server) StaticPageHandler(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		http.ServeFileFS(w, r, templateFS, "static/"+name)
	}
}

package server

import (
	"crypto/subtle"
	"embed"
	"log"
	"net"
	"net/http"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"rdev/internal/protocol"

	"github.com/lxzan/gws"
	gossh "golang.org/x/crypto/ssh"
)

//go:embed static
var templateFS embed.FS

const sessionHistoryLimit = 1024 * 1024

// clientIDPattern whitelists device IDs: the ID is reused as SSH username, URL
// query parameter, log identifier and map key across the server, so restrict it
// to a safe printable set with a bounded length.
var clientIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func validClientID(id string) bool {
	return clientIDPattern.MatchString(id)
}

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
	ID                string
	DisplayName       string
	RequestedID       string
	InstanceID        string
	DeviceIdentity    string
	ReconnectProof    string
	Version           string
	OS                string
	Conn              *gws.Conn
	Transport         DeviceTransport
	ConnectedAt       time.Time
	Password          string
	Sessions          map[string]*ProxySession
	Forwards          map[string]*ProxyForward
	FileOperations    []string
	FileRTC           bool
	TerminalRTC       bool
	Desktop           *protocol.DesktopCapabilities
	ConfinedDownloads bool
	writeMu           sync.Mutex
	mu                sync.Mutex
}

// clientOS reports a canonical GOOS name, falling back to desktop metadata
// for clients that predate the clientOS registration field.
func clientOS(c *ClientConn) string {
	if c == nil {
		return ""
	}
	osName := strings.TrimSpace(c.OS)
	if osName == "" && c.Desktop != nil {
		osName = strings.TrimSpace(c.Desktop.Platform)
	}
	switch osName = strings.ToLower(osName); osName {
	case "macos", "mac", "osx", "mac os x":
		return "darwin"
	case "win32", "win64", "win":
		return "windows"
	default:
		return osName
	}
}

func clientDisplayName(c *ClientConn) string {
	if name := strings.TrimSpace(c.DisplayName); name != "" {
		return name
	}
	return c.ID
}

// Server manages WebSocket clients and SSH proxy
type Server struct {
	health              serverHealth
	schedulerMu         sync.Mutex
	scheduler           *Scheduler
	automationMu        sync.Mutex
	desktopOwners       map[string]desktopAutomationLease
	BrowserEnabled      bool                 // explicit opt-in for server-hosted Chrome
	WebRTCEnabled       bool                 // explicit opt-in for browser-device desktop WebRTC
	WebRTCICEServers    []protocol.IceServer // nil uses legacy STUN; empty explicitly uses host candidates only
	browserMu           sync.Mutex
	browsers            map[string]*browserSession
	clients             map[string]*ClientConn
	deviceWatchers      map[chan struct{}]struct{}
	mu                  sync.RWMutex
	sessions            map[string]*ProxySession
	sessMu              sync.RWMutex
	forwards            map[string]*ProxyForward
	fwdMu               sync.RWMutex
	revForwards         map[string]*ReverseForward
	revMu               sync.RWMutex
	fileResults         map[string]chan *protocol.Message
	fileRequests        map[string]*fileRequestRoute
	fileTasks           map[string]*fileTaskRoute
	mediaMu             sync.Mutex
	mediaGrants         map[string]mediaGrant
	mediaSlots          chan struct{}
	fileMu              sync.RWMutex
	agentFileReqs       map[string]chan *protocol.Message
	agentFileTasks      map[string]*agentFileTask
	agentFileMu         sync.Mutex
	desktops            map[string]*desktopRoute
	desktopMu           sync.RWMutex
	vncMu               sync.RWMutex
	vncSettings         map[string]protocol.Message
	vncStreams          map[string]*vncDesktopStream
	gpuDesktopMu        sync.RWMutex
	gpuDesktopTunnels   map[string]*gpuDesktopTunnel
	proxyTunnelsMu      sync.Mutex
	proxyTunnels        map[string]*proxyTunnelPending
	releaseLatestMu     sync.Mutex
	releaseLatestTag    string
	releaseLatestAt     time.Time
	releaseLatestAssets map[string]releaseAssetTag
	upgrader            *gws.Upgrader
	authLimiter         *authRateLimiter // per-device password brute-force throttle
	agentStoreOnce      sync.Once
	agentStoreMu        sync.RWMutex
	agentStoreInst      *agentSessionStore
	agentFileMutations  agentFileMutations
	agentSessionsMu     sync.Mutex
	agentSessions       map[string]*agentSessionEntry // per-device agent state, owned by the server
	agentEvictOnce      sync.Once
	agentWG             sync.WaitGroup // server-owned agent goroutines (turns/probes)

	// Public config (set by main) for API/UI
	SSHPort          string // e.g. "2222"
	HTTPHost         string // e.g. "192.168.1.100:8080"
	PublicServer     string // explicit client endpoint list; avoids deriving from HTTP Host
	TunnelBase       string // optional ws(s):// base advertised for tcp-tunnel dial-backs
	TCPPort          string
	KCPPort          string
	AdminToken       string // optional token for web APIs and browser WebSockets
	VNCAddr          string // optional VNC/RFB listen address
	ProxyAddr        string // optional HTTP/SOCKS5 proxy listen address
	MaxSessions      int    // maximum concurrent sessions per device
	MaxForwards      int    // maximum concurrent forwards per device
	BatchConcurrency int    // maximum concurrent batch operations
	ReleaseVersion   string // server release version, injected by main

	// LLM agent (browser /agent endpoint). Agent is the multi-provider model
	// configuration; nil or no provider with an API key disables the agent.
	// The AgentBaseURL/AgentAPIKey/AgentModel fields are the single-model
	// flag values main uses to synthesize Agent.
	Agent         *AgentConfig
	AgentBaseURL  string // OpenAI-compatible endpoint root
	AgentAPIKey   string
	AgentModel    string
	AgentMaxTurns int

	// Agent session persistence: JSONL history per device under
	// DataDir/agent/sessions. AgentSessionTTL <= 0 disables persistence and
	// the TTL cleanup entirely.
	DataDir         string
	AgentSessionTTL time.Duration
	identity        *identityRegistry
	identityOnce    sync.Once

	// ReleaseDir, when set, makes release downloads fully local: assets are
	// served from <ReleaseDir>/<tag>/<asset> and no GitHub/mirror URL is ever
	// contacted or handed out. Empty keeps the GitHub proxy/redirect flow
	// (with the DataDir disk cache in front of it).
	ReleaseDir string
}

// NewServer creates a new Server
func NewServer() *Server {
	s := &Server{
		health:            serverHealth{startedAt: time.Now()},
		clients:           make(map[string]*ClientConn),
		identity:          &identityRegistry{records: make(map[string]identityRecord)},
		sessions:          make(map[string]*ProxySession),
		forwards:          make(map[string]*ProxyForward),
		revForwards:       make(map[string]*ReverseForward),
		fileResults:       make(map[string]chan *protocol.Message),
		fileRequests:      make(map[string]*fileRequestRoute),
		fileTasks:         make(map[string]*fileTaskRoute),
		agentFileReqs:     make(map[string]chan *protocol.Message),
		agentFileTasks:    make(map[string]*agentFileTask),
		desktops:          make(map[string]*desktopRoute),
		vncSettings:       make(map[string]protocol.Message),
		vncStreams:        make(map[string]*vncDesktopStream),
		gpuDesktopTunnels: make(map[string]*gpuDesktopTunnel),
		proxyTunnels:      make(map[string]*proxyTunnelPending),
		MaxSessions:       256,
		WebRTCICEServers:  []protocol.IceServer{{URLs: []string{"stun:stun.l.google.com:19302"}}},
		MaxForwards:       1024,
		BatchConcurrency:  runtime.GOMAXPROCS(0) * 8,
		authLimiter:       newAuthRateLimiter(),
	}
	s.upgrader = gws.NewUpgrader(&wsHandler{srv: s}, &gws.ServerOption{
		ReadMaxPayloadSize: 16 * 1024 * 1024,
		ParallelGolimit:    runtime.GOMAXPROCS(0),
		PermessageDeflate: gws.PermessageDeflate{
			Enabled:               true,
			ServerContextTakeover: true,
			ClientContextTakeover: true,
			// gws defaults to 12 with takeover, but OkHttp cannot negotiate
			// client_max_window_bits. 15 accepts its standard 32 KiB window.
			ClientMaxWindowBits: 15,
			Threshold:           256,
		},
	})
	return s
}

func (h *wsHandler) handleRegister(socket *gws.Conn, msg *protocol.Message) {
	if id, _ := socket.Session().Load("clientID"); id != nil {
		_ = socket.WriteClose(1008, []byte("connection already registered"))
		return
	}
	if msg.ClientID == "" {
		socket.WriteClose(1000, nil)
		return
	}

	clientID := strings.TrimSpace(msg.ClientID)
	if !validClientID(clientID) {
		// Security: reject unsafe/oversized IDs instead of letting them flow
		// into SSH usernames, URLs and logs.
		log.Printf("rejected registration with invalid client ID %q", clientID)
		socket.WriteClose(1000, nil)
		return
	}
	identity := strings.TrimSpace(msg.DeviceIdentity)
	assignedID, proof, _, authErr := h.srv.authenticateRegistration(identity, clientID, msg.ReconnectProof)
	if authErr != nil {
		log.Printf("rejected registration identity=%q: %v", identity, authErr)
		socket.WriteClose(1008, []byte("identity authentication failed"))
		return
	}
	clientID = assignedID
	instanceID := strings.TrimSpace(msg.InstanceID)
	client := &ClientConn{
		ID:                clientID,
		DisplayName:       strings.TrimSpace(msg.DisplayName),
		RequestedID:       clientID,
		InstanceID:        instanceID,
		DeviceIdentity:    identity,
		ReconnectProof:    msg.ReconnectProof,
		Version:           msg.ClientVersion,
		OS:                msg.ClientOS,
		Conn:              socket,
		Transport:         &wsDeviceTransport{conn: socket},
		ConnectedAt:       time.Now(),
		Password:          msg.Password,
		Desktop:           cloneDesktopCapabilities(msg.DesktopCapabilities),
		FileOperations:    append([]string(nil), msg.FileOperations...),
		FileRTC:           msg.FileRTC,
		TerminalRTC:       msg.TerminalRTC,
		ConfinedDownloads: msg.ConfinedDownloads,
		Sessions:          make(map[string]*ProxySession),
		Forwards:          make(map[string]*ProxyForward),
	}

	old, assignedID, duplicate := h.srv.registerClient(client)
	if assignedID == "" {
		h.srv.failRegistration(client)
		return
	}
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
		Type:           protocol.MsgRegister,
		ClientID:       assignedID,
		InstanceID:     instanceID,
		DeviceIdentity: identity,
		ReconnectProof: proof,
		SSHPort:        h.srv.SSHPort,
		HTTPHost:       h.srv.HTTPHost,
	})
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
	reg := s.identityRegistry()
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if err := s.loadIdentityLocked(reg); err != nil {
		return nil, "", true
	}
	record, ok := reg.records[client.DeviceIdentity]
	if !ok || record.Revoked || record.ClientID == "" || subtle.ConstantTimeCompare([]byte(record.ProofHash), []byte(hashIdentityProof(client.ReconnectProof))) != 1 {
		return nil, "", true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	requestedID := record.ClientID
	client.RequestedID = requestedID
	if old := s.clients[requestedID]; old == nil {
		client.ID = requestedID
		s.clients[requestedID] = client
		s.notifyDeviceWatchersLocked()
		return nil, requestedID, false
	} else if old.DeviceIdentity == client.DeviceIdentity {
		client.ID = requestedID
		s.clients[requestedID] = client
		s.notifyDeviceWatchersLocked()
		return old, requestedID, false
	}
	return nil, "", true
}

func (s *Server) failRegistration(client *ClientConn) {
	if client != nil && client.Transport != nil {
		_ = client.Transport.Close("identity registration rejected")
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
	s.notifyDeviceWatchersLocked()
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
	s.notifyDeviceWatchersLocked()
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

func (s *Server) handleClientBinary(client *ClientConn, raw []byte) {
	typ, id, payload, err := protocol.DecodeBinFrame(raw)
	if err != nil {
		return
	}
	if route := s.getFileTask(id); route != nil && (route.client != client || route.rtc) {
		return
	}
	if s.handleFileManagerBinary(raw) {
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
		if sess != nil && (sess.terminal == nil || sess.terminal.client == client) {
			sess.SetExitCode(msg.ExitCode)
		}

	case protocol.MsgClose:
		sess := s.getSession(msg.SessionID)
		if sess != nil && (sess.terminal == nil || sess.terminal.client == client) {
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
		"file_create_result", "file_stat_result", "file_mkdir_result", "file_delete_result", "file_rename_result", "file_copy_result":
		if route := s.getFileTask(msg.TaskID); route != nil && route.client != client {
			return
		}
		s.handleFileManagerMessage(msg, client.ID)
	case protocol.MsgFileRTCSignal:
		s.handleFileRTCSignal(client, msg)
	case protocol.MsgDesktopReady, protocol.MsgDesktopClose, protocol.MsgDesktopClipboard, protocol.MsgDesktopCuaResult:
		s.handleDesktopMessage(msg)
	case protocol.MsgDesktopRTCSignal:
		s.handleDesktopRTCSignal(client, msg)
	case protocol.MsgTerminalRTCSignal:
		s.handleTerminalRTCSignal(client, msg)
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
	// Constant-time compare: do not leak token prefix via timing.
	return constantTimeEqual(token, s.AdminToken)
}

func (s *Server) requireAuth(w http.ResponseWriter, r *http.Request) bool {
	if s.authOK(r) {
		return true
	}
	http.Error(w, "unauthorized", http.StatusUnauthorized)
	return false
}

// deviceAuthBypassed reports whether the request carries a valid admin token;
// admins skip per-device password checks in the browser endpoints.
func (s *Server) deviceAuthBypassed(r *http.Request) bool {
	return s.AdminToken != "" && s.authOK(r)
}

// RequireAuth wraps h with admin-token authentication. Used by main to protect
// routes whose handlers cannot call requireAuth themselves.
func (s *Server) RequireAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.requireAuth(w, r) {
			return
		}
		h(w, r)
	}
}

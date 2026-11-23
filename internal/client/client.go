package client

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lxzan/gws"
	gossh "golang.org/x/crypto/ssh"
	"rdev/internal/protocol"
)

const (
	defaultReconnectMin = 1 * time.Second
	defaultReconnectMax = 30 * time.Second
	clientWriteWait     = 10 * time.Second
	clientReadWait      = 75 * time.Second
	clientPingPeriod    = 25 * time.Second
)

// convertModes converts protocol modes (map[uint8]uint32) to ssh.TerminalModes.
// Returns nil if the map is empty.
func convertModes(m map[uint8]uint32) gossh.TerminalModes {
	if len(m) == 0 {
		return nil
	}
	modes := make(gossh.TerminalModes, len(m))
	for k, v := range m {
		modes[k] = v
	}
	return modes
}

// --- Write adapters ---

// coalescingWriter batches small writes into larger WebSocket frames.
// Sends immediately for >= 4KB, otherwise buffers up to 5ms.
type coalescingWriter struct {
	client    *Client
	sessionID string
	typ       byte // BinData or BinStderr
	buf       bytes.Buffer
	timer     *time.Timer
	interval  time.Duration
	mu        sync.Mutex
}

func newCoalescingWriter(client *Client, sessionID string, typ byte) *coalescingWriter {
	return newCoalescingWriterWithInterval(client, sessionID, typ, 5*time.Millisecond)
}

func newCoalescingWriterWithInterval(client *Client, sessionID string, typ byte, interval time.Duration) *coalescingWriter {
	return &coalescingWriter{
		client:    client,
		sessionID: sessionID,
		typ:       typ,
		buf:       *bytes.NewBuffer(make([]byte, 0, 8192)),
		interval:  interval,
	}
}

func (w *coalescingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(p) >= 4096 {
		w.flushLocked()
		w.client.sendBinary(w.typ, w.sessionID, p)
		return len(p), nil
	}
	w.buf.Write(p)
	if w.buf.Len() >= 4096 {
		w.flushLocked()
	} else if w.timer == nil {
		w.timer = time.AfterFunc(w.interval, w.flush)
	}
	return len(p), nil
}

func (w *coalescingWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.flushLocked()
}

// Serialize draining with writes so a timer cannot send older bytes after a
// newer chunk or after the final flush. sendBinary copies into its frame before
// returning, so the buffer can be reused without another allocation here.
func (w *coalescingWriter) flushLocked() {
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	if w.buf.Len() == 0 {
		return
	}
	w.client.sendBinary(w.typ, w.sessionID, w.buf.Bytes())
	w.buf.Reset()
}

// Client is the rdev client that connects to the server.
type Client struct {
	tcpDials map[string]*pendingTCPDial // guarded by mu; pending outbound connects

	serverURL            string
	clientID             string
	displayName          string
	requestedID          string
	instanceID           string
	deviceIdentity       string
	reconnectProof       string
	identityErr          error
	password             string
	shell                string
	version              string
	conn                 *gws.Conn
	transport            clientTransport
	writeMu              sync.Mutex
	sessions             map[string]*clientSession
	forwards             map[string]net.Conn
	forwardOpen          map[string]chan struct{}
	listeners            map[string]net.Listener
	fileStreams          map[string]*fileStream
	fileListings         fileListingCache
	uploadMu             sync.Mutex // upload maps; never held during file I/O
	uploadPaths          map[string]*managedUpload
	uploads              map[string]*managedUpload
	downloads            map[string]chan struct{}
	downloadFingerprints downloadFingerprintCache
	fileRTC              map[string]*fileRTCSession // guarded by mu
	desktopSessions      map[string]*desktopSession
	mu                   sync.Mutex
	done                 chan struct{}
	reconnectReset       chan struct{}
	reconnectMin         time.Duration
	reconnectMax         time.Duration

	// Server info (received on register response)
	sshPort  string
	httpHost string
	// tunnelBase is the server-advertised ws(s):// base for tcp-tunnel
	// dial-backs (register response), preferred over deriving from httpHost.
	tunnelBase string
	// activeWSBase is the ws(s)://host base of the current control connection,
	// empty when connected via tcp/kcp. Used to build tunnel dial-back URLs.
	activeWSBase string

	// OnConnect is called after successfully connecting and registering.
	OnConnect func(c *Client)
}

// NewClient creates a new client
func NewClient(serverURL, clientID, password, shell string) *Client {
	savedIdentity, _, identityErr := loadClientIdentity(serverURL, clientID)
	displayName := strings.TrimSpace(os.Getenv("RDEV_DISPLAY_NAME"))
	if displayName == "" {
		displayName, _ = os.Hostname()
	}
	c := &Client{
		serverURL:       serverURL,
		clientID:        clientID,
		displayName:     displayName,
		requestedID:     clientID,
		instanceID:      newClientInstanceID(),
		deviceIdentity:  savedIdentity.DeviceIdentity,
		reconnectProof:  savedIdentity.ReconnectProof,
		identityErr:     identityErr,
		password:        password,
		shell:           shell,
		sessions:        make(map[string]*clientSession),
		forwards:        make(map[string]net.Conn),
		forwardOpen:     make(map[string]chan struct{}),
		listeners:       make(map[string]net.Listener),
		fileStreams:     make(map[string]*fileStream),
		uploads:         make(map[string]*managedUpload),
		uploadPaths:     make(map[string]*managedUpload),
		downloads:       make(map[string]chan struct{}),
		desktopSessions: make(map[string]*desktopSession),
		done:            make(chan struct{}, 1),
		reconnectReset:  make(chan struct{}, 1),
		reconnectMin:    defaultReconnectMin,
		reconnectMax:    defaultReconnectMax,
	}
	return c
}

// SetReconnectDelays configures the reconnect backoff bounds.
func (c *Client) SetReconnectDelays(minDelay, maxDelay time.Duration) {
	if minDelay <= 0 {
		minDelay = defaultReconnectMin
	}
	if maxDelay < minDelay {
		maxDelay = minDelay
	}
	c.reconnectMin = minDelay
	c.reconnectMax = maxDelay
}

// SetVersion sets the version string reported during registration.
func (c *Client) SetVersion(version string) {
	c.version = version
}

// SetDisplayName sets presentation metadata before Run; it never changes identity.
func (c *Client) SetDisplayName(name string) {
	if name = strings.TrimSpace(name); name != "" {
		c.displayName = name
	}
}

func (c *Client) registrationMessage() *protocol.Message {
	return &protocol.Message{
		Type:                protocol.MsgRegister,
		ClientID:            c.requestedID,
		DisplayName:         c.displayName,
		InstanceID:          c.instanceID,
		DeviceIdentity:      c.deviceIdentity,
		ReconnectProof:      c.reconnectProof,
		ClientVersion:       c.version,
		ClientOS:            runtime.GOOS,
		Password:            c.password,
		DesktopCapabilities: desktopCapabilities(),
		FileOperations:      []string{"create", "mkdir", "delete", "rename", "rename_directory"},
		FileRTC:             true,
		ConfinedDownloads:   true,
	}
}

func newClientInstanceID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid())
}

func (c *Client) sendFileTransferError(taskID, path, errText string) {
	c.send(&protocol.Message{Type: protocol.MsgFileTransferError, TaskID: taskID, Path: path, Error: errText})
	if rtc := c.fileRTCFor(taskID); rtc != nil {
		rtc.close(true)
	}
}

func defaultFilePath(path string) string {
	if path != "" {
		return path
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	if cwd, err := os.Getwd(); err == nil && cwd != "" {
		return cwd
	}
	return "."
}

func fileParent(path string) string {
	clean := filepath.Clean(defaultFilePath(path))
	parent := filepath.Dir(clean)
	if parent == "." && filepath.IsAbs(clean) {
		return clean
	}
	return parent
}

func (c *Client) handleFileMkdir(msg *protocol.Message) {
	response := createFileDirectory(msg)
	c.fileListings.invalidate()
	c.send(response)
}

func createFileDirectory(msg *protocol.Message) *protocol.Message {
	response := &protocol.Message{Type: protocol.MsgFileMkdirResult, RequestID: msg.RequestID, Path: msg.Path}
	if strings.TrimSpace(msg.Path) == "" {
		response.Error = "missing directory path"
		return response
	}
	if err := os.MkdirAll(msg.Path, 0755); err != nil {
		response.Error = err.Error()
	} else {
		response.Success = true
	}
	return response
}

func (c *Client) handleFileListRequest(msg *protocol.Message) {
	path := defaultFilePath(msg.Path)
	limit := msg.Size
	if limit <= 0 {
		limit = 200
	}
	if limit > 2000 {
		limit = 2000
	}
	offset := max(msg.Offset, 0)
	entries, total, err := c.fileListings.list(path, offset, int(limit))
	if err != nil {
		c.send(&protocol.Message{Type: protocol.MsgFileListResult, RequestID: msg.RequestID, Path: path, Error: err.Error()})
		return
	}
	home, _ := os.UserHomeDir()
	c.send(&protocol.Message{
		Type:        protocol.MsgFileListResult,
		RequestID:   msg.RequestID,
		Path:        filepath.Clean(path),
		ParentPath:  fileParent(path),
		HomePath:    home,
		FileEntries: entries,
		Truncated:   offset+int64(len(entries)) < int64(total),
		Offset:      offset,
		Size:        limit,
		FileMode:    int32(total),
	})
}

func listFileEntries(path string, offset int64, limit int) ([]protocol.FileEntry, int, error) {
	infos, err := os.ReadDir(path)
	if err != nil {
		return nil, 0, err
	}
	sort.Slice(infos, func(i, j int) bool {
		if infos[i].IsDir() != infos[j].IsDir() {
			return infos[i].IsDir()
		}
		a, b := strings.ToLower(infos[i].Name()), strings.ToLower(infos[j].Name())
		if a == b {
			return infos[i].Name() < infos[j].Name()
		}
		return a < b
	})
	entries := make([]protocol.FileEntry, 0, len(infos))
	for _, ent := range infos {
		info, err := ent.Info()
		if err != nil {
			continue
		}
		entries = append(entries, protocol.FileEntry{
			Name:    ent.Name(),
			Path:    filepath.Join(path, ent.Name()),
			IsDir:   ent.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime().Format(time.RFC3339),
		})
	}
	total := len(entries)
	start := int(min(max(offset, 0), int64(total)))
	end := total
	if limit > 0 && limit < total-start {
		end = start + limit
	}
	return entries[start:end], total, nil
}

func safeJoinFile(dir, name string) string {
	if name == "" {
		return filepath.Clean(dir)
	}
	if filepath.IsAbs(name) {
		return filepath.Clean(name)
	}
	return filepath.Join(defaultFilePath(dir), filepath.Base(name))
}

func (c *Client) handleManagedTransferCancel(taskID string) {
	if taskID == "" {
		return
	}
	if rtc := c.fileRTCFor(taskID); rtc != nil {
		rtc.close(false)
		rtc.startMu.Lock()
		c.cancelFileOperations(taskID)
		rtc.startMu.Unlock()
		return
	}
	c.cancelFileOperations(taskID)
}

func (c *Client) cancelFileOperations(taskID string) {
	if up := c.managedUploadFor(taskID); up != nil {
		c.cancelManagedUpload(up)
	}
	c.mu.Lock()
	if cancel := c.downloads[taskID]; cancel != nil {
		close(cancel)
		delete(c.downloads, taskID)
	}
	c.mu.Unlock()
}

// --- Cleanup ---

func (c *Client) cleanup() {
	c.mu.Lock()
	conn := c.conn
	uploads, desktops := c.teardownState()
	c.mu.Unlock()
	c.finishTeardown(uploads, desktops)
	if conn != nil {
		conn.WriteClose(1000, nil)
	}
	close(c.done)
}

// SSHPort returns the server's SSH port (received on register).
func (c *Client) SSHPort() string { return c.sshPort }

// ClientID returns the server-assigned client ID.
func (c *Client) ClientID() string { return c.clientID }

// HTTPHost returns the server's HTTP host:port (received on register).
func (c *Client) HTTPHost() string { return c.httpHost }

package client

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lxzan/gws"
	"github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"
	"rdev/internal/protocol"
	"rdev/internal/ptyutil"
	"rdev/internal/wincompat"
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
	if len(p) >= 4096 {
		// Large write: send immediately
		w.flush() // drain any pending buffer
		w.client.sendBinary(w.typ, w.sessionID, p)
		return len(p), nil
	}

	w.mu.Lock()
	w.buf.Write(p)
	if w.buf.Len() >= 4096 {
		data := make([]byte, w.buf.Len())
		copy(data, w.buf.Bytes())
		w.buf.Reset()
		if w.timer != nil {
			w.timer.Stop()
			w.timer = nil
		}
		w.mu.Unlock()
		w.client.sendBinary(w.typ, w.sessionID, data)
	} else if w.timer == nil {
		w.timer = time.AfterFunc(w.interval, w.flush)
		w.mu.Unlock()
	} else {
		w.mu.Unlock()
	}
	return len(p), nil
}

func (w *coalescingWriter) flush() {
	w.mu.Lock()
	if w.buf.Len() == 0 {
		w.timer = nil
		w.mu.Unlock()
		return
	}
	data := make([]byte, w.buf.Len())
	copy(data, w.buf.Bytes())
	w.buf.Reset()
	w.timer = nil
	w.mu.Unlock()
	w.client.sendBinary(w.typ, w.sessionID, data)
}

// sftpRWC bridges SFTP server over WebSocket
type sftpRWC struct {
	reader io.Reader
	writer io.Writer
	closer io.Closer
}

func (s *sftpRWC) Read(p []byte) (int, error)  { return s.reader.Read(p) }
func (s *sftpRWC) Write(p []byte) (int, error) { return s.writer.Write(p) }
func (s *sftpRWC) Close() error {
	s.closer.Close()
	return nil
}

// clientSession represents a proxied session on the client device
type clientSession struct {
	id        string
	subsystem string // "", "sftp"
	command   string
	pty       bool

	// PTY mode
	ptyProc *ptyutil.Process

	// Non-PTY exec mode
	stdinPipe io.WriteCloser
	cmdWaitFn func() (int, error)

	// SFTP mode
	sftpInput  *io.PipeWriter
	sftpOutput *io.PipeReader

	done chan struct{}
	once sync.Once
}

func (s *clientSession) close() {
	s.once.Do(func() {
		if s.ptyProc != nil {
			s.ptyProc.Close()
		}
		if s.stdinPipe != nil {
			s.stdinPipe.Close()
		}
		if s.sftpInput != nil {
			s.sftpInput.Close()
		}
		close(s.done)
	})
}

type fileStream struct {
	path string
	file *os.File
	mode os.FileMode
}

type managedUpload struct {
	path      string
	partPath  string
	file      *os.File
	size      int64
	offset    int64
	startedAt time.Time
}

// Client is the rdev client that connects to the server
type Client struct {
	serverURL       string
	clientID        string
	password        string
	shell           string
	conn            *gws.Conn
	writeMu         sync.Mutex
	sessions        map[string]*clientSession
	forwards        map[string]net.Conn
	forwardOpen     map[string]chan struct{}
	listeners       map[string]net.Listener
	fileStreams     map[string]*fileStream
	uploads         map[string]*managedUpload
	downloads       map[string]chan struct{}
	desktopSessions map[string]*desktopSession
	mu              sync.Mutex
	done            chan struct{}

	// Server info (received on register response)
	sshPort  string
	httpHost string

	// OnConnect is called after successfully connecting and registering.
	OnConnect func(c *Client)
}

// NewClient creates a new client
func NewClient(serverURL, clientID, password, shell string) *Client {
	return &Client{
		serverURL:       serverURL,
		clientID:        clientID,
		password:        password,
		shell:           shell,
		sessions:        make(map[string]*clientSession),
		forwards:        make(map[string]net.Conn),
		forwardOpen:     make(map[string]chan struct{}),
		listeners:       make(map[string]net.Listener),
		fileStreams:     make(map[string]*fileStream),
		uploads:         make(map[string]*managedUpload),
		downloads:       make(map[string]chan struct{}),
		desktopSessions: make(map[string]*desktopSession),
		done:            make(chan struct{}, 1),
	}
}

// wsEventHandler implements gws.Event for the client
type wsEventHandler struct {
	gws.BuiltinEventHandler
	client *Client
}

func (h *wsEventHandler) OnOpen(socket *gws.Conn) {
	h.client.conn = socket
	if err := h.client.send(&protocol.Message{
		Type:                protocol.MsgRegister,
		ClientID:            h.client.clientID,
		Password:            h.client.password,
		DesktopCapabilities: desktopCapabilities(),
	}); err != nil {
		log.Printf("register send error: %v", err)
		return
	}
	log.Printf("connected to %s as '%s'", h.client.serverURL, h.client.clientID)
	// OnConnect will be called after receiving the register response
	// (in handleMessage when MsgRegister response arrives with sshPort)
}

func (h *wsEventHandler) OnClose(socket *gws.Conn, err error) {
	log.Printf("connection closed: %v", err)
	h.client.mu.Lock()
	for sid, sess := range h.client.sessions {
		sess.close()
		delete(h.client.sessions, sid)
	}
	for fid, tcpConn := range h.client.forwards {
		tcpConn.Close()
		delete(h.client.forwards, fid)
	}
	for tid, upload := range h.client.uploads {
		upload.file.Close()
		delete(h.client.uploads, tid)
	}
	for tid, cancel := range h.client.downloads {
		close(cancel)
		delete(h.client.downloads, tid)
	}
	for sid, session := range h.client.desktopSessions {
		close(session.stop)
		delete(h.client.desktopSessions, sid)
	}
	h.client.conn = nil
	h.client.mu.Unlock()
	select {
	case h.client.done <- struct{}{}:
	default:
	}
}

func (h *wsEventHandler) OnMessage(socket *gws.Conn, message *gws.Message) {
	defer message.Close()

	if message.Opcode == gws.OpcodeBinary {
		h.handleBinaryMessage(message.Bytes())
		return
	}

	// Text frame = JSON control message
	msg, err := protocol.Decode(message.Bytes())
	if err != nil {
		return
	}
	h.client.handleMessage(msg)
}

func (h *wsEventHandler) handleBinaryMessage(raw []byte) {
	typ, id, payload, err := protocol.DecodeBinFrame(raw)
	if err != nil {
		return
	}

	switch typ {
	case protocol.BinData:
		h.client.handleBinData(id, payload)
	case protocol.BinStderr:
		// stderr not expected from server in current design
	case protocol.BinTCPData:
		h.client.handleBinTCPData(id, payload)
	case protocol.BinFilePut:
		h.client.handleBinFilePut(id, payload)
	case protocol.BinFileStart:
		h.client.handleBinFileStart(id, payload)
	case protocol.BinFileChunk:
		h.client.handleBinFileChunk(id, payload)
	case protocol.BinFileEnd:
		h.client.handleBinFileEnd(id)
	case protocol.BinFileUploadChunk:
		_, taskID, offset, data, err := protocol.DecodeBinFrameOffset(raw)
		if err == nil {
			h.client.handleManagedUploadChunk(taskID, offset, data)
		}
	case protocol.BinFileTransferCancel:
		h.client.handleManagedTransferCancel(id)
	}
}

// Run starts the client with auto-reconnect
func (c *Client) Run() error {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	for {
		err := c.connect()
		if err != nil {
			log.Printf("connection error: %v, reconnecting in 3s...", err)
			select {
			case <-time.After(3 * time.Second):
				continue
			case <-sigCh:
				return nil
			}
		}

		select {
		case <-c.done:
			log.Printf("disconnected, reconnecting in 3s...")
			select {
			case <-time.After(3 * time.Second):
				continue
			case <-sigCh:
				return nil
			}
		case <-sigCh:
			c.cleanup()
			return nil
		}
	}
}

func (c *Client) connect() error {
	wsURL := c.serverURL

	// Normalize scheme: ensure exactly "ws://" or "wss://" (no triple slash)
	if strings.HasPrefix(wsURL, "wss:///") {
		wsURL = "wss://" + strings.TrimLeft(wsURL[len("wss://"):], "/")
	} else if strings.HasPrefix(wsURL, "ws:///") {
		wsURL = "ws://" + strings.TrimLeft(wsURL[len("ws://"):], "/")
	} else if !strings.HasPrefix(wsURL, "ws://") && !strings.HasPrefix(wsURL, "wss://") {
		wsURL = "ws://" + wsURL
	}

	if !strings.HasSuffix(wsURL, "/ws") {
		wsURL += "/ws"
	}

	handler := &wsEventHandler{client: c}
	socket, _, err := gws.NewClient(handler, &gws.ClientOption{
		Addr:               wsURL,
		HandshakeTimeout:   10 * time.Second,
		ReadMaxPayloadSize: 16 * 1024 * 1024,
		NewDialer:          websocketDialerFor(wsURL),
		PermessageDeflate: gws.PermessageDeflate{
			Enabled:               true,
			ServerContextTakeover: true,
			ClientContextTakeover: true,
			Threshold:             256,
		},
	})
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}

	go socket.ReadLoop()
	return nil
}

func (c *Client) handleMessage(msg *protocol.Message) {
	switch msg.Type {
	case protocol.MsgRegister:
		if msg.ClientID != "" {
			c.clientID = msg.ClientID
		}
		c.sshPort = msg.SSHPort
		c.httpHost = msg.HTTPHost
		if c.OnConnect != nil {
			c.OnConnect(c)
		}
	case protocol.MsgNewSession:
		c.handleNewSession(msg)
	case protocol.MsgStdinClose:
		c.handleStdinClose(msg)
	case protocol.MsgResize:
		c.handleResize(msg)
	case protocol.MsgClose:
		c.handleClose(msg)

	// TCP forwarding control
	case protocol.MsgTCPConnect:
		c.handleTCPConnect(msg)
	case protocol.MsgTCPOpen:
		c.handleTCPOpen(msg)
	case protocol.MsgTCPListen:
		c.handleTCPListen(msg)
	case protocol.MsgTCPClose:
		c.handleTCPClose(msg)
	case protocol.MsgFileListRequest:
		go c.handleFileListRequest(msg)
	case protocol.MsgFileUploadStart:
		go c.handleManagedUploadStart(msg)
	case protocol.MsgFileUploadEnd:
		go c.handleManagedUploadEnd(msg)
	case protocol.MsgFileDownloadStart:
		go c.handleManagedDownloadStart(msg)
	case protocol.MsgFileTransferCancel:
		c.handleManagedTransferCancel(msg.TaskID)
	case protocol.MsgDesktopStart:
		go c.handleDesktopStart(msg)
	case protocol.MsgDesktopInput:
		c.handleDesktopInput(msg)
	case protocol.MsgDesktopClose:
		c.handleDesktopClose(msg.SessionID)
	}
}

func (c *Client) sendBinaryOffset(typ byte, id string, offset int64, data []byte) error {
	frame := protocol.EncodeBinFrameOffset(typ, id, offset, data)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.conn == nil {
		return fmt.Errorf("not connected")
	}
	return c.conn.WriteMessage(gws.OpcodeBinary, frame)
}

func (c *Client) sendFileTransferError(taskID, path, errText string) {
	c.send(&protocol.Message{Type: protocol.MsgFileTransferError, TaskID: taskID, Path: path, Error: errText})
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

func (c *Client) handleFileListRequest(msg *protocol.Message) {
	path := defaultFilePath(msg.Path)
	entries, truncated, err := listFileEntries(path, 2000)
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
		Truncated:   truncated,
	})
}

func listFileEntries(path string, limit int) ([]protocol.FileEntry, bool, error) {
	infos, err := os.ReadDir(path)
	if err != nil {
		return nil, false, err
	}
	sort.Slice(infos, func(i, j int) bool {
		if infos[i].IsDir() != infos[j].IsDir() {
			return infos[i].IsDir()
		}
		return strings.ToLower(infos[i].Name()) < strings.ToLower(infos[j].Name())
	})
	truncated := false
	if limit > 0 && len(infos) > limit {
		infos = infos[:limit]
		truncated = true
	}
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
	return entries, truncated, nil
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

func (c *Client) handleManagedUploadFromURL(msg *protocol.Message) {
	taskID := msg.TaskID
	target := msg.Path
	if target == "" {
		target = safeJoinFile(msg.ParentPath, msg.Name)
	}
	if taskID == "" || target == "" {
		return
	}
	partPath := target + ".rdevpart"
	if err := os.MkdirAll(filepath.Dir(partPath), 0755); err != nil {
		c.sendFileTransferError(taskID, target, fmt.Sprintf("mkdir error: %v", err))
		return
	}
	req, err := http.NewRequest(http.MethodGet, msg.DownloadURL, nil)
	if err != nil {
		c.sendFileTransferError(taskID, target, err.Error())
		return
	}
	req.Header.Set("user-agent", "Mozilla/5.0 RDev")
	req.Header.Set("referer", "https://www.aliyundrive.com/")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.sendFileTransferError(taskID, target, err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		c.sendFileTransferError(taskID, target, fmt.Sprintf("download url failed: %s", resp.Status))
		return
	}
	file, err := os.OpenFile(partPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		c.sendFileTransferError(taskID, target, err.Error())
		return
	}
	written, copyErr := io.Copy(file, resp.Body)
	closeErr := file.Close()
	if copyErr != nil {
		c.sendFileTransferError(taskID, target, copyErr.Error())
		return
	}
	if closeErr != nil {
		c.sendFileTransferError(taskID, target, closeErr.Error())
		return
	}
	if msg.Size >= 0 && written != msg.Size {
		c.sendFileTransferError(taskID, target, fmt.Sprintf("size mismatch: wrote %d of %d", written, msg.Size))
		return
	}
	if err := os.Rename(partPath, target); err != nil {
		c.sendFileTransferError(taskID, target, err.Error())
		return
	}
	c.send(&protocol.Message{Type: protocol.MsgFileTransferEnd, TaskID: taskID, Path: target, Size: written, Success: true})
}

func (c *Client) handleManagedUploadStart(msg *protocol.Message) {
	taskID := msg.TaskID
	if taskID == "" {
		return
	}
	if msg.DownloadURL != "" {
		go c.handleManagedUploadFromURL(msg)
		return
	}
	target := msg.Path
	if target == "" {
		target = safeJoinFile(msg.ParentPath, msg.Name)
	}
	if target == "" {
		c.sendFileTransferError(taskID, "", "missing target path")
		return
	}
	partPath := target + ".rdevpart"
	if err := os.MkdirAll(filepath.Dir(partPath), 0755); err != nil {
		c.sendFileTransferError(taskID, target, fmt.Sprintf("mkdir error: %v", err))
		return
	}
	f, err := os.OpenFile(partPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		c.sendFileTransferError(taskID, target, err.Error())
		return
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		c.sendFileTransferError(taskID, target, err.Error())
		return
	}
	offset := st.Size()
	if msg.Size >= 0 && offset > msg.Size {
		if err := f.Truncate(0); err != nil {
			f.Close()
			c.sendFileTransferError(taskID, target, err.Error())
			return
		}
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		f.Close()
		c.sendFileTransferError(taskID, target, err.Error())
		return
	}
	c.mu.Lock()
	if old := c.uploads[taskID]; old != nil {
		old.file.Close()
	}
	c.uploads[taskID] = &managedUpload{path: target, partPath: partPath, file: f, size: msg.Size, offset: offset, startedAt: time.Now()}
	c.mu.Unlock()
	c.send(&protocol.Message{Type: protocol.MsgFileUploadReady, TaskID: taskID, Path: target, Offset: offset, Size: msg.Size})
}

func (c *Client) handleManagedUploadChunk(taskID string, offset int64, data []byte) {
	c.mu.Lock()
	up := c.uploads[taskID]
	c.mu.Unlock()
	if up == nil {
		c.sendFileTransferError(taskID, "", "upload not found")
		return
	}
	if offset != up.offset {
		c.sendFileTransferError(taskID, up.path, fmt.Sprintf("unexpected offset %d, want %d", offset, up.offset))
		return
	}
	n, err := up.file.Write(data)
	if err != nil {
		c.sendFileTransferError(taskID, up.path, err.Error())
		return
	}
	up.offset += int64(n)
	c.sendBinaryOffset(protocol.BinFileUploadAck, taskID, up.offset, nil)
}

func (c *Client) handleManagedUploadEnd(msg *protocol.Message) {
	taskID := msg.TaskID
	c.mu.Lock()
	up := c.uploads[taskID]
	delete(c.uploads, taskID)
	c.mu.Unlock()
	if up == nil {
		c.sendFileTransferError(taskID, msg.Path, "upload not found")
		return
	}
	if err := up.file.Sync(); err != nil {
		up.file.Close()
		c.sendFileTransferError(taskID, up.path, err.Error())
		return
	}
	if err := up.file.Close(); err != nil {
		c.sendFileTransferError(taskID, up.path, err.Error())
		return
	}
	if up.size >= 0 && up.offset != up.size {
		c.sendFileTransferError(taskID, up.path, fmt.Sprintf("size mismatch: wrote %d of %d", up.offset, up.size))
		return
	}
	if err := os.Rename(up.partPath, up.path); err != nil {
		c.sendFileTransferError(taskID, up.path, err.Error())
		return
	}
	c.send(&protocol.Message{Type: protocol.MsgFileTransferEnd, TaskID: taskID, Path: up.path, Size: up.offset, Success: true})
}

func (c *Client) handleManagedDownloadStart(msg *protocol.Message) {
	taskID := msg.TaskID
	path := msg.Path
	if taskID == "" || path == "" {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		c.sendFileTransferError(taskID, path, err.Error())
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		c.sendFileTransferError(taskID, path, err.Error())
		return
	}
	if st.IsDir() {
		c.sendFileTransferError(taskID, path, "cannot download directory")
		return
	}
	offset := msg.Offset
	if offset < 0 || offset > st.Size() {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		c.sendFileTransferError(taskID, path, err.Error())
		return
	}
	cancel := make(chan struct{})
	c.mu.Lock()
	if old := c.downloads[taskID]; old != nil {
		close(old)
	}
	c.downloads[taskID] = cancel
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.downloads[taskID] == cancel {
			delete(c.downloads, taskID)
		}
		c.mu.Unlock()
	}()
	c.send(&protocol.Message{Type: protocol.MsgFileDownloadStart, TaskID: taskID, Path: path, Name: filepath.Base(path), Size: st.Size(), Offset: offset, ModTime: st.ModTime().Format(time.RFC3339)})
	buf := make([]byte, 512*1024)
	cur := offset
	for {
		select {
		case <-cancel:
			return
		default:
		}
		n, readErr := f.Read(buf)
		if n > 0 {
			select {
			case <-cancel:
				return
			default:
			}
			if err := c.sendBinaryOffset(protocol.BinFileDownloadChunk, taskID, cur, buf[:n]); err != nil {
				return
			}
			cur += int64(n)
		}
		if readErr == io.EOF {
			c.sendBinaryOffset(protocol.BinFileTransferEnd, taskID, cur, nil)
			c.send(&protocol.Message{Type: protocol.MsgFileTransferEnd, TaskID: taskID, Path: path, Size: st.Size(), Offset: cur, Success: true})
			return
		}
		if readErr != nil {
			c.sendFileTransferError(taskID, path, readErr.Error())
			return
		}
	}
}

func (c *Client) handleManagedTransferCancel(taskID string) {
	if taskID == "" {
		return
	}
	c.mu.Lock()
	if up := c.uploads[taskID]; up != nil {
		up.file.Close()
		delete(c.uploads, taskID)
	}
	if cancel := c.downloads[taskID]; cancel != nil {
		close(cancel)
		delete(c.downloads, taskID)
	}
	c.mu.Unlock()
}

func (c *Client) handleBinData(sessionID string, data []byte) {
	c.mu.Lock()
	sess, ok := c.sessions[sessionID]
	c.mu.Unlock()
	if !ok || len(data) == 0 {
		return
	}

	switch {
	case sess.ptyProc != nil:
		sess.ptyProc.Write(data)
	case sess.stdinPipe != nil:
		sess.stdinPipe.Write(data)
	case sess.sftpInput != nil:
		sess.sftpInput.Write(data)
	}
}

func (c *Client) handleBinTCPData(forwardID string, data []byte) {
	c.mu.Lock()
	conn, ok := c.forwards[forwardID]
	c.mu.Unlock()
	if !ok || len(data) == 0 {
		return
	}
	conn.Write(data)
}

func (c *Client) sendFileResult(id, path string, success bool, errText string) {
	c.send(&protocol.Message{Type: protocol.MsgFileResult, SessionID: id, FilePath: path, Success: success, Error: errText})
}

func (c *Client) handleBinFileStart(id string, payload []byte) {
	path, mode, _, err := protocol.DecodeBinFilePut(payload)
	if err != nil {
		c.sendFileResult(id, "", false, fmt.Sprintf("decode error: %v", err))
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		c.sendFileResult(id, path, false, fmt.Sprintf("mkdir error: %v", err))
		return
	}
	fm := os.FileMode(0644)
	if mode > 0 {
		fm = os.FileMode(mode)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fm)
	if err != nil {
		c.sendFileResult(id, path, false, err.Error())
		return
	}
	c.mu.Lock()
	if old := c.fileStreams[id]; old != nil {
		old.file.Close()
	}
	c.fileStreams[id] = &fileStream{path: path, file: f, mode: fm}
	c.mu.Unlock()
}

func (c *Client) handleBinFileChunk(id string, data []byte) {
	c.mu.Lock()
	fs := c.fileStreams[id]
	c.mu.Unlock()
	if fs == nil {
		c.sendFileResult(id, "", false, "file stream not found")
		return
	}
	if _, err := fs.file.Write(data); err != nil {
		fs.file.Close()
		c.mu.Lock()
		delete(c.fileStreams, id)
		c.mu.Unlock()
		c.sendFileResult(id, fs.path, false, err.Error())
	}
}

func (c *Client) handleBinFileEnd(id string) {
	c.mu.Lock()
	fs := c.fileStreams[id]
	delete(c.fileStreams, id)
	c.mu.Unlock()
	if fs == nil {
		c.sendFileResult(id, "", false, "file stream not found")
		return
	}
	if err := fs.file.Close(); err != nil {
		c.sendFileResult(id, fs.path, false, err.Error())
		return
	}
	c.sendFileResult(id, fs.path, true, "")
}

func (c *Client) handleBinFilePut(id string, payload []byte) {
	path, mode, fileData, err := protocol.DecodeBinFilePut(payload)
	if err != nil {
		c.sendFileResult(id, "", false, fmt.Sprintf("decode error: %v", err))
		return
	}

	log.Printf("file_put: writing %s (%d bytes)", path, len(fileData))

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		c.sendFileResult(id, path, false, fmt.Sprintf("mkdir error: %v", err))
		return
	}

	fm := os.FileMode(0644)
	if mode > 0 {
		fm = os.FileMode(mode)
	}
	if err := os.WriteFile(path, fileData, fm); err != nil {
		c.sendFileResult(id, path, false, err.Error())
		return
	}

	log.Printf("file_put: wrote %d bytes to %s", len(fileData), path)
	c.sendFileResult(id, path, true, "")
}

// --- Session handling ---

func (c *Client) handleNewSession(msg *protocol.Message) {
	sessionID := msg.SessionID
	log.Printf("new session: id=%s subsystem=%q command=%q pty=%v",
		sessionID, msg.Subsystem, msg.Command, msg.Pty)

	var sess *clientSession
	var err error

	switch msg.Subsystem {
	case "sftp":
		sess, err = c.startSFTPSession(sessionID)
	default:
		sess, err = c.startShellExecSession(msg)
	}

	if err != nil {
		log.Printf("session %s start failed: %v", sessionID, err)
		c.sendClose(sessionID)
		return
	}

	c.mu.Lock()
	c.sessions[sessionID] = sess
	c.mu.Unlock()
}

func (c *Client) startShellExecSession(msg *protocol.Message) (*clientSession, error) {
	sess := &clientSession{
		id:        msg.SessionID,
		subsystem: msg.Subsystem,
		command:   msg.Command,
		pty:       msg.Pty,
		done:      make(chan struct{}),
	}

	if msg.Pty {
		cfg := &ptyutil.Config{
			Command: msg.Command,
			Shell:   c.shell,
			Env:     msg.Env,
			Term:    msg.Term,
			Rows:    uint16(msg.Rows),
			Cols:    uint16(msg.Cols),
			Modes:   convertModes(msg.Modes),
		}
		proc, ptyErr := ptyutil.Start(cfg)
		if ptyErr != nil {
			// PTY unavailable (e.g. ConPty on Wine, no /dev/pts) → fallback to exec mode
			log.Printf("session %s: PTY unavailable (%v), falling back to exec mode", msg.SessionID, ptyErr)
			sess.pty = false
		} else {
			sess.ptyProc = proc

			// Read PTY output -> coalesced binary send to server.
			// WinPTY on legacy Windows emits many tiny chunks; a slightly longer
			// output-only aggregation window keeps SSH input responsive while
			// avoiding WebSocket frame storms during screen redraws.
			cw := newCoalescingWriterWithInterval(c, msg.SessionID, protocol.BinData, 16*time.Millisecond)
			readDone := make(chan struct{})
			go func() {
				defer close(readDone)
				io.Copy(cw, proc)
				cw.flush()
			}()

			go func() {
				exitCode, _ := proc.Wait()

				// On some Windows versions, ConPTY output pipe does not EOF
				// after the process exits. Close the input side first so the
				// ConPTY host drains and closes the output pipe, unblocking Read.
				// For the go-pty ConPTY backend this means we close inPipe early;
				// Process.Close handles the rest idempotently.
				proc.CloseInput()

				// Give the read loop a few seconds to drain remaining output.
				select {
				case <-readDone:
					// read loop finished normally
				case <-time.After(5 * time.Second):
					log.Printf("session %s: PTY read did not finish after process exit, forcing close", msg.SessionID)
				}

				proc.Close()
				c.sendExitCode(msg.SessionID, exitCode)
				c.sendClose(msg.SessionID)
				sess.close()
			}()

			log.Printf("session %s: PTY started (cmd=%q)", msg.SessionID, msg.Command)
			return sess, nil
		}
	}

	if msg.Command != "" {
		if gitCmd, ok := parseGitSmartSSHCommand(msg.Command); ok && !hasSystemGitCommand(gitCmd.Name) {
			return c.startGitFallbackSession(sess, gitCmd)
		}
	}

	// Exec mode (non-PTY, or PTY fallback)
	shell := c.shell
	if shell == "" {
		shell = os.Getenv("SHELL")
		if shell == "" {
			shell = os.Getenv("COMSPEC")
			if shell == "" {
				if runtime.GOOS == "windows" {
					shell = "cmd.exe"
				} else {
					shell = "/bin/sh"
				}
			}
		}
	}

	flag := "-c"
	if runtime.GOOS == "windows" {
		flag = "/c"
	}

	var cmd *exec.Cmd
	if msg.Command != "" {
		cmd = exec.Command(shell, flag, msg.Command)
	} else {
		cmd = exec.Command(shell)
	}
	cmd.Env = append(os.Environ(), msg.Env...)

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		return nil, err
	}

	cmd.Stdin = stdinR
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW

	if err := cmd.Start(); err != nil {
		stdinR.Close()
		stdinW.Close()
		stdoutR.Close()
		stdoutW.Close()
		stderrR.Close()
		stderrW.Close()
		return nil, err
	}

	stdinR.Close()
	stdoutW.Close()
	stderrW.Close()

	sess.stdinPipe = wincompat.EncodeInput(stdinW)
	sess.cmdWaitFn = func() (int, error) {
		err := cmd.Wait()
		if err == nil {
			return 0, nil
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), nil
		}
		return -1, err
	}

	var ioWg sync.WaitGroup

	ioWg.Add(1)
	go func() {
		defer ioWg.Done()
		defer stdoutR.Close()
		cw := newCoalescingWriter(c, msg.SessionID, protocol.BinData)
		io.Copy(cw, wincompat.DecodeOutput(stdoutR))
		cw.flush()
	}()

	ioWg.Add(1)
	go func() {
		defer ioWg.Done()
		defer stderrR.Close()
		cw := newCoalescingWriter(c, msg.SessionID, protocol.BinStderr)
		io.Copy(cw, wincompat.DecodeOutput(stderrR))
		cw.flush()
	}()

	go func() {
		ioWg.Wait()
		exitCode, _ := sess.cmdWaitFn()
		c.sendExitCode(msg.SessionID, exitCode)
		c.sendClose(msg.SessionID)
		sess.close()
	}()

	log.Printf("session %s: exec started (cmd=%q)", msg.SessionID, msg.Command)

	return sess, nil
}

func isGitSmartSSHCommand(command string) bool {
	_, ok := parseGitSmartSSHCommand(command)
	return ok
}

func (c *Client) startSFTPSession(sessionID string) (*clientSession, error) {
	pr1, pw1 := io.Pipe()
	pr2, pw2 := io.Pipe()

	rwc := &sftpRWC{reader: pr1, writer: pw2, closer: pw1}

	sess := &clientSession{
		id:         sessionID,
		subsystem:  "sftp",
		sftpInput:  pw1,
		sftpOutput: pr2,
		done:       make(chan struct{}),
	}

	go func() {
		defer pw2.Close()
		defer pr1.Close()

		server, err := sftp.NewServer(rwc)
		if err != nil {
			log.Printf("session %s: sftp init error: %v", sessionID, err)
			c.sendClose(sessionID)
			return
		}
		defer server.Close()

		if err := server.Serve(); err != nil && err != io.EOF {
			log.Printf("session %s: sftp error: %v", sessionID, err)
		}
		c.sendClose(sessionID)
	}()

	// SFTP output → binary frames (coalesced)
	go func() {
		cw := newCoalescingWriter(c, sessionID, protocol.BinData)
		io.Copy(cw, pr2)
		cw.flush()
	}()

	log.Printf("session %s: SFTP server started", sessionID)
	return sess, nil
}

func (c *Client) handleStdinClose(msg *protocol.Message) {
	c.mu.Lock()
	sess, ok := c.sessions[msg.SessionID]
	c.mu.Unlock()
	if !ok {
		return
	}
	if sess.stdinPipe != nil {
		sess.stdinPipe.Close()
		sess.stdinPipe = nil
	}
	if sess.sftpInput != nil {
		sess.sftpInput.Close()
	}
}

func (c *Client) handleResize(msg *protocol.Message) {
	c.mu.Lock()
	sess, ok := c.sessions[msg.SessionID]
	c.mu.Unlock()
	if !ok || sess.ptyProc == nil {
		return
	}
	if err := sess.ptyProc.Resize(uint16(msg.Rows), uint16(msg.Cols)); err != nil {
		// Silently ignore resize on closed PTY (common during session teardown)
		log.Printf("session %s: resize error: %v", msg.SessionID, err)
	}
}

func (c *Client) handleClose(msg *protocol.Message) {
	c.mu.Lock()
	sess, ok := c.sessions[msg.SessionID]
	if ok {
		delete(c.sessions, msg.SessionID)
	}
	c.mu.Unlock()
	if ok {
		sess.close()
		log.Printf("session %s closed", msg.SessionID)
	}
}

// --- TCP forwarding (-L) ---

func (c *Client) handleTCPConnect(msg *protocol.Message) {
	addr := net.JoinHostPort(msg.Host, fmt.Sprintf("%d", msg.Port))
	log.Printf("forward: connecting to %s (id=%s)", addr, msg.ForwardID)

	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		log.Printf("forward: connect to %s failed: %v", addr, err)
		c.send(&protocol.Message{
			Type:      protocol.MsgTCPFail,
			ForwardID: msg.ForwardID,
			Error:     err.Error(),
		})
		return
	}

	c.mu.Lock()
	c.forwards[msg.ForwardID] = conn
	c.mu.Unlock()

	c.send(&protocol.Message{Type: protocol.MsgTCPOpen, ForwardID: msg.ForwardID})

	// Read TCP response → binary frames
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				c.sendBinary(protocol.BinTCPData, msg.ForwardID, buf[:n])
			}
			if err != nil {
				c.send(&protocol.Message{Type: protocol.MsgTCPClose, ForwardID: msg.ForwardID})
				c.mu.Lock()
				delete(c.forwards, msg.ForwardID)
				c.mu.Unlock()
				return
			}
		}
	}()

	log.Printf("forward: connected to %s (id=%s)", addr, msg.ForwardID)
}

func (c *Client) handleTCPListen(msg *protocol.Message) {
	addr := net.JoinHostPort(msg.Host, fmt.Sprintf("%d", msg.Port))
	log.Printf("forward listen: %s (listenID=%s)", addr, msg.ListenID)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		c.send(&protocol.Message{Type: protocol.MsgTCPListenOK, ListenID: msg.ListenID, Error: err.Error()})
		return
	}
	_, portText, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portText)

	c.mu.Lock()
	if old := c.listeners[msg.ListenID]; old != nil {
		old.Close()
	}
	c.listeners[msg.ListenID] = ln
	c.mu.Unlock()
	c.send(&protocol.Message{Type: protocol.MsgTCPListenOK, ListenID: msg.ListenID, Port: port})

	go func() {
		defer func() {
			c.mu.Lock()
			if c.listeners[msg.ListenID] == ln {
				delete(c.listeners, msg.ListenID)
			}
			c.mu.Unlock()
		}()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			forwardID := generateClientForwardID()
			openCh := make(chan struct{})
			c.mu.Lock()
			c.forwards[forwardID] = conn
			c.forwardOpen[forwardID] = openCh
			c.mu.Unlock()
			c.send(&protocol.Message{
				Type:       protocol.MsgTCPAccept,
				ListenID:   msg.ListenID,
				ForwardID:  forwardID,
				SourceAddr: conn.RemoteAddr().String(),
			})
			go func() {
				select {
				case <-openCh:
					c.readTCPForward(forwardID, conn)
				case <-time.After(10 * time.Second):
					c.handleTCPClose(&protocol.Message{ForwardID: forwardID})
				}
			}()
		}
	}()
}

func (c *Client) handleTCPOpen(msg *protocol.Message) {
	c.mu.Lock()
	openCh := c.forwardOpen[msg.ForwardID]
	delete(c.forwardOpen, msg.ForwardID)
	c.mu.Unlock()
	if openCh != nil {
		safeCloseForwardOpen(openCh)
	}
}

func (c *Client) readTCPForward(forwardID string, conn net.Conn) {
	buf := make([]byte, 32*1024)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			c.sendBinary(protocol.BinTCPData, forwardID, buf[:n])
		}
		if err != nil {
			c.send(&protocol.Message{Type: protocol.MsgTCPClose, ForwardID: forwardID})
			c.mu.Lock()
			if c.forwards[forwardID] == conn {
				delete(c.forwards, forwardID)
			}
			delete(c.forwardOpen, forwardID)
			c.mu.Unlock()
			return
		}
	}
}

func (c *Client) handleTCPClose(msg *protocol.Message) {
	if msg.ListenID != "" {
		c.mu.Lock()
		ln, ok := c.listeners[msg.ListenID]
		if ok {
			delete(c.listeners, msg.ListenID)
		}
		c.mu.Unlock()
		if ok {
			ln.Close()
			log.Printf("forward listen: closed %s", msg.ListenID)
		}
		return
	}
	c.mu.Lock()
	conn, ok := c.forwards[msg.ForwardID]
	if ok {
		delete(c.forwards, msg.ForwardID)
	}
	openCh := c.forwardOpen[msg.ForwardID]
	delete(c.forwardOpen, msg.ForwardID)
	c.mu.Unlock()
	if openCh != nil {
		safeCloseForwardOpen(openCh)
	}
	if ok {
		conn.Close()
		log.Printf("forward: closed %s", msg.ForwardID)
	}
}

func safeCloseForwardOpen(ch chan struct{}) {
	defer func() { recover() }()
	close(ch)
}

func generateClientForwardID() string {
	return fmt.Sprintf("cf-%d-%d", time.Now().UnixNano(), os.Getpid())
}

// --- WebSocket send helpers ---

func (c *Client) send(msg *protocol.Message) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("not connected")
	}
	data, err := protocol.Encode(msg)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err = conn.WriteMessage(gws.OpcodeText, data)
	_ = conn.SetWriteDeadline(time.Time{})
	return err
}

func (c *Client) sendBinary(typ byte, id string, data []byte) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("not connected")
	}
	frame := protocol.EncodeBinFrame(typ, id, data)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err := conn.WriteMessage(gws.OpcodeBinary, frame)
	_ = conn.SetWriteDeadline(time.Time{})
	return err
}

func (c *Client) sendClose(sessionID string) error {
	return c.send(&protocol.Message{
		Type:      protocol.MsgClose,
		SessionID: sessionID,
	})
}

func (c *Client) sendExitCode(sessionID string, code int) error {
	return c.send(&protocol.Message{
		Type:      protocol.MsgExitCode,
		SessionID: sessionID,
		ExitCode:  code,
	})
}

// --- Cleanup ---

func (c *Client) cleanup() {
	c.mu.Lock()
	for sid, sess := range c.sessions {
		sess.close()
		delete(c.sessions, sid)
	}
	for fid, conn := range c.forwards {
		conn.Close()
		delete(c.forwards, fid)
	}
	for id, fs := range c.fileStreams {
		fs.file.Close()
		delete(c.fileStreams, id)
	}
	conn := c.conn
	c.mu.Unlock()
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

package client

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lxzan/gws"
	"github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"
	"rdev/internal/protocol"
	"rdev/internal/ptyutil"
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
	mu        sync.Mutex
}

func newCoalescingWriter(client *Client, sessionID string, typ byte) *coalescingWriter {
	return &coalescingWriter{
		client:    client,
		sessionID: sessionID,
		typ:       typ,
		buf:       *bytes.NewBuffer(make([]byte, 0, 8192)),
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
		w.timer = time.AfterFunc(5*time.Millisecond, w.flush)
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

// Client is the rdev client that connects to the server
type Client struct {
	serverURL   string
	clientID    string
	password    string
	shell       string
	conn        *gws.Conn
	writeMu     sync.Mutex
	sessions    map[string]*clientSession
	forwards    map[string]net.Conn
	fileStreams map[string]*fileStream
	mu          sync.Mutex
	done        chan struct{}

	// Server info (received on register response)
	sshPort  string
	httpHost string

	// OnConnect is called after successfully connecting and registering.
	OnConnect func(c *Client)
}

// NewClient creates a new client
func NewClient(serverURL, clientID, password, shell string) *Client {
	return &Client{
		serverURL:   serverURL,
		clientID:    clientID,
		password:    password,
		shell:       shell,
		sessions:    make(map[string]*clientSession),
		forwards:    make(map[string]net.Conn),
		fileStreams: make(map[string]*fileStream),
		done:        make(chan struct{}, 1),
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
		Type:     protocol.MsgRegister,
		ClientID: h.client.clientID,
		Password: h.client.password,
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
	case protocol.MsgTCPClose:
		c.handleTCPClose(msg)
	}
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

			// Read PTY output -> coalesced binary send to server
			cw := newCoalescingWriter(c, msg.SessionID, protocol.BinData)
			go func() {
				io.Copy(cw, proc)
				cw.flush()
			}()

			go func() {
				exitCode, _ := proc.Wait()
				proc.Close()
				c.sendExitCode(msg.SessionID, exitCode)
				c.sendClose(msg.SessionID)
				sess.close()
			}()

			log.Printf("session %s: PTY started (cmd=%q)", msg.SessionID, msg.Command)
			return sess, nil
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

	sess.stdinPipe = stdinW

	// For non-interactive commands, close stdin immediately so the shell doesn't hang
	if msg.Command != "" {
		stdinW.Close()
		sess.stdinPipe = nil
	}
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
		io.Copy(cw, stdoutR)
		cw.flush()
	}()

	ioWg.Add(1)
	go func() {
		defer ioWg.Done()
		defer stderrR.Close()
		cw := newCoalescingWriter(c, msg.SessionID, protocol.BinStderr)
		io.Copy(cw, stderrR)
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
	sess.ptyProc.Resize(uint16(msg.Rows), uint16(msg.Cols))
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

func (c *Client) handleTCPClose(msg *protocol.Message) {
	c.mu.Lock()
	conn, ok := c.forwards[msg.ForwardID]
	if ok {
		delete(c.forwards, msg.ForwardID)
	}
	c.mu.Unlock()
	if ok {
		conn.Close()
		log.Printf("forward: closed %s", msg.ForwardID)
	}
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
	return conn.WriteMessage(gws.OpcodeText, data)
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
	return conn.WriteMessage(gws.OpcodeBinary, frame)
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

// HTTPHost returns the server's HTTP host:port (received on register).
func (c *Client) HTTPHost() string { return c.httpHost }

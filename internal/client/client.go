package client

import (
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lxzan/gws"
	"github.com/pkg/sftp"
	"rdev/internal/protocol"
	"rdev/internal/ptyutil"
)

// --- io.Writer adapters for sending data via WebSocket ---

type dataWriter struct {
	client    *Client
	sessionID string
}

func (d *dataWriter) Write(p []byte) (int, error) {
	if err := d.client.sendData(d.sessionID, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

type stderrWriter struct {
	client    *Client
	sessionID string
}

func (s *stderrWriter) Write(p []byte) (int, error) {
	if err := s.client.sendStderr(s.sessionID, p); err != nil {
		return 0, err
	}
	return len(p), nil
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

// Client is the rdev client that connects to the server
type Client struct {
	serverURL string
	clientID  string
	password  string
	shell     string
	conn      *gws.Conn
	sessions  map[string]*clientSession
	forwards  map[string]net.Conn
	mu        sync.Mutex
	done      chan struct{}
	connected chan struct{} // signaled when connected
}

// NewClient creates a new client
func NewClient(serverURL, clientID, password, shell string) *Client {
	return &Client{
		serverURL: serverURL,
		clientID:  clientID,
		password:  password,
		shell:     shell,
		sessions:  make(map[string]*clientSession),
		forwards:  make(map[string]net.Conn),
		done:      make(chan struct{}),
		connected: make(chan struct{}, 1),
	}
}

// wsEventHandler implements gws.Event for the client
type wsEventHandler struct {
	gws.BuiltinEventHandler
	client *Client
}

func (h *wsEventHandler) OnOpen(socket *gws.Conn) {
	// Send register message
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

	// Signal disconnect
	select {
	case h.client.done <- struct{}{}:
	default:
	}
}

func (h *wsEventHandler) OnMessage(socket *gws.Conn, message *gws.Message) {
	defer message.Close()

	msg, err := protocol.Decode(message.Bytes())
	if err != nil {
		return
	}
	h.client.handleMessage(msg)
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
	if !strings.HasPrefix(wsURL, "ws://") && !strings.HasPrefix(wsURL, "wss://") {
		wsURL = "ws://" + wsURL
	}
	if !strings.HasSuffix(wsURL, "/ws") {
		wsURL += "/ws"
	}

	handler := &wsEventHandler{client: c}
	socket, _, err := gws.NewClient(handler, &gws.ClientOption{
		Addr:              wsURL,
		HandshakeTimeout:  10 * time.Second,
		ReadMaxPayloadSize: 16 * 1024 * 1024,
	})
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}

	go socket.ReadLoop()
	return nil
}

func (c *Client) handleMessage(msg *protocol.Message) {
	switch msg.Type {
	case protocol.MsgNewSession:
		c.handleNewSession(msg)
	case protocol.MsgData:
		c.handleData(msg)
	case protocol.MsgStdinClose:
		c.handleStdinClose(msg)
	case protocol.MsgResize:
		c.handleResize(msg)
	case protocol.MsgClose:
		c.handleClose(msg)

	// TCP forwarding
	case protocol.MsgTCPConnect:
		c.handleTCPConnect(msg)
	case protocol.MsgTCPData:
		c.handleTCPData(msg)
	case protocol.MsgTCPClose:
		c.handleTCPClose(msg)

	// File distribution
	case protocol.MsgFilePut:
		c.handleFilePut(msg)
	}
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
		}
		proc, err := ptyutil.Start(cfg)
		if err != nil {
			return nil, err
		}
		sess.ptyProc = proc

		// Read PTY output -> send to server
		go func() {
			io.Copy(&dataWriter{c, msg.SessionID}, proc)
		}()

		// Wait for process to exit, then send exit code and close
		go func() {
			exitCode, _ := proc.Wait()
			proc.Close()
			c.sendExitCode(msg.SessionID, exitCode)
			c.sendClose(msg.SessionID)
			sess.close()
		}()

		log.Printf("session %s: PTY started (cmd=%q)", msg.SessionID, msg.Command)
	} else {
		// Non-PTY exec: use os.Pipe for reliable I/O
		shell := c.shell
		if shell == "" {
			shell = os.Getenv("SHELL")
			if shell == "" {
				shell = "/bin/sh"
			}
		}

		var cmd *exec.Cmd
		if msg.Command != "" {
			cmd = exec.Command(shell, "-c", msg.Command)
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
			stdinR.Close(); stdinW.Close()
			stdoutR.Close(); stdoutW.Close()
			stderrR.Close(); stderrW.Close()
			return nil, err
		}

		stdinR.Close()
		stdoutW.Close()
		stderrW.Close()

		sess.stdinPipe = stdinW
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
			io.Copy(&dataWriter{c, msg.SessionID}, stdoutR)
		}()

		ioWg.Add(1)
		go func() {
			defer ioWg.Done()
			defer stderrR.Close()
			io.Copy(&stderrWriter{c, msg.SessionID}, stderrR)
		}()

		go func() {
			ioWg.Wait()
			exitCode, _ := sess.cmdWaitFn()
			c.sendExitCode(msg.SessionID, exitCode)
			c.sendClose(msg.SessionID)
			sess.close()
		}()

		log.Printf("session %s: exec started (cmd=%q)", msg.SessionID, msg.Command)
	}

	return sess, nil
}

func (c *Client) startSFTPSession(sessionID string) (*clientSession, error) {
	pr1, pw1 := io.Pipe() // WebSocket -> SFTP server
	pr2, pw2 := io.Pipe() // SFTP server -> WebSocket

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

	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := pr2.Read(buf)
			if n > 0 {
				c.sendData(sessionID, buf[:n])
			}
			if err != nil {
				break
			}
		}
	}()

	log.Printf("session %s: SFTP server started", sessionID)
	return sess, nil
}

func (c *Client) handleData(msg *protocol.Message) {
	data, err := protocol.DecodeData(msg.Data)
	if err != nil || len(data) == 0 {
		return
	}

	c.mu.Lock()
	sess, ok := c.sessions[msg.SessionID]
	c.mu.Unlock()

	if !ok {
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

// --- File distribution ---

func (c *Client) handleFilePut(msg *protocol.Message) {
	path := msg.FilePath
	log.Printf("file_put: writing %s", path)

	data, err := protocol.DecodeData(msg.Data)
	if err != nil {
		c.send(&protocol.Message{
			Type:     protocol.MsgFileResult,
			FilePath: path,
			Error:    fmt.Sprintf("decode error: %v", err),
		})
		return
	}

	// Ensure parent directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		c.send(&protocol.Message{
			Type:     protocol.MsgFileResult,
			FilePath: path,
			Error:    fmt.Sprintf("mkdir error: %v", err),
		})
		return
	}

	mode := os.FileMode(0644)
	if msg.FileMode > 0 {
		mode = os.FileMode(msg.FileMode)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		c.send(&protocol.Message{
			Type:     protocol.MsgFileResult,
			FilePath: path,
			Error:    err.Error(),
		})
		return
	}

	log.Printf("file_put: wrote %d bytes to %s", len(data), path)
	c.send(&protocol.Message{
		Type:     protocol.MsgFileResult,
		FilePath: path,
		Success:  true,
	})
}

// --- TCP forwarding (-L) ---

func (c *Client) handleTCPConnect(msg *protocol.Message) {
	addr := fmt.Sprintf("%s:%d", msg.Host, msg.Port)
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

	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				c.send(&protocol.Message{
					Type:      protocol.MsgTCPData,
					ForwardID: msg.ForwardID,
					Data:      protocol.EncodeData(buf[:n]),
				})
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

func (c *Client) handleTCPData(msg *protocol.Message) {
	c.mu.Lock()
	conn, ok := c.forwards[msg.ForwardID]
	c.mu.Unlock()

	if !ok {
		return
	}

	data, err := protocol.DecodeData(msg.Data)
	if err != nil || len(data) == 0 {
		return
	}
	conn.Write(data)
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
	return conn.WriteMessage(gws.OpcodeText, data)
}

func (c *Client) sendData(sessionID string, raw []byte) error {
	return c.send(&protocol.Message{
		Type:      protocol.MsgData,
		SessionID: sessionID,
		Data:      protocol.EncodeData(raw),
	})
}

func (c *Client) sendStderr(sessionID string, raw []byte) error {
	return c.send(&protocol.Message{
		Type:      protocol.MsgStderrData,
		SessionID: sessionID,
		Stderr:    protocol.EncodeData(raw),
	})
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
	c.mu.Unlock()

	if c.conn != nil {
		c.conn.WriteClose(1000, nil)
	}
	close(c.done)
}

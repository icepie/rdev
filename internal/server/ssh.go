package server

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	gossh "golang.org/x/crypto/ssh"

	"github.com/gliderlabs/ssh"
	"rdev/internal/protocol"
)

// SSHServer wraps the gliderlabs SSH server
type SSHServer struct {
	server     *ssh.Server
	srv        *Server
	hostKey    gossh.Signer
	authKeys   []gossh.PublicKey
	authKeysMu sync.RWMutex
	fwdHandler *ForwardedTCPHandler // for -R port forwarding
}

// NewSSHServer creates a new SSH server
func NewSSHServer(srv *Server, addr, hostKeyPath, authorizedKeysPath string) (*SSHServer, error) {
	s := &SSHServer{srv: srv}

	signer, err := s.loadOrGenerateHostKey(hostKeyPath)
	if err != nil {
		return nil, fmt.Errorf("host key: %w", err)
	}
	s.hostKey = signer
	s.loadAuthorizedKeys(authorizedKeysPath)
	s.fwdHandler = &ForwardedTCPHandler{}

	sshServer := &ssh.Server{
		Addr:                       addr,
		Handler:                    s.handleSession,
		PublicKeyHandler:           s.handlePublicKey,
		PasswordHandler:            s.handlePassword,
		KeyboardInteractiveHandler: s.handleKeyboardInteractive,
		BannerHandler: func(ctx ssh.Context) string {
			clientID := ctx.User()
			if _, ok := srv.GetClient(clientID); !ok {
				return fmt.Sprintf("\r\n⚠ Device '%s' is not connected.\r\n", clientID)
			}
			return ""
		},
		SubsystemHandlers: map[string]ssh.SubsystemHandler{
			"sftp": s.handleSession,
		},
		// Port forwarding
		ChannelHandlers: map[string]ssh.ChannelHandler{
			"session":      ssh.DefaultSessionHandler,
			"direct-tcpip": s.handleDirectTCPIP,
		},
		RequestHandlers: map[string]ssh.RequestHandler{
			"tcpip-forward":        s.fwdHandler.HandleSSHRequest,
			"cancel-tcpip-forward": s.fwdHandler.HandleSSHRequest,
		},
		// Allow all port forwarding by default
		LocalPortForwardingCallback: func(ctx ssh.Context, destAddr string, destPort uint32) bool {
			clientID := ctx.User()
			_, ok := srv.GetClient(clientID)
			if !ok {
				log.Printf("ssh fwd -L: client %s not connected, denied", clientID)
				return false
			}
			log.Printf("ssh fwd -L: %s -> %s:%d allowed", clientID, destAddr, destPort)
			return true
		},
		ReversePortForwardingCallback: func(ctx ssh.Context, bindAddr string, bindPort uint32) bool {
			log.Printf("ssh fwd -R: %s bind %s:%d allowed", ctx.User(), bindAddr, bindPort)
			return true
		},
	}

	sshServer.AddHostKey(signer)

	s.server = sshServer
	return s, nil
}

// ListenAndServe starts the SSH server
func (s *SSHServer) ListenAndServe() error {
	log.Printf("SSH server listening on %s", s.server.Addr)
	return s.server.ListenAndServe()
}

// --- Host key and auth ---

func (s *SSHServer) loadOrGenerateHostKey(path string) (gossh.Signer, error) {
	if path == "" {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return nil, err
		}
		return gossh.NewSignerFromKey(key)
	}
	data, err := os.ReadFile(path)
	if err == nil {
		return gossh.ParsePrivateKey(data)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	pemData := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, pemData, 0600); err != nil {
		return nil, err
	}
	log.Printf("generated new host key: %s", path)
	return gossh.NewSignerFromKey(key)
}

func (s *SSHServer) loadAuthorizedKeys(path string) {
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("error reading authorized_keys: %v", err)
		}
		return
	}
	s.authKeysMu.Lock()
	defer s.authKeysMu.Unlock()
	s.authKeys = nil
	for len(data) > 0 {
		pubKey, _, _, rest, err := gossh.ParseAuthorizedKey(data)
		if err != nil {
			break
		}
		s.authKeys = append(s.authKeys, pubKey)
		data = rest
	}
	log.Printf("loaded %d authorized keys from %s", len(s.authKeys), path)
}

func (s *SSHServer) handlePublicKey(ctx ssh.Context, key ssh.PublicKey) bool {
	clientID := ctx.User()
	client, ok := s.srv.GetClient(clientID)
	if !ok {
		return true // let auth pass, session handler will show error and close
	}
	s.authKeysMu.RLock()
	defer s.authKeysMu.RUnlock()
	for _, authKey := range s.authKeys {
		if ssh.KeysEqual(key, authKey) {
			log.Printf("ssh auth: %s authenticated via public key", clientID)
			return true
		}
	}
	// No authorized_keys match: allow if client has no password (open mode)
	if client.Password == "" {
		log.Printf("ssh auth: %s accepted (no auth required)", clientID)
		return true
	}
	return false
}

func (s *SSHServer) handlePassword(ctx ssh.Context, pass string) bool {
	clientID := ctx.User()
	client, ok := s.srv.GetClient(clientID)
	if !ok {
		return true // let auth pass, session handler will show error and close
	}
	// Open mode: no password set, any password works
	if client.Password == "" {
		log.Printf("ssh auth: %s accepted (no auth required)", clientID)
		return true
	}
	if client.Password == pass {
		log.Printf("ssh auth: %s authenticated via password", clientID)
		return true
	}
	return false
}

func (s *SSHServer) handleKeyboardInteractive(ctx ssh.Context, challenger gossh.KeyboardInteractiveChallenge) bool {
	clientID := ctx.User()
	client, ok := s.srv.GetClient(clientID)
	if !ok {
		return true // let auth pass, session handler will show error and close
	}
	// Open mode: no challenge, auto-accept
	if client.Password == "" {
		log.Printf("ssh auth: %s accepted via keyboard-interactive (no auth required)", clientID)
		return true
	}
	// Password-protected: challenge the user
	answers, err := challenger("RDev Authentication", "", []string{"Password: "}, []bool{false})
	if err != nil || len(answers) == 0 {
		return false
	}
	if answers[0] == client.Password {
		log.Printf("ssh auth: %s authenticated via keyboard-interactive", clientID)
		return true
	}
	return false
}

// --- Session handling (shell/exec/sftp) ---

func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *SSHServer) handleSession(sess ssh.Session) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC in handleSession: %v", r)
			sess.Exit(1)
		}
	}()
	clientID := sess.User()
	client, ok := s.srv.GetClient(clientID)
	if !ok {
		fmt.Fprintf(sess, "rdev: client '%s' is not connected\n", clientID)
		sess.Exit(1)
		return
	}

	sessionID := generateID()
	subsystem := sess.Subsystem()
	ptyReq, winCh, isPty := sess.Pty()

	// Request PTY on the device only for interactive shell sessions
	wantPty := isPty && subsystem == ""

	command := sess.RawCommand()
	newSessionMsg := &protocol.Message{
		Type:      protocol.MsgNewSession,
		ClientID:  clientID,
		SessionID: sessionID,
		Subsystem: subsystem,
		Command:   command,
		Pty:       wantPty,
		Env:       sess.Environ(),
		Term:      ptyReq.Term,
		Rows:      ptyReq.Window.Height,
		Cols:      ptyReq.Window.Width,
		Modes:     ptyReq.Modes,
	}

	proxySess := &ProxySession{
		ID:       sessionID,
		ClientID: clientID,
		WriteCh:  make(chan []byte, 8192),
		StderrCh: make(chan []byte, 2048),
		CloseCh:  make(chan struct{}, 1),
		Done:     make(chan struct{}),
		exitDone: make(chan struct{}),
		CloseSSH: func() { sess.Close() },
		ExitSSH:  func(code int) { sess.Exit(code) },
	}
	proxySess.SetSessionMeta(wantPty, ptyReq.Term, command, subsystem, ptyReq.Window.Height, ptyReq.Window.Width)

	if !s.srv.RegisterSession(proxySess, client) {
		fmt.Fprintf(sess, "rdev: too many active sessions on device\n")
		sess.Exit(1)
		return
	}
	defer func() {
		s.srv.removeSession(sessionID)
		client.mu.Lock()
		delete(client.Sessions, sessionID)
		client.mu.Unlock()
	}()

	if err := client.Send(newSessionMsg); err != nil {
		fmt.Fprintf(sess, "rdev: failed to reach client\n")
		sess.Exit(1)
		return
	}

	var once sync.Once
	cleanup := func() { once.Do(func() { close(proxySess.Done) }) }

	// Window resize
	if isPty && winCh != nil {
		go func() {
			for win := range winCh {
				client.Send(&protocol.Message{
					Type:      protocol.MsgResize,
					SessionID: sessionID,
					Rows:      win.Height,
					Cols:      win.Width,
				})
			}
		}()
	}

	// SSH client stdin -> client device (binary frame)
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := sess.Read(buf)
			if n > 0 {
				client.SendBinary(protocol.BinData, sessionID, buf[:n])
			}
			if err != nil {
				if err == io.EOF {
					client.Send(&protocol.Message{Type: protocol.MsgStdinClose, SessionID: sessionID})
				} else {
					client.Send(&protocol.Message{Type: protocol.MsgClose, SessionID: sessionID})
					cleanup()
				}
				return
			}
		}
	}()

	// Client device stdout -> SSH client
	go func() {
		for data := range proxySess.WriteCh {
			sess.Write(data)
		}
		sess.Exit(proxySess.WaitExitCode(500 * time.Millisecond))
		cleanup()
	}()

	// Client device stderr -> SSH client
	go func() {
		if stderr := sess.Stderr(); stderr != nil {
			for data := range proxySess.StderrCh {
				stderr.Write(data)
			}
		}
	}()

	// When client device says Close, drain channels and exit
	go func() {
		<-proxySess.CloseCh
		proxySess.NotifyObserversClose()
		proxySess.CloseOutput()
	}()

	<-proxySess.Done
	proxySess.NotifyObserversClose() // ensure observers are notified on any exit path
}

// --- Port forwarding: -L (local/direct-tcpip) ---

func (s *SSHServer) handleDirectTCPIP(srv *ssh.Server, conn *gossh.ServerConn, newChan gossh.NewChannel, ctx ssh.Context) {
	clientID := ctx.User()
	client, ok := s.srv.GetClient(clientID)
	if !ok {
		newChan.Reject(gossh.ConnectionFailed, "client not connected")
		return
	}

	d := localForwardChannelData{}
	if err := gossh.Unmarshal(newChan.ExtraData(), &d); err != nil {
		newChan.Reject(gossh.ConnectionFailed, "bad forward data")
		return
	}

	// Accept the SSH channel
	ch, reqs, err := newChan.Accept()
	if err != nil {
		return
	}
	go gossh.DiscardRequests(reqs)

	forwardID := generateID()
	dest := net.JoinHostPort(d.DestAddr, strconv.FormatUint(uint64(d.DestPort), 10))
	log.Printf("ssh fwd -L: %s -> %s (forwardID=%s)", clientID, dest, forwardID)

	// Send connect request to client device
	client.Send(&protocol.Message{
		Type:      protocol.MsgTCPConnect,
		ClientID:  clientID,
		ForwardID: forwardID,
		Host:      d.DestAddr,
		Port:      int(d.DestPort),
	})

	fwd := &ProxyForward{
		ID:       forwardID,
		ClientID: clientID,
		WriteCh:  make(chan []byte, 16384),
		CloseCh:  make(chan struct{}, 1),
		Done:     make(chan struct{}),
		CloseSSH: func() { ch.Close() },
	}
	if !s.srv.RegisterForward(fwd, client) {
		newChan.Reject(gossh.ResourceShortage, "too many active forwards")
		return
	}
	defer func() {
		s.srv.removeForward(forwardID)
		client.mu.Lock()
		delete(client.Forwards, forwardID)
		client.mu.Unlock()
	}()

	var once sync.Once
	cleanup := func() { once.Do(func() { close(fwd.Done) }) }

	// SSH channel -> client device (binary frame)
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := ch.Read(buf)
			if n > 0 {
				client.SendBinary(protocol.BinTCPData, forwardID, buf[:n])
			}
			if err != nil {
				client.Send(&protocol.Message{Type: protocol.MsgTCPClose, ForwardID: forwardID})
				cleanup()
				return
			}
		}
	}()

	// Client device -> SSH channel (data from remote target)
	go func() {
		for data := range fwd.WriteCh {
			if _, err := ch.Write(data); err != nil {
				cleanup()
				return
			}
		}
		ch.Close()
		cleanup()
	}()

	// When close signal received, close the write channel
	go func() {
		<-fwd.CloseCh
		fwd.CloseOutput()
	}()

	<-fwd.Done
}

// localForwardChannelData matches RFC4254 Section 7.2
type localForwardChannelData struct {
	DestAddr   string
	DestPort   uint32
	OriginAddr string
	OriginPort uint32
}

// WatchAuthorizedKeys periodically reloads the authorized_keys file
func (s *SSHServer) WatchAuthorizedKeys(path string) {
	if path == "" {
		return
	}
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			s.loadAuthorizedKeys(path)
		}
	}()
}

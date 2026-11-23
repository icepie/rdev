package client

import (
	"crypto/rand"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lxzan/gws"
	kcp "github.com/xtaci/kcp-go/v5"
	"rdev/internal/protocol"
	tframe "rdev/internal/transport"
)

type clientTransport interface {
	WriteJSON([]byte) error
	WriteBinary([]byte) error
	WritePing([]byte) error
	Close(string) error
}

type wsClientTransport struct{ conn *gws.Conn }

func (t *wsClientTransport) WriteJSON(data []byte) error {
	_ = t.conn.SetWriteDeadline(time.Now().Add(clientWriteWait))
	defer t.conn.SetWriteDeadline(time.Time{})
	return t.conn.WriteMessage(gws.OpcodeText, data)
}
func (t *wsClientTransport) WriteBinary(data []byte) error {
	_ = t.conn.SetWriteDeadline(time.Now().Add(clientWriteWait))
	defer t.conn.SetWriteDeadline(time.Time{})
	return t.conn.WriteMessage(gws.OpcodeBinary, data)
}
func (t *wsClientTransport) WritePing(payload []byte) error {
	_ = t.conn.SetWriteDeadline(time.Now().Add(clientWriteWait))
	defer t.conn.SetWriteDeadline(time.Time{})
	return t.conn.WritePing(payload)
}
func (t *wsClientTransport) Close(reason string) error {
	return t.conn.WriteClose(1000, []byte(reason))
}

type streamClientTransport struct {
	conn net.Conn
	mu   sync.Mutex
}

func (t *streamClientTransport) write(kind byte, payload []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	_ = t.conn.SetWriteDeadline(time.Now().Add(clientWriteWait))
	defer t.conn.SetWriteDeadline(time.Time{})
	return tframe.WriteFrame(t.conn, kind, payload)
}
func (t *streamClientTransport) WriteJSON(data []byte) error { return t.write(tframe.KindJSON, data) }
func (t *streamClientTransport) WriteBinary(data []byte) error {
	return t.write(tframe.KindBinary, data)
}
func (t *streamClientTransport) WritePing(payload []byte) error {
	return t.write(tframe.KindPing, payload)
}
func (t *streamClientTransport) Close(reason string) error {
	_ = t.write(tframe.KindClose, []byte(reason))
	return t.conn.Close()
}

type reconnectBackoff struct {
	min     time.Duration
	max     time.Duration
	current time.Duration
}

func newReconnectBackoff(minDelay, maxDelay time.Duration) *reconnectBackoff {
	if minDelay <= 0 {
		minDelay = defaultReconnectMin
	}
	if maxDelay < minDelay {
		maxDelay = minDelay
	}
	return &reconnectBackoff{min: minDelay, max: maxDelay, current: minDelay}
}

func (b *reconnectBackoff) Reset() {
	b.current = b.min
}

func (b *reconnectBackoff) Next() time.Duration {
	base := b.current
	b.current *= 2
	if b.current > b.max || b.current < 0 {
		b.current = b.max
	}
	if base <= 0 {
		return b.min
	}
	jitter := base / 5
	if jitter <= 0 {
		return base
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(jitter*2)+1))
	if err != nil {
		return base
	}
	return base - jitter + time.Duration(n.Int64())
}

// wsEventHandler implements gws.Event for the client
type wsEventHandler struct {
	gws.BuiltinEventHandler
	client *Client
}

func (h *wsEventHandler) OnOpen(socket *gws.Conn) {
	_ = socket.SetDeadline(time.Now().Add(clientReadWait))
	h.client.mu.Lock()
	h.client.conn = socket
	h.client.transport = &wsClientTransport{conn: socket}
	h.client.mu.Unlock()
	if h.client.identityErr != nil {
		log.Printf("identity credentials unavailable: %v", h.client.identityErr)
		socket.WriteClose(1011, []byte("identity credentials unavailable"))
		return
	}
	if err := h.client.send(h.client.registrationMessage()); err != nil {
		log.Printf("register send error: %v", err)
		return
	}
	log.Printf("connected to %s as '%s'", h.client.serverURL, h.client.requestedID)
	// OnConnect will be called after receiving the register response
	// (in handleMessage when MsgRegister response arrives with sshPort)
}

func (h *wsEventHandler) OnClose(socket *gws.Conn, err error) {
	log.Printf("connection closed: %v", err)
	c := h.client
	c.mu.Lock()
	if c.conn != socket {
		c.mu.Unlock()
		return
	}
	uploads, desktops := c.teardownState()
	c.mu.Unlock()
	c.finishTeardown(uploads, desktops)
	c.notifyDisconnect()
}

func (h *wsEventHandler) OnPing(socket *gws.Conn, payload []byte) {
	_ = socket.SetDeadline(time.Now().Add(clientReadWait))
	h.client.writeMu.Lock()
	_ = socket.WritePong(payload)
	h.client.writeMu.Unlock()
}

func (h *wsEventHandler) OnPong(socket *gws.Conn, payload []byte) {
	_ = socket.SetDeadline(time.Now().Add(clientReadWait))
}

func (h *wsEventHandler) OnMessage(socket *gws.Conn, message *gws.Message) {
	defer message.Close()
	if !h.client.isCurrentConn(socket) {
		return
	}
	_ = socket.SetDeadline(time.Now().Add(clientReadWait))

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

func (c *Client) isCurrentConn(socket *gws.Conn) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn == socket
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
		if err == nil && h.client.fileRTCFor(taskID) == nil {
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
	backoff := newReconnectBackoff(c.reconnectMin, c.reconnectMax)

	for {
		if c.drainReconnectSignals() {
			backoff.Reset()
		}
		err := c.connect()
		if err != nil {
			delay := backoff.Next()
			log.Printf("connection error: %v, reconnecting in %s...", err, delay.Round(time.Millisecond))
			select {
			case <-time.After(delay):
				continue
			case <-sigCh:
				return nil
			}
		}

		for connected := true; connected; {
			select {
			case <-c.reconnectReset:
				backoff.Reset()
			case <-c.done:
				if c.drainReconnectSignals() {
					backoff.Reset()
				}
				delay := backoff.Next()
				log.Printf("disconnected, reconnecting in %s...", delay.Round(time.Millisecond))
				select {
				case <-time.After(delay):
					connected = false
				case <-sigCh:
					return nil
				}
			case <-sigCh:
				c.cleanup()
				return nil
			}
		}
	}
}

func (c *Client) drainReconnectSignals() bool {
	reset := false
	for {
		select {
		case <-c.done:
		case <-c.reconnectReset:
			reset = true
		default:
			return reset
		}
	}
}

func resolveWebSocketURL(wsURL string, maxRedirects int) (string, error) {
	current := strings.TrimSpace(wsURL)
	if current == "" {
		return "", fmt.Errorf("empty websocket url")
	}
	for i := 0; i <= maxRedirects; i++ {
		normalized, err := normalizeWebSocketURL(current)
		if err != nil {
			return "", err
		}
		current = normalized
		probe := current
		if strings.HasPrefix(probe, "wss://") {
			probe = "https://" + strings.TrimPrefix(probe, "wss://")
		} else if strings.HasPrefix(probe, "ws://") {
			probe = "http://" + strings.TrimPrefix(probe, "ws://")
		}
		client := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
		req, err := http.NewRequest(http.MethodGet, probe, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("User-Agent", "rdev-client")
		resp, err := client.Do(req)
		if err != nil {
			return current, nil
		}
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
			loc := strings.TrimSpace(resp.Header.Get("Location"))
			if loc == "" {
				return "", fmt.Errorf("redirect without location")
			}
			base, err := url.Parse(current)
			if err != nil {
				return "", err
			}
			rel, err := url.Parse(loc)
			if err != nil {
				return "", err
			}
			next := base.ResolveReference(rel)
			current = next.String()
			if strings.HasPrefix(current, "http://") {
				current = "ws://" + strings.TrimPrefix(current, "http://")
			} else if strings.HasPrefix(current, "https://") {
				current = "wss://" + strings.TrimPrefix(current, "https://")
			}
			continue
		default:
			return current, nil
		}
	}
	return current, nil
}

func normalizeWebSocketURL(value string) (string, error) {
	urlValue := strings.TrimSpace(value)
	if urlValue == "" {
		return "", fmt.Errorf("empty websocket url")
	}
	if strings.HasPrefix(urlValue, "wss:///") {
		urlValue = "wss://" + strings.TrimLeft(urlValue[len("wss://"):], "/")
	} else if strings.HasPrefix(urlValue, "ws:///") {
		urlValue = "ws://" + strings.TrimLeft(urlValue[len("ws://"):], "/")
	} else if strings.HasPrefix(urlValue, "https:///") {
		urlValue = "https://" + strings.TrimLeft(urlValue[len("https://"):], "/")
	} else if strings.HasPrefix(urlValue, "http:///") {
		urlValue = "http://" + strings.TrimLeft(urlValue[len("http://"):], "/")
	}
	if strings.HasPrefix(urlValue, "https://") {
		urlValue = "wss://" + strings.TrimPrefix(urlValue, "https://")
	} else if strings.HasPrefix(urlValue, "http://") {
		urlValue = "ws://" + strings.TrimPrefix(urlValue, "http://")
	} else if !strings.HasPrefix(urlValue, "ws://") && !strings.HasPrefix(urlValue, "wss://") {
		urlValue = "ws://" + urlValue
	}
	parsed, err := url.Parse(urlValue)
	if err != nil {
		return "", err
	}
	if parsed.Path == "" || parsed.Path == "/" {
		parsed.Path = "/ws"
	} else if !strings.HasSuffix(parsed.Path, "/ws") {
		parsed.Path = strings.TrimRight(parsed.Path, "/") + "/ws"
	}
	return parsed.String(), nil
}

func splitServerEndpoints(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func normalizeServerEndpoint(value string) (kind, endpoint string, err error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return "", "", fmt.Errorf("empty server endpoint")
	}
	fixTriple := func(prefix string) {
		if strings.HasPrefix(v, prefix+":///") {
			v = prefix + "://" + strings.TrimLeft(v[len(prefix+"://"):], "/")
		}
	}
	for _, p := range []string{"tcp", "kcp", "udp", "ws", "wss", "http", "https"} {
		fixTriple(p)
	}
	switch {
	case strings.HasPrefix(v, "tcp://"):
		return "tcp", v, nil
	case strings.HasPrefix(v, "kcp://"), strings.HasPrefix(v, "udp://"):
		return "kcp", v, nil
	case strings.HasPrefix(v, "ws://"), strings.HasPrefix(v, "wss://"), strings.HasPrefix(v, "http://"), strings.HasPrefix(v, "https://"):
		return "ws", v, nil
	default:
		return "tcp", "tcp://" + v, nil
	}
}

func (c *Client) connect() error {
	var lastErr error
	for _, endpoint := range splitServerEndpoints(c.serverURL) {
		kind, normalized, err := normalizeServerEndpoint(endpoint)
		if err != nil {
			lastErr = err
			continue
		}
		if kind == "ws" {
			if err := c.connectWebSocket(normalized); err != nil {
				lastErr = err
				continue
			}
			return nil
		}
		if err := c.connectStream(kind, normalized); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("no server endpoints")
}

func (c *Client) connectWebSocket(serverURL string) error {
	wsURL, err := resolveWebSocketURL(serverURL, 5)
	if err != nil {
		return err
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

	if u, uerr := url.Parse(wsURL); uerr == nil {
		u.Path, u.RawQuery, u.Fragment = "", "", ""
		c.mu.Lock()
		c.activeWSBase = u.String()
		c.mu.Unlock()
	}

	go socket.ReadLoop()
	go c.pingLoop(socket)
	return nil
}

func (c *Client) connectStream(kind, endpoint string) error {
	addr := strings.TrimPrefix(strings.TrimPrefix(endpoint, "tcp://"), "kcp://")
	addr = strings.TrimPrefix(addr, "udp://")
	if strings.Contains(addr, "/") {
		addr = strings.SplitN(addr, "/", 2)[0]
	}
	var conn net.Conn
	var err error
	if kind == "kcp" {
		k, e := kcp.DialWithOptions(addr, nil, 0, 0)
		if e != nil {
			return fmt.Errorf("dial kcp: %w", e)
		}
		k.SetNoDelay(1, 20, 2, 1)
		k.SetWindowSize(256, 256)
		k.SetMtu(1200)
		k.SetStreamMode(true)
		k.SetWriteDelay(false)
		conn = k
	} else {
		conn, err = net.DialTimeout("tcp", addr, 10*time.Second)
		if err != nil {
			return fmt.Errorf("dial tcp: %w", err)
		}
	}
	if c.identityErr != nil {
		conn.Close()
		return fmt.Errorf("identity credentials unavailable: %w", c.identityErr)
	}
	transport := &streamClientTransport{conn: conn}
	c.mu.Lock()
	c.transport = transport
	c.conn = nil
	c.activeWSBase = ""
	c.mu.Unlock()
	if err := c.send(c.registrationMessage()); err != nil {
		conn.Close()
		return err
	}
	log.Printf("connected to %s as '%s'", endpoint, c.requestedID)
	go c.streamReadLoop(conn, transport)
	go c.streamPingLoop(transport)
	return nil
}

func (c *Client) isCurrentTransport(t clientTransport) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.transport == t
}

func (c *Client) streamReadLoop(conn net.Conn, transport clientTransport) {
	defer func() {
		c.closeCurrentTransport(transport, conn)
	}()
	_ = conn.SetReadDeadline(time.Now().Add(clientReadWait))
	for {
		frame, err := tframe.ReadFrame(conn)
		if err != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(clientReadWait))
		if !c.isCurrentTransport(transport) {
			return
		}
		switch frame.Kind {
		case tframe.KindJSON:
			msg, err := protocol.Decode(frame.Payload)
			if err == nil {
				c.handleMessage(msg)
			}
		case tframe.KindBinary:
			(&wsEventHandler{client: c}).handleBinaryMessage(frame.Payload)
		case tframe.KindPing:
			_ = transport.(*streamClientTransport).write(tframe.KindPong, frame.Payload)
		case tframe.KindClose:
			return
		}
	}
}

func (c *Client) closeCurrentTransport(transport clientTransport, conn net.Conn) {
	c.mu.Lock()
	if c.transport != transport {
		c.mu.Unlock()
		return
	}
	uploads, desktops := c.teardownState()
	c.mu.Unlock()
	c.finishTeardown(uploads, desktops)
	conn.Close()
	c.notifyDisconnect()
}

// teardownState closes and forgets every connection-scoped resource. Callers
// hold c.mu and have confirmed that the connection being torn down is still
// the current one. The returned snapshots are finished by finishTeardown after
// c.mu is released, because closing them can block on their own locks.
//
// Lock order: c.mu -> c.uploadMu (snapshotUploads). Nothing takes c.mu while
// holding uploadMu or writeMu, so this order stays acyclic.
func (c *Client) teardownState() (uploads []*managedUpload, desktops map[string]*desktopSession) {
	for sid, sess := range c.sessions {
		sess.close()
		delete(c.sessions, sid)
	}
	for id, pending := range c.tcpDials {
		pending.cancel()
		delete(c.tcpDials, id)
	}
	for fid, tcpConn := range c.forwards {
		tcpConn.Close()
		delete(c.forwards, fid)
	}
	for id, fs := range c.fileStreams {
		fs.file.Close()
		delete(c.fileStreams, id)
	}
	uploads = c.snapshotUploads()
	for tid, cancel := range c.downloads {
		close(cancel)
		delete(c.downloads, tid)
	}
	desktops = c.desktopSessions
	c.desktopSessions = make(map[string]*desktopSession)
	c.conn = nil
	c.transport = nil
	return uploads, desktops
}

// finishTeardown releases the resources captured by teardownState; it must run
// without c.mu held.
func (c *Client) finishTeardown(uploads []*managedUpload, desktops map[string]*desktopSession) {
	for _, session := range desktops {
		session.close()
	}
	c.closeFileRTCSessions()
	for _, upload := range uploads {
		c.cancelManagedUpload(upload)
	}
}

// notifyDisconnect wakes Run without blocking the caller.
func (c *Client) notifyDisconnect() {
	select {
	case c.done <- struct{}{}:
	default:
	}
}

func (c *Client) handleMessage(msg *protocol.Message) {
	switch msg.Type {
	case protocol.MsgRegister:
		if msg.ClientID != "" {
			oldID := c.clientID
			c.clientID = msg.ClientID
			if oldID != "" && oldID != msg.ClientID {
				log.Printf("server assigned device ID %q for requested ID %q", msg.ClientID, c.requestedID)
			}
		}
		c.sshPort = msg.SSHPort
		c.httpHost = msg.HTTPHost
		c.tunnelBase = msg.TunnelBase
		c.reconnectBackoffReset()
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
	case protocol.MsgFileCreate, protocol.MsgFileDelete, protocol.MsgFileRename:
		go func() { response := mutateFileEntry(msg); c.fileListings.invalidate(); c.send(response) }()
	case protocol.MsgFileMkdir:
		go c.handleFileMkdir(msg)
	case protocol.MsgFileListRequest:
		go c.handleFileListRequest(msg)
	case protocol.MsgFileUploadStart:
		c.startFileTransfer(msg)
	case protocol.MsgFileUploadEnd:
		go c.handleManagedUploadEnd(msg)
	case protocol.MsgFileDownloadStart:
		c.startFileTransfer(msg)
	case protocol.MsgFileRTCSignal:
		c.handleFileRTCSignal(msg)
	case protocol.MsgFileTransferCancel:
		c.handleManagedTransferCancel(msg.TaskID)
	case protocol.MsgDesktopStart:
		c.handleDesktopStart(msg)
	case protocol.MsgDesktopRTCSignal:
		c.handleDesktopRTCSignal(msg)
	case protocol.MsgDesktopSnapshot:
		c.handleDesktopSnapshot(msg)
	case protocol.MsgDesktopInput:
		c.handleDesktopInput(msg)
	case protocol.MsgDesktopCua:
		go c.handleDesktopCua(msg)
	case protocol.MsgDesktopClipboard:
		c.handleDesktopClipboard(msg)
	case protocol.MsgDesktopClose:
		c.handleDesktopClose(msg.SessionID)
	}
}

func (c *Client) sendBinaryOffset(typ byte, id string, offset int64, data []byte) error {
	frame := protocol.EncodeBinFrameOffset(typ, id, offset, data)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.transport == nil {
		return fmt.Errorf("not connected")
	}
	return c.transport.WriteBinary(frame)
}

// --- WebSocket send helpers ---

func (c *Client) send(msg *protocol.Message) error {
	c.mu.Lock()
	transport := c.transport
	c.mu.Unlock()
	if transport == nil {
		return fmt.Errorf("not connected")
	}
	data, err := protocol.Encode(msg)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return transport.WriteJSON(data)
}

func (c *Client) sendBinary(typ byte, id string, data []byte) error {
	c.mu.Lock()
	transport := c.transport
	c.mu.Unlock()
	if transport == nil {
		return fmt.Errorf("not connected")
	}
	frame := protocol.EncodeBinFrame(typ, id, data)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return transport.WriteBinary(frame)
}

func (c *Client) pingLoop(conn *gws.Conn) {
	ticker := time.NewTicker(clientPingPeriod)
	defer ticker.Stop()
	for range ticker.C {
		if !c.isCurrentConn(conn) {
			return
		}
		c.writeMu.Lock()
		_ = conn.SetWriteDeadline(time.Now().Add(clientWriteWait))
		err := conn.WritePing(nil)
		_ = conn.SetWriteDeadline(time.Time{})
		c.writeMu.Unlock()
		if err != nil {
			_ = conn.WriteClose(1001, []byte("ping failed"))
			return
		}
	}
}

func (c *Client) streamPingLoop(transport clientTransport) {
	ticker := time.NewTicker(clientPingPeriod)
	defer ticker.Stop()
	for range ticker.C {
		if !c.isCurrentTransport(transport) {
			return
		}
		c.writeMu.Lock()
		err := transport.WritePing(nil)
		c.writeMu.Unlock()
		if err != nil {
			_ = transport.Close("ping failed")
			return
		}
	}
}

func (c *Client) reconnectBackoffReset() {
	select {
	case c.reconnectReset <- struct{}{}:
	default:
	}
}

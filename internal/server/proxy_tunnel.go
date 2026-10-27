package server

import (
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/lxzan/gws"
)

// proxyTunnelPending is a one-shot dial-back slot: tcp_connect carries the
// path+token, the device opens a dedicated WebSocket for the stream, and the
// waiting proxy handler receives it as a raw net.Conn. Entries are single-use
// and expire with the dial timeout.
type proxyTunnelPending struct {
	clientID string
	token    string
	ready    chan net.Conn // buffered (1)
	expires  time.Time
}

// registerProxyTunnel reserves a dial-back slot for forwardID and returns the
// URL path to embed in tcp_connect plus the channel delivering the tunnel.
func (s *Server) registerProxyTunnel(client *ClientConn, forwardID string) (string, <-chan net.Conn) {
	token := generateID()
	pending := &proxyTunnelPending{
		clientID: client.ID,
		token:    token,
		ready:    make(chan net.Conn, 1),
		expires:  time.Now().Add(proxyDialTimeout),
	}
	s.proxyTunnelsMu.Lock()
	s.proxyTunnels[forwardID] = pending
	s.proxyTunnelsMu.Unlock()
	return "/tcp-tunnel?id=" + forwardID + "&token=" + token, pending.ready
}

// popProxyTunnel removes and returns the pending slot if the token matches.
// Popping makes each slot single-use: a late or replayed dial-back is rejected.
func (s *Server) popProxyTunnel(forwardID, token string) *proxyTunnelPending {
	s.proxyTunnelsMu.Lock()
	defer s.proxyTunnelsMu.Unlock()
	pending, ok := s.proxyTunnels[forwardID]
	if !ok || time.Now().After(pending.expires) || !constantTimeEqual(pending.token, token) {
		delete(s.proxyTunnels, forwardID)
		return nil
	}
	delete(s.proxyTunnels, forwardID)
	return pending
}

func (s *Server) removeProxyTunnel(forwardID string) {
	s.proxyTunnelsMu.Lock()
	delete(s.proxyTunnels, forwardID)
	s.proxyTunnelsMu.Unlock()
}

// HandleTCPTunnel accepts device dial-back WebSockets carrying one raw
// proxied stream each. The per-forward token is the only credential: it is
// unguessable, single-use and short-lived, so no admin token is required.
func (s *Server) HandleTCPTunnel(w http.ResponseWriter, r *http.Request) {
	pending := s.popProxyTunnel(r.URL.Query().Get("id"), r.URL.Query().Get("token"))
	if pending == nil {
		http.Error(w, "unknown tunnel", http.StatusNotFound)
		return
	}
	handler := &tcpTunnelHandler{ready: pending.ready}
	upgrader := gws.NewUpgrader(handler, &gws.ServerOption{
		ReadMaxPayloadSize: 16 * 1024 * 1024,
		CheckUtf8Enabled:   false,
	})
	socket, err := upgrader.Upgrade(w, r)
	if err != nil {
		return
	}
	socket.ReadLoop()
}

type tcpTunnelHandler struct {
	gws.BuiltinEventHandler
	ready chan net.Conn
	conn  wsTunnelConn
}

func (h *tcpTunnelHandler) OnOpen(socket *gws.Conn) {
	h.conn.socket = socket
	h.conn.readCh = make(chan []byte, 64)
	h.conn.done = make(chan struct{})
	h.conn.remoteAddr = socket.RemoteAddr()
	h.conn.localAddr = socket.LocalAddr()
	select {
	case h.ready <- &h.conn:
	default:
		// Waiter already gone (timeout/legacy path won): drop the tunnel.
		_ = socket.WriteClose(1000, nil)
	}
}

func (h *tcpTunnelHandler) OnMessage(socket *gws.Conn, message *gws.Message) {
	payload := append([]byte(nil), message.Bytes()...)
	message.Close()
	if message.Opcode != gws.OpcodeBinary || len(payload) == 0 {
		return
	}
	h.conn.deliver(payload)
}

func (h *tcpTunnelHandler) OnClose(socket *gws.Conn, err error) {
	h.conn.signalClose()
}

// wsTunnelConn adapts a dial-back WebSocket to net.Conn for raw stream
// splicing. Data frames carry stream bytes verbatim; close maps to EOF.
type wsTunnelConn struct {
	socket     *gws.Conn
	readCh     chan []byte
	done       chan struct{}
	closeOnce  sync.Once
	writeMu    sync.Mutex
	cur        []byte
	remoteAddr net.Addr
	localAddr  net.Addr
}

func (c *wsTunnelConn) deliver(p []byte) {
	select {
	case c.readCh <- p:
	case <-c.done:
	}
}

func (c *wsTunnelConn) signalClose() {
	c.closeOnce.Do(func() { close(c.done) })
}

func (c *wsTunnelConn) Read(p []byte) (int, error) {
	for len(c.cur) == 0 {
		select {
		case b := <-c.readCh:
			c.cur = b
		case <-c.done:
			// Drain frames buffered before the close, then report EOF.
			select {
			case b := <-c.readCh:
				c.cur = b
			default:
				return 0, io.EOF
			}
		}
	}
	n := copy(p, c.cur)
	c.cur = c.cur[n:]
	return n, nil
}

func (c *wsTunnelConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	select {
	case <-c.done:
		return 0, net.ErrClosed
	default:
	}
	if c.socket == nil {
		return 0, net.ErrClosed
	}
	_ = c.socket.SetWriteDeadline(time.Now().Add(wsWriteWait))
	defer c.socket.SetWriteDeadline(time.Time{})
	if err := c.socket.WriteMessage(gws.OpcodeBinary, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *wsTunnelConn) Close() error {
	c.signalClose()
	if c.socket != nil {
		_ = c.socket.WriteClose(1000, nil)
	}
	return nil
}

func (c *wsTunnelConn) LocalAddr() net.Addr  { return c.localAddr }
func (c *wsTunnelConn) RemoteAddr() net.Addr { return c.remoteAddr }

// Deadlines are managed by the proxied connection side; io.Copy never sets any.
func (c *wsTunnelConn) SetDeadline(time.Time) error      { return nil }
func (c *wsTunnelConn) SetReadDeadline(time.Time) error  { return nil }
func (c *wsTunnelConn) SetWriteDeadline(time.Time) error { return nil }

// joinProxyConns splices the proxy client connection and a dial-back tunnel
// with raw io.Copy in both directions. The first direction to finish closes
// both connections so the surviving peer sees a prompt EOF (close-delimited
// responses depend on it); it returns after both copy goroutines exit.
// upstream supplies the client->device direction (buffered bytes included).
func joinProxyConns(clientConn net.Conn, upstream io.Reader, tunnel net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(tunnel, upstream)
		_ = tunnel.Close()
		_ = clientConn.Close()
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(clientConn, tunnel)
		_ = tunnel.Close()
		_ = clientConn.Close()
	}()
	wg.Wait()
}

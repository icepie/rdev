package server

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"rdev/internal/protocol"
)

const (
	// proxyHandshakeTimeout covers protocol sniffing, auth and target
	// negotiation; it is cleared once the tunnel is established.
	proxyHandshakeTimeout = 15 * time.Second
	proxyDialTimeout      = 10 * time.Second
)

type proxyServer struct {
	srv *Server
}

// StartProxyServer exposes connected RDev devices as HTTP and SOCKS5 proxies on
// a single port. The first byte selects the protocol (0x05 = SOCKS5, otherwise
// HTTP). Device selection uses proxy authentication: username=deviceId and
// password=device password, or the combined form username="deviceId@password".
// Devices without a password accept any password but still require the device
// ID as username.
func StartProxyServer(srv *Server, addr string) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	ps := &proxyServer{srv: srv}
	go ps.serve(ln)
	return ln, nil
}

func (p *proxyServer) serve(ln net.Listener) {
	log.Printf("proxy server (HTTP/SOCKS5) listening on %s", ln.Addr())
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("proxy accept error: %v", err)
			continue
		}
		pc := &proxyConn{srv: p.srv, conn: conn, reader: bufio.NewReader(conn)}
		go pc.run()
	}
}

type proxyConn struct {
	srv    *Server
	conn   net.Conn
	reader *bufio.Reader
}

func (c *proxyConn) run() {
	defer c.conn.Close()
	_ = c.conn.SetDeadline(time.Now().Add(proxyHandshakeTimeout))
	first, err := c.reader.Peek(1)
	if err != nil {
		return
	}
	if first[0] == 0x05 {
		c.handleSOCKS5()
		return
	}
	c.handleHTTP()
}

// --- auth ---

// authenticateProxyDevice resolves proxy credentials to a connected device.
// The username selects the device; it may also embed the password in the
// combined form "deviceID@password" (device IDs cannot contain '@', so the
// first '@' splits the two). Open devices (no password) accept any password.
// Failures for password-protected devices are throttled per device.
func (s *Server) authenticateProxyDevice(username, password string) (*ClientConn, bool) {
	deviceID := username
	if i := strings.IndexByte(username, '@'); i >= 0 {
		deviceID, password = username[:i], username[i+1:]
	}
	if !validClientID(deviceID) {
		return nil, false
	}
	client, ok := s.GetClient(deviceID)
	if !ok {
		return nil, false
	}
	if client.Password == "" {
		return client, true
	}
	// Brute-force throttle: reject during cooldown without comparing.
	if s.authLimiter.wait(deviceID) > 0 {
		return nil, false
	}
	// Constant-time compare of fingerprints: do not leak password prefix.
	if constantTimeEqual(passwordFingerprint(password), passwordFingerprint(client.Password)) {
		s.authLimiter.success(deviceID)
		return client, true
	}
	s.authLimiter.fail(deviceID)
	return nil, false
}

// --- dial through the device (shared by HTTP CONNECT and SOCKS5 CONNECT) ---

// dialViaDevice asks the device to dial host:port (tcp_connect, the same flow
// as SSH -L) and waits for the connection to be established. The request also
// carries a one-shot dial-back path: clients that support it open a dedicated
// raw WebSocket per stream (fast path, returned as tunnel); older clients
// answer with framed tcp_open and stream over the control channel (fwd).
// Exactly one of fwd/tunnel carries the stream; fwd stays registered in both
// cases for limit accounting and must be released with releaseProxyForward.
func (s *Server) dialViaDevice(client *ClientConn, host string, port int) (fwd *ProxyForward, tunnel net.Conn, err error) {
	forwardID := generateID()
	fwd = &ProxyForward{
		ID:       forwardID,
		ClientID: client.ID,
		WriteCh:  make(chan []byte, 16384),
		CloseCh:  make(chan struct{}, 1),
		OpenCh:   make(chan struct{}),
		Done:     make(chan struct{}),
	}
	if !s.RegisterForward(fwd, client) {
		return nil, nil, errors.New("too many active forwards")
	}
	tunnelPath, tunnelReady := s.registerProxyTunnel(client, forwardID)
	if err := client.Send(&protocol.Message{
		Type:      protocol.MsgTCPConnect,
		ClientID:  client.ID,
		ForwardID: forwardID,
		Host:      host,
		Port:      port,
		Tunnel:    tunnelPath,
	}); err != nil {
		s.releaseProxyForward(client, fwd)
		return nil, nil, err
	}
	select {
	case <-fwd.OpenCh:
		// Framed path (legacy client or dial-back fallback).
		s.removeProxyTunnel(forwardID)
		if msg := fwd.FailError(); msg != "" {
			s.releaseProxyForward(client, fwd)
			return nil, nil, errors.New(msg)
		}
		return fwd, nil, nil
	case conn := <-tunnelReady:
		// Fast path: dedicated raw WebSocket stream.
		return fwd, conn, nil
	case <-time.After(proxyDialTimeout):
		client.Send(&protocol.Message{Type: protocol.MsgTCPClose, ForwardID: forwardID})
		s.releaseProxyForward(client, fwd)
		return nil, nil, errors.New("device TCP connect timeout")
	}
}

// releaseProxyForward drops the forward slot and any pending dial-back slot.
func (s *Server) releaseProxyForward(client *ClientConn, fwd *ProxyForward) {
	s.removeForward(fwd.ID)
	s.removeProxyTunnel(fwd.ID)
	client.mu.Lock()
	delete(client.Forwards, fwd.ID)
	client.mu.Unlock()
}

// relayProxyTunnel bridges the proxy client connection and the device-side TCP
// connection until either side closes. upstream supplies the bytes forwarded
// towards the device (typically the buffered connection reader, optionally
// prefixed with a rewritten HTTP request).
func (s *Server) relayProxyTunnel(client *ClientConn, fwd *ProxyForward, conn net.Conn, upstream io.Reader) {
	done := make(chan struct{})
	defer close(done)
	defer s.releaseProxyForward(client, fwd)

	go func() {
		select {
		case <-fwd.CloseCh:
			fwd.CloseOutput()
		case <-done:
		}
	}()

	// Proxy client -> device (binary frames).
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := upstream.Read(buf)
			if n > 0 {
				client.SendBinary(protocol.BinTCPData, fwd.ID, buf[:n])
			}
			if err != nil {
				client.Send(&protocol.Message{Type: protocol.MsgTCPClose, ForwardID: fwd.ID})
				return
			}
		}
	}()

	// Device -> proxy client.
	for data := range fwd.WriteCh {
		if _, err := conn.Write(data); err != nil {
			return
		}
	}
}

// --- HTTP proxy ---

func (c *proxyConn) handleHTTP() {
	req, err := http.ReadRequest(c.reader)
	if err != nil {
		return
	}
	username, password, ok := parseProxyAuth(req.Header.Get("Proxy-Authorization"))
	if !ok {
		writeProxyAuthRequired(c.conn)
		return
	}
	client, ok := c.srv.authenticateProxyDevice(username, password)
	if !ok {
		writeProxyAuthRequired(c.conn)
		return
	}

	if req.Method == http.MethodConnect {
		host, port, err := proxyTargetHostPort(requestAuthority(req), 443)
		if err != nil {
			writeProxyError(c.conn, http.StatusBadRequest, err.Error())
			return
		}
		log.Printf("proxy CONNECT: %s -> %s:%d", client.ID, host, port)
		fwd, tunnel, err := c.srv.dialViaDevice(client, host, port)
		if err != nil {
			writeProxyError(c.conn, http.StatusBadGateway, err.Error())
			return
		}
		if _, err := io.WriteString(c.conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			c.srv.releaseProxyForward(client, fwd)
			if tunnel != nil {
				tunnel.Close()
			}
			return
		}
		_ = c.conn.SetDeadline(time.Time{})
		if tunnel != nil {
			defer c.srv.releaseProxyForward(client, fwd)
			joinProxyConns(c.conn, c.reader, tunnel)
			return
		}
		c.srv.relayProxyTunnel(client, fwd, c.conn, c.reader)
		return
	}

	// Plain HTTP request with absolute URI (e.g. GET http://host/path).
	host, port, err := proxyTargetHostPort(requestAuthority(req), defaultPortForScheme(req.URL.Scheme))
	if err != nil {
		writeProxyError(c.conn, http.StatusBadRequest, err.Error())
		return
	}
	log.Printf("proxy %s: %s -> %s:%d", req.Method, client.ID, host, port)
	fwd, tunnel, err := c.srv.dialViaDevice(client, host, port)
	if err != nil {
		writeProxyError(c.conn, http.StatusBadGateway, err.Error())
		return
	}

	// Rewrite into an origin-form request: strip proxy credentials before they
	// can leak to the origin and force close so one request maps to one tunnel.
	req.Header.Del("Proxy-Authorization")
	req.Header.Del("Proxy-Connection")
	req.RequestURI = ""
	req.Close = true

	// Serialize the (possibly large) request lazily into the tunnel; once the
	// body is written, forward any remaining client bytes verbatim.
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(req.Write(pw))
	}()
	_ = c.conn.SetDeadline(time.Time{})
	if tunnel != nil {
		defer c.srv.releaseProxyForward(client, fwd)
		joinProxyConns(c.conn, io.MultiReader(pr, c.reader), tunnel)
		return
	}
	c.srv.relayProxyTunnel(client, fwd, c.conn, io.MultiReader(pr, c.reader))
}

// requestAuthority returns the host[:port] a proxy request targets.
func requestAuthority(req *http.Request) string {
	if req.URL != nil && req.URL.Host != "" {
		return req.URL.Host
	}
	return req.Host
}

func defaultPortForScheme(scheme string) int {
	if scheme == "https" {
		return 443
	}
	return 80
}

// proxyTargetHostPort splits an authority into host and port, applying
// defaultPort when the authority carries no port.
func proxyTargetHostPort(authority string, defaultPort int) (string, int, error) {
	if authority == "" {
		return "", 0, errors.New("missing target host")
	}
	host, portText, err := net.SplitHostPort(authority)
	if err != nil {
		// No port (or a bare IPv6 literal): treat the whole authority as host.
		host = strings.Trim(authority, "[]")
		if host == "" {
			return "", 0, errors.New("missing target host")
		}
		return host, defaultPort, nil
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("invalid target port %q", portText)
	}
	return host, port, nil
}

// parseProxyAuth decodes a "Basic base64(user:pass)" Proxy-Authorization
// header.
func parseProxyAuth(header string) (string, string, bool) {
	const prefix = "basic "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", "", false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(header[len(prefix):]))
	if err != nil {
		return "", "", false
	}
	user, pass, ok := strings.Cut(string(raw), ":")
	return user, pass, ok
}

func writeProxyAuthRequired(conn net.Conn) {
	io.WriteString(conn, "HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic realm=\"rdev\"\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
}

func writeProxyError(conn net.Conn, status int, msg string) {
	fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\nContent-Type: text/plain\r\nConnection: close\r\nContent-Length: %d\r\n\r\n%s",
		status, http.StatusText(status), len(msg), msg)
}

// --- SOCKS5 (RFC 1928) with username/password auth (RFC 1929) ---

const (
	socksVersion5       = 0x05
	socksAuthUserPass   = 0x02
	socksCmdConnect     = 0x01
	socksAtypIPv4       = 0x01
	socksAtypDomain     = 0x03
	socksAtypIPv6       = 0x04
	socksRepSuccess     = 0x00
	socksRepRefused     = 0x05
	socksRepCmdNotSupp  = 0x07
	socksRepAtypNotSupp = 0x08
)

func (c *proxyConn) handleSOCKS5() {
	header := make([]byte, 2)
	if _, err := io.ReadFull(c.reader, header); err != nil {
		return
	}
	if header[0] != socksVersion5 {
		return
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(c.reader, methods); err != nil {
		return
	}
	// The username selects the exit device, so user/pass auth is mandatory.
	if !socksHasMethod(methods, socksAuthUserPass) {
		c.conn.Write([]byte{socksVersion5, 0xFF})
		return
	}
	if _, err := c.conn.Write([]byte{socksVersion5, socksAuthUserPass}); err != nil {
		return
	}

	username, password, ok := c.readSocksAuth()
	if !ok {
		return
	}
	client, ok := c.srv.authenticateProxyDevice(username, password)
	if !ok {
		// RFC 1929 failure status, then close.
		c.conn.Write([]byte{0x01, 0x01})
		return
	}
	if _, err := c.conn.Write([]byte{0x01, 0x00}); err != nil {
		return
	}

	host, port, replyCode, ok := c.readSocksRequest()
	if !ok {
		if replyCode != 0 {
			c.writeSocksReply(replyCode)
		}
		return
	}
	log.Printf("proxy SOCKS5: %s -> %s:%d", client.ID, host, port)
	fwd, tunnel, err := c.srv.dialViaDevice(client, host, port)
	if err != nil {
		c.writeSocksReply(socksRepRefused)
		return
	}
	if !c.writeSocksReply(socksRepSuccess) {
		c.srv.releaseProxyForward(client, fwd)
		if tunnel != nil {
			tunnel.Close()
		}
		return
	}
	_ = c.conn.SetDeadline(time.Time{})
	if tunnel != nil {
		defer c.srv.releaseProxyForward(client, fwd)
		joinProxyConns(c.conn, c.reader, tunnel)
		return
	}
	c.srv.relayProxyTunnel(client, fwd, c.conn, c.reader)
}

func socksHasMethod(methods []byte, want byte) bool {
	for _, m := range methods {
		if m == want {
			return true
		}
	}
	return false
}

// readSocksAuth parses the RFC 1929 username/password subnegotiation.
func (c *proxyConn) readSocksAuth() (string, string, bool) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(c.reader, header); err != nil || header[0] != 0x01 {
		return "", "", false
	}
	ulen := int(header[1])
	if ulen == 0 {
		return "", "", false
	}
	buf := make([]byte, ulen+1)
	if _, err := io.ReadFull(c.reader, buf); err != nil {
		return "", "", false
	}
	username := string(buf[:ulen])
	plen := int(buf[ulen])
	password := make([]byte, plen)
	if _, err := io.ReadFull(c.reader, password); err != nil {
		return "", "", false
	}
	return username, string(password), true
}

// readSocksRequest parses the CONNECT request. ok=false with replyCode!=0
// means the caller should send that error reply before giving up.
func (c *proxyConn) readSocksRequest() (string, int, byte, bool) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(c.reader, header); err != nil || header[0] != socksVersion5 {
		return "", 0, 0, false
	}
	if header[1] != socksCmdConnect {
		return "", 0, socksRepCmdNotSupp, false
	}
	var host string
	switch header[3] {
	case socksAtypIPv4:
		addr := make([]byte, 4)
		if _, err := io.ReadFull(c.reader, addr); err != nil {
			return "", 0, 0, false
		}
		host = net.IP(addr).String()
	case socksAtypIPv6:
		addr := make([]byte, 16)
		if _, err := io.ReadFull(c.reader, addr); err != nil {
			return "", 0, 0, false
		}
		host = net.IP(addr).String()
	case socksAtypDomain:
		lenBuf := make([]byte, 1)
		if _, err := io.ReadFull(c.reader, lenBuf); err != nil || lenBuf[0] == 0 {
			return "", 0, 0, false
		}
		domain := make([]byte, int(lenBuf[0]))
		if _, err := io.ReadFull(c.reader, domain); err != nil {
			return "", 0, 0, false
		}
		host = string(domain)
	default:
		return "", 0, socksRepAtypNotSupp, false
	}
	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(c.reader, portBuf); err != nil {
		return "", 0, 0, false
	}
	port := int(portBuf[0])<<8 | int(portBuf[1])
	if port == 0 {
		return "", 0, socksRepRefused, false
	}
	return host, port, 0, true
}

func (c *proxyConn) writeSocksReply(rep byte) bool {
	// BND.ADDR/BND.PORT are informational; 0.0.0.0:0 is fine for CONNECT.
	_, err := c.conn.Write([]byte{socksVersion5, rep, 0x00, socksAtypIPv4, 0, 0, 0, 0, 0, 0})
	return err == nil
}

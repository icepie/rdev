package server

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lxzan/gws"
	"rdev/internal/protocol"
)

// fakeDeviceTransport records everything the server sends to the device and
// optionally auto-answers tcp_connect with tcp_open, simulating a connected
// device without any real network.
type fakeDeviceTransport struct {
	mu     sync.Mutex
	msgs   []*protocol.Message
	binary [][]byte // raw payloads of BinTCPData frames
	onSend func(msg *protocol.Message)
}

func (t *fakeDeviceTransport) WriteJSON(data []byte) error {
	msg, err := protocol.Decode(data)
	if err != nil {
		return err
	}
	t.mu.Lock()
	t.msgs = append(t.msgs, msg)
	t.mu.Unlock()
	if t.onSend != nil {
		t.onSend(msg)
	}
	return nil
}

func (t *fakeDeviceTransport) WriteBinary(data []byte) error {
	typ, _, payload, err := protocol.DecodeBinFrame(data)
	if err != nil {
		return err
	}
	if typ != protocol.BinTCPData {
		return nil
	}
	t.mu.Lock()
	t.binary = append(t.binary, append([]byte(nil), payload...))
	t.mu.Unlock()
	return nil
}

func (t *fakeDeviceTransport) WritePing([]byte) error { return nil }
func (t *fakeDeviceTransport) Close(string) error     { return nil }
func (t *fakeDeviceTransport) RemoteAddr() string     { return "fake" }

func (t *fakeDeviceTransport) lastMsg() *protocol.Message {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.msgs) == 0 {
		return nil
	}
	return t.msgs[len(t.msgs)-1]
}

func (t *fakeDeviceTransport) binaryBytes() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []byte
	for _, b := range t.binary {
		out = append(out, b...)
	}
	return out
}

// newProxyTestServer registers a fake device that answers every tcp_connect
// with an immediate tcp_open.
func newProxyTestServer(t *testing.T, deviceID, password string) (*Server, *ClientConn, *fakeDeviceTransport) {
	t.Helper()
	s := NewServer()
	transport := &fakeDeviceTransport{}
	client := &ClientConn{
		ID:        deviceID,
		Password:  password,
		Transport: transport,
		Sessions:  make(map[string]*ProxySession),
		Forwards:  make(map[string]*ProxyForward),
	}
	s.clients[deviceID] = client
	transport.onSend = func(msg *protocol.Message) {
		if msg.Type == protocol.MsgTCPConnect {
			go s.handleClientMessage(client, &protocol.Message{Type: protocol.MsgTCPOpen, ForwardID: msg.ForwardID})
		}
	}
	return s, client, transport
}

func proxyBasicAuth(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

func startTestProxy(t *testing.T, s *Server) string {
	t.Helper()
	ln, err := StartProxyServer(s, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("StartProxyServer: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().String()
}

func dialTestProxy(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	return conn
}

func TestAuthenticateProxyDevice(t *testing.T) {
	s := NewServer()
	s.clients["pc"] = &ClientConn{ID: "pc", Password: "secret"}
	s.clients["open"] = &ClientConn{ID: "open"}

	for _, tc := range []struct {
		name   string
		user   string
		pass   string
		wantID string
		wantOK bool
	}{
		{"split fields", "pc", "secret", "pc", true},
		{"combined user@pass", "pc@secret", "", "pc", true},
		{"combined keeps @ in password", "pc@sec@ret", "", "pc", false}, // password would be "sec@ret"
		{"wrong password", "pc", "nope", "", false},
		{"combined wrong password", "pc@nope", "", "", false},
		{"unknown device", "ghost", "secret", "", false},
		{"open device any password", "open", "", "open", true},
		{"open device with password", "open", "anything", "open", true},
		{"invalid device id", "bad id", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, ok := s.authenticateProxyDevice(tc.user, tc.pass)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && client.ID != tc.wantID {
				t.Fatalf("device = %q, want %q", client.ID, tc.wantID)
			}
		})
	}
}

// socks5Handshake drives a full SOCKS5 user/pass CONNECT handshake and returns
// the server reply code.
func socks5Handshake(t *testing.T, conn net.Conn, user, pass, host string, port int) byte {
	t.Helper()
	if _, err := conn.Write([]byte{0x05, 0x01, 0x02}); err != nil {
		t.Fatalf("write greeting: %v", err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("read method reply: %v", err)
	}
	if reply[0] != 0x05 || reply[1] != 0x02 {
		t.Fatalf("method reply = %v, want [5 2]", reply)
	}

	auth := []byte{0x01, byte(len(user))}
	auth = append(auth, user...)
	auth = append(auth, byte(len(pass)))
	auth = append(auth, pass...)
	if _, err := conn.Write(auth); err != nil {
		t.Fatalf("write auth: %v", err)
	}
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("read auth reply: %v", err)
	}
	if reply[1] != 0x00 {
		t.Fatalf("auth status = %d, want 0", reply[1])
	}

	req := []byte{0x05, 0x01, 0x00, 0x03, byte(len(host))}
	req = append(req, host...)
	req = append(req, byte(port>>8), byte(port))
	if _, err := conn.Write(req); err != nil {
		t.Fatalf("write request: %v", err)
	}
	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		t.Fatalf("read connect reply: %v", err)
	}
	if head[0] != 0x05 {
		t.Fatalf("reply version = %d, want 5", head[0])
	}
	// Drain the rest of the reply (ATYP + BND.ADDR + BND.PORT).
	rest := make([]byte, 6)
	if _, err := io.ReadFull(conn, rest); err != nil {
		t.Fatalf("read reply tail: %v", err)
	}
	return head[1]
}

func TestSOCKS5ProxyEndToEnd(t *testing.T) {
	s, client, transport := newProxyTestServer(t, "pc", "secret")
	addr := startTestProxy(t, s)
	conn := dialTestProxy(t, addr)

	if rep := socks5Handshake(t, conn, "pc", "secret", "example.com", 443); rep != 0x00 {
		t.Fatalf("connect reply = %#x, want success", rep)
	}

	connectMsg := transport.lastMsg()
	if connectMsg == nil || connectMsg.Type != protocol.MsgTCPConnect {
		t.Fatalf("device did not receive tcp_connect, got %#v", connectMsg)
	}
	if connectMsg.Host != "example.com" || connectMsg.Port != 443 {
		t.Fatalf("target = %s:%d, want example.com:443", connectMsg.Host, connectMsg.Port)
	}

	// Proxy client -> device.
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write ping: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(string(transport.binaryBytes()), "ping") {
		if time.Now().After(deadline) {
			t.Fatalf("device did not receive tunneled data, got %q", transport.binaryBytes())
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Device -> proxy client.
	fwd := s.getForward(connectMsg.ForwardID)
	if fwd == nil {
		t.Fatal("forward not registered")
	}
	fwd.WriteCh <- []byte("pong")
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read pong: %v", err)
	}
	if string(buf) != "pong" {
		t.Fatalf("got %q, want pong", buf)
	}

	// Device close must terminate the tunnel.
	s.handleClientMessage(client, &protocol.Message{Type: protocol.MsgTCPClose, ForwardID: connectMsg.ForwardID})
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("expected connection to close after device tcp_close")
	}
}

func TestSOCKS5RejectsBadPassword(t *testing.T) {
	s, _, _ := newProxyTestServer(t, "pc", "secret")
	addr := startTestProxy(t, s)
	conn := dialTestProxy(t, addr)

	if _, err := conn.Write([]byte{0x05, 0x01, 0x02}); err != nil {
		t.Fatalf("write greeting: %v", err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("read method reply: %v", err)
	}
	auth := append([]byte{0x01, 2, 'p', 'c', 5}, []byte("wrong")...)
	if _, err := conn.Write(auth); err != nil {
		t.Fatalf("write auth: %v", err)
	}
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("read auth reply: %v", err)
	}
	if reply[1] != 0x01 {
		t.Fatalf("auth status = %d, want failure 1", reply[1])
	}
}

func TestHTTPConnectProxyEndToEnd(t *testing.T) {
	s, _, transport := newProxyTestServer(t, "pc", "secret")
	addr := startTestProxy(t, s)
	conn := dialTestProxy(t, addr)

	// Combined "deviceID@password" username form.
	fmt.Fprintf(conn, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\nProxy-Authorization: %s\r\n\r\n", proxyBasicAuth("pc@secret", ""))
	reader := bufio.NewReader(conn)
	status, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read connect response: %v", err)
	}
	if !strings.Contains(status, "200") {
		t.Fatalf("CONNECT status = %q, want 200", status)
	}
	// Consume the rest of the response header block.
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatalf("read header terminator: %v", err)
	}

	connectMsg := transport.lastMsg()
	if connectMsg == nil || connectMsg.Host != "example.com" || connectMsg.Port != 443 {
		t.Fatalf("tcp_connect = %#v, want example.com:443", connectMsg)
	}

	if _, err := conn.Write([]byte("tls-client-hello")); err != nil {
		t.Fatalf("write tunneled data: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(string(transport.binaryBytes()), "tls-client-hello") {
		if time.Now().After(deadline) {
			t.Fatalf("device did not receive tunneled CONNECT data")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestHTTPPlainProxyRewritesRequest(t *testing.T) {
	s, _, transport := newProxyTestServer(t, "pc", "secret")
	addr := startTestProxy(t, s)
	conn := dialTestProxy(t, addr)

	fmt.Fprintf(conn, "GET http://example.com/path?q=1 HTTP/1.1\r\nHost: example.com\r\nProxy-Authorization: %s\r\n\r\n", proxyBasicAuth("pc", "secret"))

	deadline := time.Now().Add(2 * time.Second)
	var sent string
	for {
		sent = string(transport.binaryBytes())
		if strings.Contains(sent, "\r\n\r\n") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("device did not receive rewritten request, got %q", sent)
		}
		time.Sleep(5 * time.Millisecond)
	}

	if !strings.HasPrefix(sent, "GET /path?q=1 HTTP/1.1\r\n") {
		t.Fatalf("request line = %q, want origin form", sent)
	}
	if strings.Contains(strings.ToLower(sent), "proxy-authorization") {
		t.Fatal("proxy credentials leaked towards origin")
	}
	if !strings.Contains(strings.ToLower(sent), "connection: close") {
		t.Fatalf("request should force Connection: close, got %q", sent)
	}

	// Origin response flows back to the proxy client.
	connectMsg := transport.lastMsg()
	fwd := s.getForward(connectMsg.ForwardID)
	if fwd == nil {
		t.Fatal("forward not registered")
	}
	response := "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nhi"
	fwd.WriteCh <- []byte(response)
	body := make([]byte, len(response))
	if _, err := io.ReadFull(conn, body); err != nil {
		t.Fatalf("read response: %v", err)
	}
	if string(body) != response {
		t.Fatalf("response = %q, want %q", body, response)
	}
}

func TestHTTPProxyRequiresAuth(t *testing.T) {
	s, _, _ := newProxyTestServer(t, "pc", "secret")
	addr := startTestProxy(t, s)

	for _, tc := range []struct {
		name   string
		header string
	}{
		{"missing", ""},
		{"wrong password", proxyBasicAuth("pc", "nope")},
		{"unknown device", proxyBasicAuth("ghost", "secret")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := dialTestProxy(t, addr)
			io.WriteString(conn, "GET http://example.com/ HTTP/1.1\r\nHost: example.com\r\n")
			if tc.header != "" {
				fmt.Fprintf(conn, "Proxy-Authorization: %s\r\n", tc.header)
			}
			io.WriteString(conn, "\r\n")
			line, err := bufio.NewReader(conn).ReadString('\n')
			if err != nil {
				t.Fatalf("read response: %v", err)
			}
			if !strings.Contains(line, "407") {
				t.Fatalf("status = %q, want 407", line)
			}
		})
	}
}

func TestSOCKS5UnsupportedCommand(t *testing.T) {
	s, _, _ := newProxyTestServer(t, "open", "")
	addr := startTestProxy(t, s)
	conn := dialTestProxy(t, addr)

	if _, err := conn.Write([]byte{0x05, 0x01, 0x02}); err != nil {
		t.Fatalf("write greeting: %v", err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("read method reply: %v", err)
	}
	if _, err := conn.Write([]byte{0x01, 4, 'o', 'p', 'e', 'n', 0}); err != nil {
		t.Fatalf("write auth: %v", err)
	}
	if _, err := io.ReadFull(conn, reply); err != nil || reply[1] != 0 {
		t.Fatalf("auth reply = %v, %v", reply, err)
	}
	// BIND (0x02) must be rejected with "command not supported".
	host := "example.com"
	req := append([]byte{0x05, 0x02, 0x00, 0x03, byte(len(host))}, host...)
	req = append(req, 0, 80)
	if _, err := conn.Write(req); err != nil {
		t.Fatalf("write request: %v", err)
	}
	full := make([]byte, 10)
	if _, err := io.ReadFull(conn, full); err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if full[1] != 0x07 {
		t.Fatalf("reply = %#x, want 0x07 command not supported", full[1])
	}
}

// testTunnelWSHandler pumps dial-back WS frames into the fake device's target.
type testTunnelWSHandler struct {
	gws.BuiltinEventHandler
	target net.Conn
}

func (h *testTunnelWSHandler) OnMessage(socket *gws.Conn, message *gws.Message) {
	defer message.Close()
	if message.Opcode == gws.OpcodeBinary {
		_, _ = h.target.Write(message.Bytes())
	}
}

func (h *testTunnelWSHandler) OnClose(socket *gws.Conn, err error) {
	_ = h.target.Close()
}

// TestProxyDialBackTunnelEndToEnd exercises the fast path: the device answers
// tcp_connect by dialing back /tcp-tunnel, and stream bytes must flow raw
// (no framed control-channel traffic) in both directions.
func TestProxyDialBackTunnelEndToEnd(t *testing.T) {
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()
	go func() {
		for {
			c, err := echoLn.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); c.Close() }()
		}
	}()
	_, echoPortStr, _ := net.SplitHostPort(echoLn.Addr().String())
	echoPort, _ := strconv.Atoi(echoPortStr)

	srv, client, transport := newProxyTestServer(t, "pc", "secret")
	tunnelSrv := httptest.NewServer(http.HandlerFunc(srv.HandleTCPTunnel))
	t.Cleanup(tunnelSrv.Close)

	// Fake device: dial back the tunnel WS instead of framed tcp_open.
	transport.onSend = func(msg *protocol.Message) {
		if msg.Type != protocol.MsgTCPConnect || msg.Tunnel == "" {
			return
		}
		go func() {
			target, err := net.DialTimeout("tcp", echoLn.Addr().String(), 2*time.Second)
			if err != nil {
				srv.handleClientMessage(client, &protocol.Message{
					Type: protocol.MsgTCPFail, ForwardID: msg.ForwardID, Error: err.Error()})
				return
			}
			wsURL := "ws" + strings.TrimPrefix(tunnelSrv.URL, "http") + msg.Tunnel
			socket, _, err := gws.NewClient(&testTunnelWSHandler{target: target}, &gws.ClientOption{Addr: wsURL})
			if err != nil {
				target.Close()
				return
			}
			go socket.ReadLoop()
			go func() {
				buf := make([]byte, 64*1024)
				for {
					n, err := target.Read(buf)
					if n > 0 {
						if werr := socket.WriteMessage(gws.OpcodeBinary, buf[:n]); werr != nil {
							break
						}
					}
					if err != nil {
						break
					}
				}
				_ = socket.WriteClose(1000, nil)
				target.Close()
			}()
		}()
	}

	addr := startTestProxy(t, srv)
	conn := dialTestProxy(t, addr)
	if rep := socks5Handshake(t, conn, "pc", "secret", "127.0.0.1", echoPort); rep != 0 {
		t.Fatalf("reply = %#x, want success", rep)
	}

	// Bidirectional data through the raw tunnel.
	payload := []byte("tunnel-fast-path")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := make([]byte, len(payload))
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("echo = %q, want %q", got, payload)
	}

	// Fast path must not have produced any framed control-channel traffic.
	if b := transport.binaryBytes(); len(b) != 0 {
		t.Fatalf("framed binary traffic on fast path: %d bytes", len(b))
	}
	transport.mu.Lock()
	for _, m := range transport.msgs {
		if m.Type == protocol.MsgTCPOpen {
			transport.mu.Unlock()
			t.Fatal("legacy tcp_open used on fast path")
		}
	}
	transport.mu.Unlock()

	conn.Close()
	eventually(t, 2*time.Second, func() bool {
		srv.fwdMu.RLock()
		defer srv.fwdMu.RUnlock()
		return len(srv.forwards) == 0
	}, "forward slot released after tunnel closed")
}

// eventually polls cond until it holds or the timeout expires.
func eventually(t *testing.T, d time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestProxyTunnelEndpointRejectsBadToken: the dial-back endpoint must reject
// unknown ids and wrong tokens without upgrading.
func TestProxyTunnelEndpointRejectsBadToken(t *testing.T) {
	s := NewServer()
	mux := http.HandlerFunc(s.HandleTCPTunnel)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	for _, url := range []string{
		ts.URL + "/tcp-tunnel?id=nope&token=nope",
		ts.URL + "/tcp-tunnel",
	} {
		resp, err := http.Get(url)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET %s: status = %d, want 404", url, resp.StatusCode)
		}
	}
}

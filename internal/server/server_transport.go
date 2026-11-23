package server

import (
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"rdev/internal/protocol"
	tframe "rdev/internal/transport"

	"github.com/lxzan/gws"
	kcp "github.com/xtaci/kcp-go/v5"
)

type DeviceTransport interface {
	WriteJSON([]byte) error
	WriteBinary([]byte) error
	WritePing([]byte) error
	Close(string) error
	RemoteAddr() string
}

type wsDeviceTransport struct{ conn *gws.Conn }

func (t *wsDeviceTransport) WriteJSON(data []byte) error {
	_ = t.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
	defer t.conn.SetWriteDeadline(time.Time{})
	return t.conn.WriteMessage(gws.OpcodeText, data)
}

func (t *wsDeviceTransport) WriteBinary(data []byte) error {
	_ = t.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
	defer t.conn.SetWriteDeadline(time.Time{})
	return t.conn.WriteMessage(gws.OpcodeBinary, data)
}

func (t *wsDeviceTransport) WritePing(payload []byte) error {
	_ = t.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
	defer t.conn.SetWriteDeadline(time.Time{})
	return t.conn.WritePing(payload)
}

func (t *wsDeviceTransport) Close(reason string) error {
	return t.conn.WriteClose(1000, []byte(reason))
}

func (t *wsDeviceTransport) RemoteAddr() string { return "" }

type streamDeviceTransport struct {
	conn net.Conn
	mu   sync.Mutex
}

func (t *streamDeviceTransport) write(kind byte, payload []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	_ = t.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
	defer t.conn.SetWriteDeadline(time.Time{})
	return tframe.WriteFrame(t.conn, kind, payload)
}

func (t *streamDeviceTransport) WriteJSON(data []byte) error { return t.write(tframe.KindJSON, data) }
func (t *streamDeviceTransport) WriteBinary(data []byte) error {
	return t.write(tframe.KindBinary, data)
}
func (t *streamDeviceTransport) WritePing(payload []byte) error {
	return t.write(tframe.KindPing, payload)
}
func (t *streamDeviceTransport) Close(reason string) error {
	_ = t.write(tframe.KindClose, []byte(reason))
	return t.conn.Close()
}
func (t *streamDeviceTransport) RemoteAddr() string { return t.conn.RemoteAddr().String() }

// Send sends a JSON control message to the client (text frame)
func (c *ClientConn) Send(msg *protocol.Message) error {
	data, err := protocol.Encode(msg)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.Transport == nil {
		return net.ErrClosed
	}
	return c.Transport.WriteJSON(data)
}

// SendBinary sends a binary data frame to the client
func (c *ClientConn) SendBinary(typ byte, id string, data []byte) error {
	frame := protocol.EncodeBinFrame(typ, id, data)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.Transport == nil {
		return net.ErrClosed
	}
	return c.Transport.WriteBinary(frame)
}

// SendFilePut sends a file to the client device (binary frame)
func (c *ClientConn) SendFilePut(id, path string, mode int32, fileData []byte) error {
	frame := protocol.EncodeBinFilePut(id, path, mode, fileData)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.Transport == nil {
		return net.ErrClosed
	}
	return c.Transport.WriteBinary(frame)
}

func (c *ClientConn) SendFileStart(id, path string, mode int32) error {
	frame := protocol.EncodeBinFileStart(id, path, mode)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.Transport == nil {
		return net.ErrClosed
	}
	return c.Transport.WriteBinary(frame)
}

func (c *ClientConn) SendFileChunk(id string, data []byte) error {
	frame := protocol.EncodeBinFrame(protocol.BinFileChunk, id, data)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.Transport == nil {
		return net.ErrClosed
	}
	return c.Transport.WriteBinary(frame)
}

func (c *ClientConn) SendFileEnd(id string) error {
	frame := protocol.EncodeBinFrame(protocol.BinFileEnd, id, nil)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.Transport == nil {
		return net.ErrClosed
	}
	return c.Transport.WriteBinary(frame)
}

func (c *ClientConn) SendBinaryOffset(typ byte, id string, offset int64, data []byte) error {
	frame := protocol.EncodeBinFrameOffset(typ, id, offset, data)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.Transport == nil {
		return net.ErrClosed
	}
	return c.Transport.WriteBinary(frame)
}

func (c *ClientConn) SendPing() error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.Transport == nil {
		return net.ErrClosed
	}
	return c.Transport.WritePing(nil)
}

// wsHandler implements gws.Event for server-side WebSocket connections
type wsHandler struct {
	gws.BuiltinEventHandler
	srv *Server
}

func closeClientResources(s *Server, client *ClientConn) {
	s.failClientFileRequests(client)
	client.mu.Lock()
	defer client.mu.Unlock()
	for sid, sess := range client.Sessions {
		sess.SignalClose()
		s.sessMu.Lock()
		delete(s.sessions, sid)
		s.sessMu.Unlock()
	}
	for fid, fwd := range client.Forwards {
		fwd.SignalClose()
		s.fwdMu.Lock()
		delete(s.forwards, fid)
		s.fwdMu.Unlock()
	}
	var cancels []func()
	s.revMu.Lock()
	for id, fwd := range s.revForwards {
		if fwd.ClientID == client.ID {
			if fwd.Cancel != nil {
				cancels = append(cancels, fwd.Cancel)
			}
			delete(s.revForwards, id)
		}
	}
	s.revMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	s.closeDesktopForClient(client.ID)
	s.closeGPUDesktopTunnelForClient(client.ID)
}

func (s *Server) clientByID(id string) *ClientConn {
	s.mu.RLock()
	client := s.clients[id]
	s.mu.RUnlock()
	return client
}

func (h *wsHandler) OnOpen(socket *gws.Conn) {
	_ = socket.SetDeadline(time.Now().Add(wsReadWait))
}

func (h *wsHandler) OnPing(socket *gws.Conn, payload []byte) {
	_ = socket.SetDeadline(time.Now().Add(wsReadWait))
	clientID, _ := socket.Session().Load("clientID")
	if clientID == nil {
		_ = socket.WritePong(payload)
		return
	}
	client := h.srv.clientByID(clientID.(string))
	if client == nil || client.Conn != socket {
		return
	}
	_ = socket.WritePong(payload)
}

func (h *wsHandler) OnPong(socket *gws.Conn, payload []byte) {
	_ = socket.SetDeadline(time.Now().Add(wsReadWait))
}

func (h *wsHandler) OnClose(socket *gws.Conn, err error) {
	clientID, _ := socket.Session().Load("clientID")
	if clientID == nil {
		return
	}
	id := clientID.(string)

	client, ok := h.srv.unregisterClient(id, socket)
	if ok {
		closeClientResources(h.srv, client)
		log.Printf("client unregistered: %s", id)
	}
}

func (h *wsHandler) OnMessage(socket *gws.Conn, message *gws.Message) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC in wsHandler.OnMessage: %v", r)
		}
	}()
	defer message.Close()

	// Binary frame = raw data message
	if message.Opcode == gws.OpcodeBinary {
		h.handleBinaryMessage(socket, message.Bytes())
		return
	}

	// Text frame = JSON control message
	msg, err := protocol.Decode(message.Bytes())
	if err != nil {
		return
	}

	// First message must be register
	if msg.Type == protocol.MsgRegister {
		h.handleRegister(socket, msg)
		return
	}

	clientID, _ := socket.Session().Load("clientID")
	if clientID == nil {
		return
	}

	h.srv.mu.RLock()
	client, ok := h.srv.clients[clientID.(string)]
	h.srv.mu.RUnlock()
	if !ok || client.Conn != socket {
		return
	}

	h.srv.handleClientMessage(client, msg)
}

func sendBytes(ch chan []byte, data []byte, label string) {
	select {
	case ch <- data:
	case <-time.After(30 * time.Second):
		log.Printf("dropping %s data after backpressure timeout", label)
	}
}

// handleBinaryMessage processes binary data frames from device clients
func (h *wsHandler) handleBinaryMessage(socket *gws.Conn, raw []byte) {
	clientID, _ := socket.Session().Load("clientID")
	if clientID == nil {
		return
	}

	h.srv.mu.RLock()
	client, ok := h.srv.clients[clientID.(string)]
	h.srv.mu.RUnlock()
	if !ok || client.Conn != socket {
		return
	}
	h.srv.handleClientBinary(client, raw)
}

func (s *Server) ServeTCP(addr string) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handleStreamDeviceConn(conn, "tcp")
		}
	}()
	return ln, nil
}

func (s *Server) ServeKCP(addr string) (*kcp.Listener, error) {
	ln, err := kcp.ListenWithOptions(addr, nil, 0, 0)
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			conn, err := ln.AcceptKCP()
			if err != nil {
				return
			}
			conn.SetNoDelay(1, 20, 2, 1)
			conn.SetWindowSize(256, 256)
			conn.SetMtu(1200)
			conn.SetStreamMode(true)
			conn.SetWriteDelay(false)
			go s.handleStreamDeviceConn(conn, "kcp")
		}
	}()
	return ln, nil
}

func (s *Server) handleStreamDeviceConn(conn net.Conn, label string) {
	transport := &streamDeviceTransport{conn: conn}
	var clientID string
	var registered bool
	defer func() {
		if registered {
			if client, ok := s.unregisterClientTransport(clientID, transport); ok {
				closeClientResources(s, client)
				log.Printf("client unregistered: %s", clientID)
			}
		}
		conn.Close()
	}()

	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		frame, err := tframe.ReadFrame(conn)
		if err != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(wsReadWait))
		switch frame.Kind {
		case tframe.KindJSON:
			msg, err := protocol.Decode(frame.Payload)
			if err != nil {
				continue
			}
			if !registered {
				if msg.Type == protocol.MsgGPUDesktopTunnel {
					s.handleGPUDesktopStreamTunnel(conn, msg)
					return
				}
				if msg.Type != protocol.MsgRegister {
					return
				}
				assigned, ok := s.registerStreamClient(transport, msg, label)
				if !ok {
					return
				}
				clientID = assigned
				registered = true
				continue
			}
			client := s.clientByID(clientID)
			if client == nil || client.Transport != transport {
				return
			}
			s.handleClientMessage(client, msg)
		case tframe.KindBinary:
			if !registered {
				continue
			}
			client := s.clientByID(clientID)
			if client == nil || client.Transport != transport {
				return
			}
			s.handleClientBinary(client, frame.Payload)
		case tframe.KindPing:
			_ = transport.write(tframe.KindPong, frame.Payload)
		case tframe.KindPong:
		case tframe.KindClose:
			return
		}
	}
}

func (s *Server) registerStreamClient(transport DeviceTransport, msg *protocol.Message, label string) (string, bool) {
	clientID := strings.TrimSpace(msg.ClientID)
	if !validClientID(clientID) {
		// Security: same ID whitelist as the WS register path.
		log.Printf("rejected registration with invalid client ID %q", clientID)
		_ = transport.Close("invalid client id")
		return "", false
	}
	identity := strings.TrimSpace(msg.DeviceIdentity)
	assigned, proof, _, authErr := s.authenticateRegistration(identity, clientID, msg.ReconnectProof)
	if authErr != nil {
		log.Printf("rejected stream registration identity=%q: %v", identity, authErr)
		_ = transport.Close("identity authentication failed")
		return "", false
	}
	clientID = assigned
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
		Transport:         transport,
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
	old, assignedID, duplicate := s.registerClient(client)
	if assignedID == "" {
		_ = transport.Close("identity registration rejected")
		return "", false
	}
	if old != nil && old.Transport != transport {
		log.Printf("client reconnected via %s: requested=%s assigned=%s", label, clientID, assignedID)
		closeClientResources(s, old)
		if old.Transport != nil {
			_ = old.Transport.Close("connection replaced")
		}
	} else if duplicate {
		log.Printf("client duplicate ID assigned via %s: requested=%s assigned=%s", label, clientID, assignedID)
	} else {
		log.Printf("client registered via %s: %s", label, assignedID)
	}
	_ = client.Send(&protocol.Message{Type: protocol.MsgRegister, ClientID: assignedID, InstanceID: instanceID, DeviceIdentity: identity, ReconnectProof: proof, SSHPort: s.SSHPort, HTTPHost: s.HTTPHost, TunnelBase: s.TunnelBase})
	go s.clientPingLoop(client)
	return assignedID, true
}

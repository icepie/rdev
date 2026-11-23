package server

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"rdev/internal/protocol"
)

type vncServer struct {
	srv  *Server
	addr string
}

type vncConn struct {
	srv      *Server
	conn     net.Conn
	reader   *bufio.Reader
	deviceID string
	client   *ClientConn
	session  string
	request  protocol.Message

	mu             sync.Mutex
	closed         bool
	readyCh        chan protocol.Message
	frameCh        chan []byte
	latestFrame    []byte
	frameWidth     int
	frameHeight    int
	lastSentFrame  []byte
	lastSentWidth  int
	lastSentHeight int
	supportsZRLE   bool
	buttons        uint8
	lastMouse      time.Time
	stream         *vncDesktopStream
}

type vncDesktopStream struct {
	srv         *Server
	client      *ClientConn
	deviceID    string
	session     string
	key         string
	request     protocol.Message
	mu          sync.Mutex
	subscribers map[*vncConn]struct{}
	ready       *protocol.Message
	latestFrame []byte
	frameWidth  int
	frameHeight int
	closed      bool
}

// StartVNCServer exposes connected RDev devices through the RFB/VNC protocol.
// Device selection uses modern VeNCrypt Plain username/password auth:
// username=deviceId, password=device password. For devices without a password,
// password may be empty or equal to the device id; if username is empty,
// password may be the device id for no-password devices.
func StartVNCServer(srv *Server, addr string) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	vs := &vncServer{srv: srv, addr: addr}
	go vs.serve(ln)
	return ln, nil
}

func (v *vncServer) serve(ln net.Listener) {
	log.Printf("VNC server listening on %s", ln.Addr())
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("vnc accept error: %v", err)
			continue
		}
		vc := &vncConn{srv: v.srv, conn: conn, reader: bufio.NewReader(conn), readyCh: make(chan protocol.Message, 1), frameCh: make(chan []byte, 1)}
		go vc.run()
	}
}

func defaultVNCDesktopRequest() protocol.Message {
	request := defaultDesktopRequest()
	request.FPS = 8
	request.Quality = 60
	request.Width = 1600
	request.Height = 1000
	request.InputBackend = "auto"
	return request
}

func (s *Server) vncDesktopRequest(deviceID string) protocol.Message {
	s.vncMu.RLock()
	request, ok := s.vncSettings[deviceID]
	s.vncMu.RUnlock()
	if !ok {
		request = defaultVNCDesktopRequest()
	}
	// VNC bridges decode full-frame JPEG only; always request MJPEG.
	request.Format = "mjpeg"
	return normalizeDesktopRequest(request)
}

func (s *Server) updateVNCDesktopRequest(deviceID string, request protocol.Message) bool {
	if deviceID == "" {
		return false
	}
	normalized := normalizeDesktopRequest(request)
	s.vncMu.Lock()
	s.vncSettings[deviceID] = normalized
	s.vncMu.Unlock()
	return true
}

func (v *vncConn) run() {
	defer v.close()
	_ = v.conn.SetDeadline(time.Now().Add(30 * time.Second))
	if err := v.handshake(); err != nil {
		log.Printf("vnc handshake failed: %v", err)
		return
	}
	shared, err := v.reader.ReadByte()
	if err != nil {
		return
	}
	_ = shared
	stream, err := v.srv.acquireVNCStream(v)
	if err != nil {
		log.Printf("vnc desktop start failed for %s: %v", v.deviceID, err)
		return
	}
	v.stream = stream
	ready := <-v.readyCh
	if ready.Error != "" || ready.Width <= 0 || ready.Height <= 0 {
		return
	}
	v.frameWidth, v.frameHeight = ready.Width, ready.Height
	if err := v.writeServerInit(ready.Width, ready.Height); err != nil {
		return
	}
	_ = v.conn.SetDeadline(time.Time{})
	v.loop()
}

func (v *vncConn) handshake() error {
	if _, err := io.WriteString(v.conn, "RFB 003.008\n"); err != nil {
		return err
	}
	version := make([]byte, 12)
	if _, err := io.ReadFull(v.reader, version); err != nil {
		return err
	}
	if !strings.HasPrefix(string(version), "RFB 003.") {
		return fmt.Errorf("unsupported protocol version %q", strings.TrimSpace(string(version)))
	}
	if _, err := v.conn.Write([]byte{1, rfbSecurityVeNCrypt}); err != nil {
		return err
	}
	selected, err := v.reader.ReadByte()
	if err != nil {
		return err
	}
	if selected != rfbSecurityVeNCrypt {
		return fmt.Errorf("client selected unsupported security type %d", selected)
	}
	if _, err := v.conn.Write([]byte{0, 2}); err != nil {
		return err
	}
	clientVersion := make([]byte, 2)
	if _, err := io.ReadFull(v.reader, clientVersion); err != nil {
		return err
	}
	if _, err := v.conn.Write([]byte{0}); err != nil { // VeNCrypt version accepted.
		return err
	}
	if err := v.writeU8(1); err != nil {
		return err
	}
	if err := v.writeU32(rfbVeNCryptPlain); err != nil {
		return err
	}
	subtype, err := v.readU32()
	if err != nil {
		return err
	}
	if subtype != rfbVeNCryptPlain {
		return fmt.Errorf("client selected unsupported VeNCrypt subtype %d", subtype)
	}
	username, password, err := v.readPlainCredentials()
	if err != nil {
		return err
	}
	client, deviceID, ok := v.authenticate(username, password)
	if !ok {
		_ = v.writeSecurityFailure("authentication failed")
		return fmt.Errorf("authentication failed for username %q", username)
	}
	v.client = client
	v.deviceID = deviceID
	v.request = v.srv.vncDesktopRequest(deviceID)
	return v.writeU32(0)
}

func (v *vncConn) readPlainCredentials() (string, string, error) {
	userLen, err := v.readU32()
	if err != nil {
		return "", "", err
	}
	passLen, err := v.readU32()
	if err != nil {
		return "", "", err
	}
	if userLen > 1024 || passLen > 1024 {
		return "", "", fmt.Errorf("credentials too large")
	}
	username := make([]byte, userLen)
	password := make([]byte, passLen)
	if _, err := io.ReadFull(v.reader, username); err != nil {
		return "", "", err
	}
	if _, err := io.ReadFull(v.reader, password); err != nil {
		return "", "", err
	}
	return strings.TrimSpace(string(username)), string(password), nil
}

func (v *vncConn) authenticate(username, password string) (*ClientConn, string, bool) {
	if username != "" {
		client := v.lookupClient(username)
		if client == nil {
			return nil, "", false
		}
		if client.Password != "" {
			// Brute-force throttle: reject during cooldown without comparing.
			if v.srv.authLimiter.wait(client.ID) > 0 {
				return nil, "", false
			}
			if constantTimeEqual(passwordFingerprint(password), passwordFingerprint(client.Password)) {
				v.srv.authLimiter.success(client.ID)
				return client, client.ID, true
			}
			v.srv.authLimiter.fail(client.ID)
			return nil, "", false
		}
		return client, client.ID, password == "" || constantTimeEqual(password, client.ID)
	}
	if password == "" {
		return nil, "", false
	}
	client := v.lookupClient(password)
	if client == nil || client.Password != "" {
		return nil, "", false
	}
	return client, client.ID, true
}

func (v *vncConn) lookupClient(deviceID string) *ClientConn {
	v.srv.mu.RLock()
	client := v.srv.clients[deviceID]
	v.srv.mu.RUnlock()
	return client
}

func vncStreamKey(deviceID string, request protocol.Message) string {
	request = normalizeDesktopRequest(request)
	return fmt.Sprintf("%s\x00%s\x00%d\x00%d\x00%d\x00%d\x00%s\x00%t", deviceID, request.Source, request.FPS, request.Quality, request.Width, request.Height, request.InputBackend, request.ShowCursor)
}

func (s *Server) acquireVNCStream(v *vncConn) (*vncDesktopStream, error) {
	key := vncStreamKey(v.deviceID, v.request)
	startStream := false
	s.vncMu.Lock()
	stream := s.vncStreams[key]
	if stream != nil && stream.closed {
		delete(s.vncStreams, key)
		stream = nil
	}
	if stream == nil {
		stream = &vncDesktopStream{srv: s, client: v.client, deviceID: v.deviceID, session: generateID(), key: key, request: v.request, subscribers: make(map[*vncConn]struct{})}
		s.vncStreams[key] = stream
		s.desktopMu.Lock()
		s.desktops[stream.session] = &desktopRoute{id: stream.session, clientID: v.deviceID, stream: stream}
		s.desktopMu.Unlock()
		startStream = true
	}
	stream.addSubscriberLocked(v)
	s.vncMu.Unlock()
	if startStream {
		startMsg := stream.request
		startMsg.Type = protocol.MsgDesktopStart
		startMsg.SessionID = stream.session
		if err := stream.client.Send(&startMsg); err != nil {
			stream.removeSubscriber(v)
			return nil, err
		}
	}
	return stream, nil
}

func (s *Server) releaseVNCStream(stream *vncDesktopStream) {
	s.vncMu.Lock()
	if current := s.vncStreams[stream.key]; current == stream {
		delete(s.vncStreams, stream.key)
	}
	s.vncMu.Unlock()
	s.desktopMu.Lock()
	if route := s.desktops[stream.session]; route != nil && route.stream == stream {
		delete(s.desktops, stream.session)
	}
	s.desktopMu.Unlock()
}

func (stream *vncDesktopStream) addSubscriberLocked(v *vncConn) {
	stream.mu.Lock()
	stream.subscribers[v] = struct{}{}
	v.session = stream.session
	ready := stream.ready
	latest := append([]byte(nil), stream.latestFrame...)
	width, height := stream.frameWidth, stream.frameHeight
	stream.mu.Unlock()
	if ready != nil {
		v.handleReady(ready)
	}
	if len(latest) > 0 {
		v.enqueueRawFrame(latest, width, height)
	}
}

func (stream *vncDesktopStream) removeSubscriber(v *vncConn) {
	stream.mu.Lock()
	delete(stream.subscribers, v)
	remaining := len(stream.subscribers)
	closed := stream.closed
	stream.mu.Unlock()
	if closed || remaining != 0 {
		return
	}
	stream.close()
}

func (stream *vncDesktopStream) handleReady(msg *protocol.Message) {
	stream.mu.Lock()
	copyMsg := *msg
	stream.ready = &copyMsg
	if msg.Width > 0 && msg.Height > 0 {
		stream.frameWidth = msg.Width
		stream.frameHeight = msg.Height
	}
	subscribers := make([]*vncConn, 0, len(stream.subscribers))
	for sub := range stream.subscribers {
		subscribers = append(subscribers, sub)
	}
	stream.mu.Unlock()
	for _, sub := range subscribers {
		sub.handleReady(msg)
	}
}

func (stream *vncDesktopStream) handleClose(msg *protocol.Message) {
	stream.mu.Lock()
	if stream.closed {
		stream.mu.Unlock()
		return
	}
	stream.closed = true
	subscribers := make([]*vncConn, 0, len(stream.subscribers))
	for sub := range stream.subscribers {
		subscribers = append(subscribers, sub)
	}
	stream.subscribers = make(map[*vncConn]struct{})
	stream.mu.Unlock()
	stream.srv.releaseVNCStream(stream)
	for _, sub := range subscribers {
		sub.handleClose(msg)
	}
}

func (stream *vncDesktopStream) enqueueFrame(data []byte) {
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return
	}
	stream.mu.Lock()
	width, height := stream.frameWidth, stream.frameHeight
	if width <= 0 || height <= 0 {
		stream.mu.Unlock()
		return
	}
	frame := vncRawFrame(img, width, height)
	stream.latestFrame = frame
	subscribers := make([]*vncConn, 0, len(stream.subscribers))
	for sub := range stream.subscribers {
		subscribers = append(subscribers, sub)
	}
	stream.mu.Unlock()
	for _, sub := range subscribers {
		sub.enqueueRawFrame(frame, width, height)
	}
}

func (stream *vncDesktopStream) closeDeviceDisconnected() {
	stream.closeWithMessage("device disconnected")
}

func (stream *vncDesktopStream) close() {
	stream.closeWithMessage("")
}

func (stream *vncDesktopStream) closeWithMessage(message string) {
	stream.mu.Lock()
	if stream.closed {
		stream.mu.Unlock()
		return
	}
	stream.closed = true
	subscribers := make([]*vncConn, 0, len(stream.subscribers))
	for sub := range stream.subscribers {
		subscribers = append(subscribers, sub)
	}
	stream.subscribers = make(map[*vncConn]struct{})
	stream.mu.Unlock()
	stream.srv.releaseVNCStream(stream)
	if message == "" && stream.client != nil {
		_ = stream.client.Send(&protocol.Message{Type: protocol.MsgDesktopClose, SessionID: stream.session})
	}
	for _, sub := range subscribers {
		sub.close()
	}
}

func (v *vncConn) loop() {
	for {
		msgType, err := v.reader.ReadByte()
		if err != nil {
			return
		}
		switch msgType {
		case rfbClientSetPixelFormat:
			if err := v.discard(19); err != nil {
				return
			}
		case rfbClientSetEncodings:
			if err := v.handleSetEncodings(); err != nil {
				return
			}
		case rfbClientFramebufferUpdateRequest:
			if err := v.handleFramebufferUpdateRequest(); err != nil {
				return
			}
		case rfbClientKeyEvent:
			if err := v.handleKeyEvent(); err != nil {
				return
			}
		case rfbClientPointerEvent:
			if err := v.handlePointerEvent(); err != nil {
				return
			}
		case rfbClientCutText:
			if err := v.handleClientCutText(); err != nil {
				return
			}
		default:
			return
		}
	}
}

func (v *vncConn) sendFramebuffer(incremental bool, reqX, reqY, reqW, reqH int) error {
	v.mu.Lock()
	frame := v.latestFrame
	last := v.lastSentFrame
	width, height := v.frameWidth, v.frameHeight
	lastWidth, lastHeight := v.lastSentWidth, v.lastSentHeight
	v.mu.Unlock()
	if width <= 0 || height <= 0 {
		return nil
	}
	if len(frame) == 0 {
		frame = make([]byte, width*height*4)
	}
	reqX, reqY, reqW, reqH, ok := vncClampRect(reqX, reqY, reqW, reqH, width, height)
	if !ok {
		return v.writeFramebufferUpdate(nil)
	}
	x, y, w, h := reqX, reqY, reqW, reqH
	if incremental && lastWidth == width && lastHeight == height && len(last) == len(frame) {
		var changed bool
		x, y, w, h, changed = vncChangedRect(last, frame, width, height)
		if !changed {
			return v.writeFramebufferUpdate(nil)
		}
		x, y, w, h, ok = vncIntersectRect(x, y, w, h, reqX, reqY, reqW, reqH)
		if !ok {
			return v.writeFramebufferUpdate(nil)
		}
	} else {
		rect := v.makeRawRect(frame, width, x, y, w, h)
		if err := v.writeFramebufferUpdate([]vncRawRect{rect}); err != nil {
			return err
		}
		v.setLastSentFrame(frame, width, height)
		return nil
	}
	rect := v.makeRawRect(frame, width, x, y, w, h)
	if err := v.writeFramebufferUpdate([]vncRawRect{rect}); err != nil {
		return err
	}
	v.setLastSentFrame(frame, width, height)
	return nil
}

func (v *vncConn) setLastSentFrame(frame []byte, width, height int) {
	v.mu.Lock()
	v.lastSentFrame = append(v.lastSentFrame[:0], frame...)
	v.lastSentWidth = width
	v.lastSentHeight = height
	v.mu.Unlock()
}

func (v *vncConn) sendInput(msg *protocol.Message) {
	msg.Type = protocol.MsgDesktopInput
	msg.SessionID = v.session
	msg.InputBackend = v.request.InputBackend
	if v.client != nil {
		v.client.Send(msg)
	}
}

func (v *vncConn) handleReady(msg *protocol.Message) {
	select {
	case v.readyCh <- *msg:
	default:
	}
}

func (v *vncConn) handleClose(msg *protocol.Message) {
	select {
	case v.readyCh <- *msg:
	default:
	}
	v.close()
}

func (v *vncConn) enqueueFrame(data []byte) {
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return
	}
	frame := vncRawFrame(img, v.frameWidth, v.frameHeight)
	v.enqueueRawFrame(frame, v.frameWidth, v.frameHeight)
}

func (v *vncConn) enqueueRawFrame(frame []byte, width, height int) {
	if len(frame) == 0 || width <= 0 || height <= 0 {
		return
	}
	v.mu.Lock()
	v.latestFrame = frame
	v.frameWidth = width
	v.frameHeight = height
	v.mu.Unlock()
	select {
	case v.frameCh <- frame:
		return
	default:
	}
	select {
	case <-v.frameCh:
	default:
	}
	select {
	case v.frameCh <- frame:
	default:
	}
}

func (v *vncConn) close() {
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return
	}
	v.closed = true
	stream := v.stream
	v.mu.Unlock()
	if stream != nil {
		stream.removeSubscriber(v)
	} else if v.session != "" {
		v.srv.desktopMu.Lock()
		delete(v.srv.desktops, v.session)
		v.srv.desktopMu.Unlock()
		if v.client != nil {
			_ = v.client.Send(&protocol.Message{Type: protocol.MsgDesktopClose, SessionID: v.session})
		}
	}
	_ = v.conn.Close()
}

func (s *Server) closeVNCForClient(clientID string) int {
	s.desktopMu.Lock()
	var routes []*desktopRoute
	for id, route := range s.desktops {
		if route.clientID == clientID && (route.vnc != nil || route.stream != nil) {
			routes = append(routes, route)
			delete(s.desktops, id)
		}
	}
	s.desktopMu.Unlock()
	closed := 0
	for _, route := range routes {
		if route.stream != nil {
			route.stream.close()
			closed++
			continue
		}
		if route.vnc != nil {
			route.vnc.close()
			closed++
		}
	}
	return closed
}

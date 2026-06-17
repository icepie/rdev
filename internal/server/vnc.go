package server

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"time"
	"unicode"

	"rdev/internal/protocol"
)

const (
	rfbSecurityVeNCrypt = 19
	rfbVeNCryptPlain    = 256

	rfbClientSetPixelFormat           = 0
	rfbClientSetEncodings             = 2
	rfbClientFramebufferUpdateRequest = 3
	rfbClientKeyEvent                 = 4
	rfbClientPointerEvent             = 5
	rfbClientCutText                  = 6

	rfbEncodingRaw  = 0
	rfbEncodingZRLE = 16
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
	if ok {
		return normalizeDesktopRequest(request)
	}
	return defaultVNCDesktopRequest()
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
			return client, client.ID, constantTimeEqual(passwordFingerprint(password), passwordFingerprint(client.Password))
		}
		return client, client.ID, password == "" || password == client.ID
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

func (v *vncConn) writeServerInit(width, height int) error {
	if err := v.writeU16(width); err != nil {
		return err
	}
	if err := v.writeU16(height); err != nil {
		return err
	}
	pf := []byte{32, 24, 0, 1, 0, 255, 0, 255, 0, 255, 16, 8, 0, 0, 0, 0}
	if _, err := v.conn.Write(pf); err != nil {
		return err
	}
	name := []byte("RDev " + v.deviceID)
	if err := v.writeU32(uint32(len(name))); err != nil {
		return err
	}
	_, err := v.conn.Write(name)
	return err
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

func (v *vncConn) handleSetEncodings() error {
	if err := v.discard(1); err != nil {
		return err
	}
	count, err := v.readU16()
	if err != nil {
		return err
	}
	v.supportsZRLE = false
	for i := 0; i < int(count); i++ {
		encoding, err := v.readS32()
		if err != nil {
			return err
		}
		if encoding == rfbEncodingZRLE {
			v.supportsZRLE = true
		}
	}
	return nil
}

func (v *vncConn) handleFramebufferUpdateRequest() error {
	incremental, err := v.reader.ReadByte()
	if err != nil {
		return err
	}
	x, err := v.readU16()
	if err != nil {
		return err
	}
	y, err := v.readU16()
	if err != nil {
		return err
	}
	width, err := v.readU16()
	if err != nil {
		return err
	}
	height, err := v.readU16()
	if err != nil {
		return err
	}
	return v.sendFramebuffer(incremental != 0, int(x), int(y), int(width), int(height))
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

type vncRawRect struct {
	x      int
	y      int
	width  int
	height int
	data   []byte
}

func (v *vncConn) makeRawRect(frame []byte, frameWidth, x, y, width, height int) vncRawRect {
	return vncRawRect{x: x, y: y, width: width, height: height, data: vncRawSubrect(frame, frameWidth, x, y, width, height)}
}

func (v *vncConn) writeFramebufferUpdate(rects []vncRawRect) error {
	if v.supportsZRLE {
		return v.writeZRLEFramebufferUpdate(rects)
	}
	return v.writeRawFramebufferUpdate(rects)
}

func (v *vncConn) writeRawFramebufferUpdate(rects []vncRawRect) error {
	if err := v.writeU8(0); err != nil { // FramebufferUpdate
		return err
	}
	if err := v.writeU8(0); err != nil {
		return err
	}
	if err := v.writeU16(len(rects)); err != nil {
		return err
	}
	for _, rect := range rects {
		if err := v.writeU16(rect.x); err != nil {
			return err
		}
		if err := v.writeU16(rect.y); err != nil {
			return err
		}
		if err := v.writeU16(rect.width); err != nil {
			return err
		}
		if err := v.writeU16(rect.height); err != nil {
			return err
		}
		if err := v.writeS32(rfbEncodingRaw); err != nil {
			return err
		}
		if _, err := v.conn.Write(rect.data); err != nil {
			return err
		}
	}
	return nil
}

func (v *vncConn) writeZRLEFramebufferUpdate(rects []vncRawRect) error {
	encoded := make([][]byte, len(rects))
	for i, rect := range rects {
		encoded[i] = vncEncodeZRLE(rect.data, rect.width, rect.height)
	}
	if err := v.writeU8(0); err != nil { // FramebufferUpdate
		return err
	}
	if err := v.writeU8(0); err != nil {
		return err
	}
	if err := v.writeU16(len(rects)); err != nil {
		return err
	}
	for i, rect := range rects {
		if err := v.writeU16(rect.x); err != nil {
			return err
		}
		if err := v.writeU16(rect.y); err != nil {
			return err
		}
		if err := v.writeU16(rect.width); err != nil {
			return err
		}
		if err := v.writeU16(rect.height); err != nil {
			return err
		}
		if err := v.writeS32(rfbEncodingZRLE); err != nil {
			return err
		}
		if err := v.writeU32(uint32(len(encoded[i]))); err != nil {
			return err
		}
		if _, err := v.conn.Write(encoded[i]); err != nil {
			return err
		}
	}
	return nil
}

func (v *vncConn) setLastSentFrame(frame []byte, width, height int) {
	v.mu.Lock()
	v.lastSentFrame = append(v.lastSentFrame[:0], frame...)
	v.lastSentWidth = width
	v.lastSentHeight = height
	v.mu.Unlock()
}

func (v *vncConn) handleKeyEvent() error {
	down, err := v.reader.ReadByte()
	if err != nil {
		return err
	}
	if err := v.discard(2); err != nil {
		return err
	}
	keysym, err := v.readU32()
	if err != nil {
		return err
	}
	key, code := rfbKey(keysym)
	if code == "" {
		return nil
	}
	inputType := "key_up"
	if down != 0 {
		inputType = "key_down"
	}
	v.sendInput(&protocol.Message{InputType: inputType, Key: key, Code: code})
	return nil
}

func rfbKey(keysym uint32) (string, string) {
	if keysym >= 'a' && keysym <= 'z' {
		upper := unicode.ToUpper(rune(keysym))
		return string(rune(keysym)), "Key" + string(upper)
	}
	if keysym >= 'A' && keysym <= 'Z' {
		return string(rune(keysym)), "Key" + string(rune(keysym))
	}
	if keysym >= '0' && keysym <= '9' {
		return string(rune(keysym)), "Digit" + string(rune(keysym))
	}
	switch keysym {
	case 0xff1b:
		return "Escape", "Escape"
	case 0xff08:
		return "Backspace", "Backspace"
	case 0xff09:
		return "Tab", "Tab"
	case 0xff0d:
		return "Enter", "Enter"
	case 0xffe3, 0xffe4:
		return "Control", "ControlLeft"
	case 0xffe1:
		return "Shift", "ShiftLeft"
	case 0xffe2:
		return "Shift", "ShiftRight"
	case 0xffe9, 0xffea:
		return "Alt", "AltLeft"
	case 0x20:
		return " ", "Space"
	case 0xff50:
		return "Home", "Home"
	case 0xff51:
		return "ArrowLeft", "ArrowLeft"
	case 0xff52:
		return "ArrowUp", "ArrowUp"
	case 0xff53:
		return "ArrowRight", "ArrowRight"
	case 0xff54:
		return "ArrowDown", "ArrowDown"
	case 0xff55:
		return "PageUp", "PageUp"
	case 0xff56:
		return "PageDown", "PageDown"
	case 0xff57:
		return "End", "End"
	case 0xff63:
		return "Insert", "Insert"
	case 0xffff:
		return "Delete", "Delete"
	}
	if keysym >= 0xffbe && keysym <= 0xffc9 {
		n := keysym - 0xffbd
		return fmt.Sprintf("F%d", n), fmt.Sprintf("F%d", n)
	}
	return "", ""
}

func (v *vncConn) handlePointerEvent() error {
	buttons, err := v.reader.ReadByte()
	if err != nil {
		return err
	}
	x, err := v.readU16()
	if err != nil {
		return err
	}
	y, err := v.readU16()
	if err != nil {
		return err
	}
	now := time.Now()
	if now.Sub(v.lastMouse) >= 16*time.Millisecond {
		v.sendInput(&protocol.Message{InputType: "mouse_move", X: int(x), Y: int(y), PointerType: "mouse"})
		v.lastMouse = now
	}
	for i := 0; i < 3; i++ {
		mask := uint8(1 << i)
		was := v.buttons&mask != 0
		is := buttons&mask != 0
		if was == is {
			continue
		}
		button := []int{0, 1, 2}[i]
		inputType := "mouse_up"
		if is {
			inputType = "mouse_down"
		}
		v.sendInput(&protocol.Message{InputType: inputType, X: int(x), Y: int(y), Button: button, PointerType: "mouse"})
	}
	if buttons&(1<<3) != 0 {
		v.sendInput(&protocol.Message{InputType: "wheel", X: int(x), Y: int(y), DeltaY: -120, PointerType: "mouse"})
	}
	if buttons&(1<<4) != 0 {
		v.sendInput(&protocol.Message{InputType: "wheel", X: int(x), Y: int(y), DeltaY: 120, PointerType: "mouse"})
	}
	if buttons&(1<<5) != 0 {
		v.sendInput(&protocol.Message{InputType: "wheel", X: int(x), Y: int(y), DeltaX: -120, PointerType: "mouse"})
	}
	if buttons&(1<<6) != 0 {
		v.sendInput(&protocol.Message{InputType: "wheel", X: int(x), Y: int(y), DeltaX: 120, PointerType: "mouse"})
	}
	v.buttons = buttons & 0x07
	return nil
}

func (v *vncConn) sendInput(msg *protocol.Message) {
	msg.Type = protocol.MsgDesktopInput
	msg.SessionID = v.session
	msg.InputBackend = v.request.InputBackend
	if v.client != nil {
		v.client.Send(msg)
	}
}

func (v *vncConn) handleClientCutText() error {
	if err := v.discard(3); err != nil {
		return err
	}
	length, err := v.readU32()
	if err != nil {
		return err
	}
	if length > 16*1024*1024 {
		return fmt.Errorf("client cut text too large")
	}
	return v.discard(int(length))
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

func vncRawFrame(img image.Image, width, height int) []byte {
	if width <= 0 || height <= 0 {
		return nil
	}
	bounds := img.Bounds()
	frame := make([]byte, width*height*4)
	for y := 0; y < height; y++ {
		sy := bounds.Min.Y + y*bounds.Dy()/height
		for x := 0; x < width; x++ {
			sx := bounds.Min.X + x*bounds.Dx()/width
			r, g, b, _ := img.At(sx, sy).RGBA()
			off := (y*width + x) * 4
			frame[off+0] = byte(b >> 8)
			frame[off+1] = byte(g >> 8)
			frame[off+2] = byte(r >> 8)
			frame[off+3] = 0
		}
	}
	return frame
}

func vncClampRect(x, y, width, height, maxWidth, maxHeight int) (int, int, int, int, bool) {
	if maxWidth <= 0 || maxHeight <= 0 || width <= 0 || height <= 0 {
		return 0, 0, 0, 0, false
	}
	if x < 0 {
		width += x
		x = 0
	}
	if y < 0 {
		height += y
		y = 0
	}
	if x >= maxWidth || y >= maxHeight || width <= 0 || height <= 0 {
		return 0, 0, 0, 0, false
	}
	if x+width > maxWidth {
		width = maxWidth - x
	}
	if y+height > maxHeight {
		height = maxHeight - y
	}
	return x, y, width, height, width > 0 && height > 0
}

func vncIntersectRect(ax, ay, aw, ah, bx, by, bw, bh int) (int, int, int, int, bool) {
	x1 := maxInt(ax, bx)
	y1 := maxInt(ay, by)
	x2 := minInt(ax+aw, bx+bw)
	y2 := minInt(ay+ah, by+bh)
	w, h := x2-x1, y2-y1
	return x1, y1, w, h, w > 0 && h > 0
}

func vncChangedRect(prev, next []byte, width, height int) (int, int, int, int, bool) {
	if width <= 0 || height <= 0 || len(prev) != len(next) || len(next) < width*height*4 {
		return 0, 0, width, height, true
	}
	minX, minY := width, height
	maxX, maxY := -1, -1
	stride := width * 4
	for y := 0; y < height; y++ {
		rowStart := y * stride
		rowEnd := rowStart + stride
		if bytes.Equal(prev[rowStart:rowEnd], next[rowStart:rowEnd]) {
			continue
		}
		left, right := -1, -1
		for x := 0; x < width; x++ {
			off := rowStart + x*4
			if !bytes.Equal(prev[off:off+4], next[off:off+4]) {
				left = x
				break
			}
		}
		for x := width - 1; x >= 0; x-- {
			off := rowStart + x*4
			if !bytes.Equal(prev[off:off+4], next[off:off+4]) {
				right = x
				break
			}
		}
		if left >= 0 {
			minX = minInt(minX, left)
			maxX = maxInt(maxX, right)
			minY = minInt(minY, y)
			maxY = maxInt(maxY, y)
		}
	}
	if maxX < minX || maxY < minY {
		return 0, 0, 0, 0, false
	}
	return minX, minY, maxX - minX + 1, maxY - minY + 1, true
}

func vncRawSubrect(frame []byte, frameWidth, x, y, width, height int) []byte {
	if width <= 0 || height <= 0 || frameWidth <= 0 {
		return nil
	}
	stride := frameWidth * 4
	rectStride := width * 4
	out := make([]byte, rectStride*height)
	for row := 0; row < height; row++ {
		src := (y+row)*stride + x*4
		copy(out[row*rectStride:(row+1)*rectStride], frame[src:src+rectStride])
	}
	return out
}

func vncEncodeZRLE(raw []byte, width, height int) []byte {
	var plain bytes.Buffer
	for y := 0; y < height; y += 64 {
		tileH := minInt(64, height-y)
		for x := 0; x < width; x += 64 {
			tileW := minInt(64, width-x)
			plain.Write(vncEncodeZRLETile(raw, width, x, y, tileW, tileH))
		}
	}
	var compressed bytes.Buffer
	zw, err := zlib.NewWriterLevel(&compressed, zlib.BestSpeed)
	if err != nil {
		zw = zlib.NewWriter(&compressed)
	}
	_, _ = zw.Write(plain.Bytes())
	_ = zw.Close()
	return compressed.Bytes()
}

func vncEncodeZRLETile(raw []byte, stridePixels, x, y, width, height int) []byte {
	palette := make([][3]byte, 0, 16)
	paletteIndex := make(map[[3]byte]byte, 16)
	indices := make([]byte, 0, width*height)
	paletteOverflow := false
	for ty := 0; ty < height; ty++ {
		row := ((y+ty)*stridePixels + x) * 4
		for tx := 0; tx < width; tx++ {
			px := row + tx*4
			color := [3]byte{raw[px], raw[px+1], raw[px+2]}
			idx, ok := paletteIndex[color]
			if !ok {
				if len(palette) >= 16 {
					paletteOverflow = true
					break
				}
				idx = byte(len(palette))
				paletteIndex[color] = idx
				palette = append(palette, color)
			}
			indices = append(indices, idx)
		}
		if paletteOverflow {
			break
		}
	}
	if len(palette) == 1 && !paletteOverflow {
		return []byte{1, palette[0][0], palette[0][1], palette[0][2]}
	}
	if len(palette) >= 2 && !paletteOverflow {
		return vncEncodeZRLEPackedPaletteTile(palette, indices, width, height)
	}
	var out bytes.Buffer
	out.WriteByte(0) // raw truecolor tile
	for ty := 0; ty < height; ty++ {
		row := ((y+ty)*stridePixels + x) * 4
		for tx := 0; tx < width; tx++ {
			px := row + tx*4
			out.Write(raw[px : px+3]) // 32bpp depth-24 little-endian CPIXEL omits X byte.
		}
	}
	return out.Bytes()
}

func vncEncodeZRLEPackedPaletteTile(palette [][3]byte, indices []byte, width, height int) []byte {
	bitsPerPixel := 1
	if len(palette) > 4 {
		bitsPerPixel = 4
	} else if len(palette) > 2 {
		bitsPerPixel = 2
	}
	var out bytes.Buffer
	out.WriteByte(byte(len(palette)))
	for _, color := range palette {
		out.Write(color[:])
	}
	mask := byte((1 << bitsPerPixel) - 1)
	for y := 0; y < height; y++ {
		bitPos := 8
		var packed byte
		row := y * width
		for x := 0; x < width; x++ {
			bitPos -= bitsPerPixel
			packed |= (indices[row+x] & mask) << bitPos
			if bitPos == 0 {
				out.WriteByte(packed)
				packed = 0
				bitPos = 8
			}
		}
		if bitPos != 8 {
			out.WriteByte(packed)
		}
	}
	return out.Bytes()
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
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

func (v *vncConn) writeSecurityFailure(reason string) error {
	if err := v.writeU32(1); err != nil {
		return err
	}
	if err := v.writeU32(uint32(len(reason))); err != nil {
		return err
	}
	_, err := io.WriteString(v.conn, reason)
	return err
}

func (v *vncConn) discard(n int) error {
	_, err := io.CopyN(io.Discard, v.reader, int64(n))
	return err
}

func (v *vncConn) readU16() (uint16, error) {
	var value uint16
	err := binary.Read(v.reader, binary.BigEndian, &value)
	return value, err
}

func (v *vncConn) readU32() (uint32, error) {
	var value uint32
	err := binary.Read(v.reader, binary.BigEndian, &value)
	return value, err
}

func (v *vncConn) readS32() (int32, error) {
	var value int32
	err := binary.Read(v.reader, binary.BigEndian, &value)
	return value, err
}

func (v *vncConn) writeU8(value byte) error {
	_, err := v.conn.Write([]byte{value})
	return err
}

func (v *vncConn) writeU16(value int) error {
	return binary.Write(v.conn, binary.BigEndian, uint16(value))
}

func (v *vncConn) writeU32(value uint32) error {
	return binary.Write(v.conn, binary.BigEndian, value)
}

func (v *vncConn) writeS32(value int32) error {
	return binary.Write(v.conn, binary.BigEndian, value)
}

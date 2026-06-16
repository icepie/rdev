package server

import (
	"bufio"
	"bytes"
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

	rfbEncodingRaw = 0
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

	mu          sync.Mutex
	closed      bool
	readyCh     chan protocol.Message
	frameCh     chan []byte
	latestFrame []byte
	frameWidth  int
	frameHeight int
	buttons     uint8
	lastMouse   time.Time
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
	if err := v.startDesktop(); err != nil {
		log.Printf("vnc desktop start failed for %s: %v", v.deviceID, err)
		return
	}
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

func (v *vncConn) startDesktop() error {
	v.session = generateID()
	v.srv.desktopMu.Lock()
	v.srv.desktops[v.session] = &desktopRoute{id: v.session, clientID: v.deviceID, vnc: v}
	v.srv.desktopMu.Unlock()
	startMsg := v.request
	startMsg.Type = protocol.MsgDesktopStart
	startMsg.SessionID = v.session
	return v.client.Send(&startMsg)
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
	return v.discard(int(count) * 4)
}

func (v *vncConn) handleFramebufferUpdateRequest() error {
	if err := v.discard(1); err != nil {
		return err
	}
	_, _ = v.readU16()
	_, _ = v.readU16()
	_, _ = v.readU16()
	_, _ = v.readU16()
	return v.sendFramebuffer()
}

func (v *vncConn) sendFramebuffer() error {
	v.mu.Lock()
	frame := append([]byte(nil), v.latestFrame...)
	width, height := v.frameWidth, v.frameHeight
	v.mu.Unlock()
	if width <= 0 || height <= 0 {
		return nil
	}
	if len(frame) == 0 {
		frame = make([]byte, width*height*4)
	}
	if err := v.writeU8(0); err != nil { // FramebufferUpdate
		return err
	}
	if err := v.writeU8(0); err != nil {
		return err
	}
	if err := v.writeU16(1); err != nil {
		return err
	}
	if err := v.writeU16(0); err != nil {
		return err
	}
	if err := v.writeU16(0); err != nil {
		return err
	}
	if err := v.writeU16(width); err != nil {
		return err
	}
	if err := v.writeU16(height); err != nil {
		return err
	}
	if err := v.writeS32(rfbEncodingRaw); err != nil {
		return err
	}
	_, err := v.conn.Write(frame)
	return err
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
	v.mu.Lock()
	v.latestFrame = frame
	v.mu.Unlock()
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

func (v *vncConn) close() {
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return
	}
	v.closed = true
	v.mu.Unlock()
	if v.session != "" {
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
		if route.clientID == clientID && route.vnc != nil {
			routes = append(routes, route)
			delete(s.desktops, id)
		}
	}
	s.desktopMu.Unlock()
	for _, route := range routes {
		route.vnc.close()
	}
	return len(routes)
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

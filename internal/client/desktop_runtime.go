package client

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"log"
	"runtime"
	"sync"
	"time"

	"rdev/internal/protocol"
)

type desktopCapturer interface {
	Bounds() image.Rectangle
	Capture() (image.Image, error)
	Close() error
}

type desktopSourceReporter interface {
	Source() protocol.DesktopSource
}

type desktopSession struct {
	stop      chan struct{}
	closeOnce sync.Once
	snapshot  chan struct{}
	input     desktopInput
	clipboard desktopClipboard
	cuaMu     sync.Mutex
	cua       *nativeCuaSession
	rtcMu     sync.Mutex
	rtc       *desktopRTCSession
	inputMu   sync.Mutex
	frameMu   sync.RWMutex
	bounds    image.Rectangle
	frameW    int
	frameH    int
	cursor    image.Point
	cursorOK  bool
}

func (s *desktopSession) setFrame(bounds image.Rectangle, width, height int) {
	s.frameMu.Lock()
	s.bounds = bounds
	s.frameW = width
	s.frameH = height
	s.frameMu.Unlock()
}

func (s *desktopSession) setCursor(x, y int) {
	s.frameMu.Lock()
	s.cursor = image.Pt(x, y)
	s.cursorOK = true
	s.frameMu.Unlock()
}

func (s *desktopSession) cursorPosition() (image.Point, bool) {
	s.frameMu.RLock()
	cursor := s.cursor
	ok := s.cursorOK
	s.frameMu.RUnlock()
	return cursor, ok
}

func (s *desktopSession) mapPoint(x, y int) (int, int) {
	s.frameMu.RLock()
	bounds := s.bounds
	frameW := s.frameW
	frameH := s.frameH
	s.frameMu.RUnlock()
	if frameW <= 0 || frameH <= 0 || bounds.Dx() <= 0 || bounds.Dy() <= 0 {
		return x, y
	}
	return bounds.Min.X + desktopInputOffset(x, frameW, bounds.Dx()), bounds.Min.Y + desktopInputOffset(y, frameH, bounds.Dy())
}

// Preserve both endpoints so a scaled viewer can reach screen-edge controls.
func desktopInputOffset(pixel, frame, source int) int {
	if frame <= 1 || source <= 1 {
		return 0
	}
	span := int64(frame - 1)
	return int((int64(max(0, min(pixel, frame-1)))*int64(source-1) + span/2) / span)
}

func (c *Client) handleDesktopStart(msg *protocol.Message) {
	if msg.SessionID == "" {
		return
	}

	c.mu.Lock()
	if _, ok := c.desktopSessions[msg.SessionID]; ok {
		c.mu.Unlock()
		return
	}
	session := &desktopSession{stop: make(chan struct{}), snapshot: make(chan struct{}, 1)}
	c.desktopSessions[msg.SessionID] = session
	c.mu.Unlock()
	go c.runDesktop(msg, session)
}

func (c *Client) runDesktop(msg *protocol.Message, session *desktopSession) {
	defer func() {
		c.mu.Lock()
		if current, ok := c.desktopSessions[msg.SessionID]; ok && current == session {
			delete(c.desktopSessions, msg.SessionID)
		}
		c.mu.Unlock()
	}()
	defer session.close()
	if session.stopped() {
		return
	}

	availableInputs := desktopInputBackends()
	preferred := chooseDesktopInputBackend(msg.InputBackend, availableInputs)
	input, inputBackend := openDesktopInput(preferred, availableInputs, newDesktopInput)
	clipboard, err := newDesktopClipboard()
	if err != nil {
		log.Printf("desktop clipboard unavailable: %v", err)
	}
	if !session.installInput(input, clipboard) {
		return
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	capturer, err := newDesktopCapturer(msg.Source)
	if err != nil {
		caps := desktopCapabilities()
		caps.Supported = false
		caps.ViewOnly = false
		caps.Input = false
		caps.Clipboard = false
		caps.Reason = err.Error()
		c.send(&protocol.Message{Type: protocol.MsgDesktopReady, SessionID: msg.SessionID, DesktopCapabilities: caps, Error: err.Error()})
		return
	}
	defer capturer.Close()

	bounds := capturer.Bounds()
	session.inputMu.Lock()
	if boundsInput, ok := session.input.(desktopInputBounds); ok {
		boundsInput.SetBounds(bounds)
	}
	session.inputMu.Unlock()
	caps := desktopCapabilities()
	caps.Supported = true
	caps.ViewOnly = false
	caps.Input = input != nil
	if len(availableInputs) > 0 {
		caps.InputBackends = append([]string(nil), availableInputs...)
		caps.InputOptions = desktopInputOptions()
	}
	caps.Clipboard = clipboard != nil
	caps.Reason = ""
	size := scaledDimension(bounds.Dx(), bounds.Dy(), msg.Width, msg.Height)
	session.setFrame(bounds, size.X, size.Y)
	sourceID := desktopSourceLabel(msg.Source)
	if reporter, ok := capturer.(desktopSourceReporter); ok {
		if source := reporter.Source(); source.ID != "" {
			sourceID = source.ID
		}
	}

	fps := msg.FPS
	if fps <= 0 {
		fps = 30
	}
	if fps > 30 {
		fps = 30
	}
	quality := msg.Quality
	if quality <= 0 {
		quality = 75
	}
	if quality > 90 {
		quality = 90
	}
	if quality < 20 {
		quality = 20
	}

	sourceName := desktopSourceLabel(msg.Source)
	if reporter, ok := capturer.(desktopSourceReporter); ok {
		if source := reporter.Source(); source.ID != "" {
			sourceName = source.ID
		}
	}
	if msg.OnDemand {
		c.send(&protocol.Message{Type: protocol.MsgDesktopReady, SessionID: msg.SessionID,
			DesktopCapabilities: caps, Width: size.X, Height: size.Y, Format: "mjpeg", Source: sourceID})
		c.runDesktopSnapshots(msg.SessionID, session, capturer, size, quality)
		return
	}
	requestedFormat := normalizeDesktopFormat(msg.Format)
	size = normalizeDesktopFrameSize(requestedFormat, size)
	session.setFrame(bounds, size.X, size.Y)
	encoder := newDesktopEncoder(requestedFormat, size.X, size.Y, quality, fps)
	defer encoder.Close()
	format := encoder.Format()
	backend := encoder.Backend()
	if requestedFormat == desktopFormatH264 && format != desktopFormatH264 {
		log.Printf("desktop h264 encoder unavailable for source %s, falling back to mjpeg", sourceName)
	} else {
		log.Printf("desktop %s encoder backend %s selected for source %s", format, backend, sourceName)
	}
	readySent := false
	sendReady := func() {
		codecInfo := encoder.CodecInfo()
		c.send(&protocol.Message{
			Type:                protocol.MsgDesktopReady,
			SessionID:           msg.SessionID,
			DesktopCapabilities: caps,
			Width:               size.X,
			Height:              size.Y,
			Format:              format,
			Codec:               codecInfo.Codec,
			Description:         codecInfo.Description,
			EncoderBackend:      backend,
			EncoderAcceleration: desktopEncoderAcceleration(backend),
			Source:              sourceID,
			FPS:                 fps,
			InputBackend:        inputBackend,
		})
		readySent = true
	}
	if desktopEncoderReady(encoder) {
		sendReady()
	}
	sender := newDesktopFrameSender(encoder)
	var frameScratch *image.RGBA
	frameInterval := time.Second / time.Duration(fps)
	timer := time.NewTimer(0)
	defer timer.Stop()
	var nextFrame time.Time
	for ; ; timer.Reset(time.Until(nextFrame)) {
		select {
		case <-session.stop:
			return
		case <-timer.C:
			nextFrame = time.Now().Add(frameInterval)
			img, err := capturer.Capture()
			if err != nil {
				err = fmt.Errorf("desktop capture failed for source %s: %w", sourceName, err)
				log.Printf("%v", err)
				c.send(&protocol.Message{Type: protocol.MsgDesktopClose, SessionID: msg.SessionID, Error: err.Error()})
				return
			}
			frame, err := resizeDesktopFrameToSize(frameScratch, img, size.X, size.Y)
			if err != nil && !errors.Is(err, errResizeNoPixels) {
				log.Printf("desktop resize error for source %s: %v", sourceName, err)
				continue
			}
			// Keep the scratch buffer only when the helper allocated it;
			// the zero-copy path returns the capturer's reusable image.
			if frame != img {
				frameScratch = frame
			}
			if err != nil {
				continue
			}
			bounds := capturer.Bounds()
			if msg.ShowCursor {
				// Cursor composition must not mutate a capturer-owned reusable
				// frame. DXGI may return that frame again when the desktop is idle.
				if sourceFrame, ok := img.(*image.RGBA); ok && frame == sourceFrame {
					frameScratch = copyDesktopRGBA(frameScratch, sourceFrame)
					frame = frameScratch
				}
				if cursor, ok := desktopCursorPosition(session, capturer); ok {
					overlayDesktopCursor(frame, bounds, cursor)
				}
			}
			session.setFrame(bounds, size.X, size.Y)
			payload, send, err := sender.tick(frame, time.Now())
			if err != nil {
				err = fmt.Errorf("desktop %s encode failed for source %s with %s: %w", format, sourceName, backend, err)
				log.Printf("%v", err)
				c.send(&protocol.Message{Type: protocol.MsgDesktopClose, SessionID: msg.SessionID, Format: format, EncoderBackend: backend, Error: err.Error()})
				return
			}
			if !readySent {
				if !desktopEncoderReady(encoder) {
					err = fmt.Errorf("desktop %s encoder %s produced no codec headers", format, backend)
					log.Printf("%v", err)
					c.send(&protocol.Message{Type: protocol.MsgDesktopClose, SessionID: msg.SessionID, Format: format, EncoderBackend: backend, Error: err.Error()})
					return
				}
				sendReady()
			}
			if !send {
				continue
			}
			started := time.Now()
			if !session.sendFrame(payload) {
				if err := c.sendBinary(protocol.BinDesktopFrame, msg.SessionID, payload); err != nil {
					log.Printf("desktop frame send error for source %s: %s", sourceName, err)
					return
				}
			}
			if elapsed := time.Since(started); elapsed > time.Second {
				log.Printf("desktop frame send slow for source %s: %s", sourceName, elapsed)
			}
		}
	}
}

func desktopSourceLabel(source string) string {
	if source == "" || source == "auto" {
		return "auto"
	}
	return source
}

func scaledDimension(sourceWidth, sourceHeight, maxWidth, maxHeight int) image.Point {
	if sourceWidth <= 0 || sourceHeight <= 0 {
		return image.Pt(1, 1)
	}
	width := sourceWidth
	height := sourceHeight
	if maxWidth > 0 && width > maxWidth {
		height = max(1, height*maxWidth/width)
		width = maxWidth
	}
	if maxHeight > 0 && height > maxHeight {
		width = max(1, width*maxHeight/height)
		height = maxHeight
	}
	return image.Pt(width, height)
}

func (c *Client) handleDesktopCua(msg *protocol.Message) {
	if msg.SessionID == "" {
		return
	}
	c.mu.Lock()
	session := c.desktopSessions[msg.SessionID]
	c.mu.Unlock()
	if session == nil || len(msg.CuaRequest) == 0 {
		return
	}
	session.cuaMu.Lock()
	if session.stopped() {
		session.cuaMu.Unlock()
		return
	}
	if session.cua == nil {
		cua, err := newNativeCuaSession()
		if err != nil {
			session.cuaMu.Unlock()
			c.send(&protocol.Message{Type: protocol.MsgDesktopCuaResult, SessionID: msg.SessionID, RequestID: msg.RequestID, Error: err.Error()})
			return
		}
		session.cua = cua
	}
	result, err := session.cua.request(msg.CuaRequest)
	session.cuaMu.Unlock()
	response := &protocol.Message{Type: protocol.MsgDesktopCuaResult, SessionID: msg.SessionID, RequestID: msg.RequestID, CuaResult: result}
	if err != nil {
		response.Error = err.Error()
	}
	c.send(response)
}

func (s *desktopSession) closeCua() {
	s.cuaMu.Lock()
	if s.cua != nil {
		s.cua.close()
		s.cua = nil
	}
	s.cuaMu.Unlock()
}
func (c *Client) handleDesktopClipboard(msg *protocol.Message) {
	if msg.SessionID == "" {
		return
	}
	c.mu.Lock()
	session := c.desktopSessions[msg.SessionID]
	c.mu.Unlock()
	if session == nil {
		return
	}
	session.inputMu.Lock()
	clipboard := session.clipboard
	session.inputMu.Unlock()
	if clipboard == nil {
		return
	}

	switch msg.ClipboardAction {
	case "get":
		items, err := clipboard.Get()
		if err != nil {
			c.send(&protocol.Message{Type: protocol.MsgDesktopClipboard, SessionID: msg.SessionID, ClipboardAction: "error", Error: err.Error()})
			return
		}
		c.send(&protocol.Message{Type: protocol.MsgDesktopClipboard, SessionID: msg.SessionID, ClipboardAction: "content", ClipboardItems: items, Text: desktopClipboardPlainText(items)})
	case "set":
		items := msg.ClipboardItems
		if len(items) == 0 && msg.Text != "" {
			items = []protocol.ClipboardItem{{MIME: "text/plain", Data: base64.StdEncoding.EncodeToString([]byte(msg.Text))}}
		}
		if err := clipboard.Set(items); err != nil {
			c.send(&protocol.Message{Type: protocol.MsgDesktopClipboard, SessionID: msg.SessionID, ClipboardAction: "error", Error: err.Error()})
			return
		}
		c.send(&protocol.Message{Type: protocol.MsgDesktopClipboard, SessionID: msg.SessionID, ClipboardAction: "content", ClipboardItems: items, Text: desktopClipboardPlainText(items)})
	}
}

func desktopClipboardPlainText(items []protocol.ClipboardItem) string {
	for _, item := range items {
		if canonicalDesktopClipboardMIME(item.MIME) != "text/plain" {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(item.Data)
		if err == nil {
			return string(data)
		}
	}
	return ""
}

func (c *Client) handleDesktopInput(msg *protocol.Message) {
	if msg.SessionID == "" {
		return
	}
	c.mu.Lock()
	session := c.desktopSessions[msg.SessionID]
	c.mu.Unlock()
	if session == nil {
		return
	}
	x, y := session.mapPoint(msg.X, msg.Y)
	if msg.InputType == "cursor_move" {
		session.setCursor(x, y)
		return
	}
	event := desktopInputEvent{
		Type:        msg.InputType,
		X:           x,
		Y:           y,
		Button:      msg.Button,
		DeltaX:      msg.DeltaX,
		DeltaY:      msg.DeltaY,
		Key:         msg.Key,
		Code:        msg.Code,
		CtrlKey:     msg.CtrlKey,
		AltKey:      msg.AltKey,
		ShiftKey:    msg.ShiftKey,
		MetaKey:     msg.MetaKey,
		PointerType: msg.PointerType,
		PointerID:   msg.PointerID,
		Pressure:    msg.Pressure,
	}
	session.inputMu.Lock()
	defer session.inputMu.Unlock()
	if session.input == nil {
		return
	}
	if err := session.input.Apply(event); err != nil {
		log.Printf("desktop input error: %v", err)
		return
	}
	if msg.InputType == "mouse_move" || msg.InputType == "mouse_down" || msg.InputType == "mouse_up" || msg.InputType == "wheel" {
		session.setCursor(x, y)
	}
}

func (c *Client) handleDesktopClose(sessionID string) {
	if sessionID == "" {
		return
	}
	c.mu.Lock()
	session := c.desktopSessions[sessionID]
	if session != nil {
		delete(c.desktopSessions, sessionID)
	}
	c.mu.Unlock()
	if session != nil {
		session.close()
	}
}

// Each native capturer remains on its owning OS thread. Input uses the existing
// lightweight control path; screenshots neither start a ticker nor a video codec.
func (c *Client) runDesktopSnapshots(id string, session *desktopSession, capturer desktopCapturer, size image.Point, quality int) {
	idle := time.NewTimer(2 * time.Minute)
	defer idle.Stop()
	var scratch *image.RGBA
	for {
		select {
		case <-session.stop:
			return
		case <-idle.C:
			c.send(&protocol.Message{Type: protocol.MsgDesktopClose, SessionID: id})
			return
		case <-session.snapshot:
			idle.Reset(2 * time.Minute)
			img, err := capturer.Capture()
			if err == nil {
				var frame *image.RGBA
				frame, err = resizeDesktopFrameToSize(scratch, img, size.X, size.Y)
				if err == nil {
					if frame != img {
						scratch = frame
					}
					session.setFrame(capturer.Bounds(), size.X, size.Y)
					var encoded bytes.Buffer
					err = jpeg.Encode(&encoded, frame, &jpeg.Options{Quality: quality})
					if err == nil {
						err = c.sendBinary(protocol.BinDesktopFrame, id, encoded.Bytes())
					}
				}
			}
			if err != nil {
				c.send(&protocol.Message{Type: protocol.MsgDesktopClose, SessionID: id, Error: err.Error()})
				return
			}
		}
	}
}

func (c *Client) handleDesktopSnapshot(msg *protocol.Message) {
	c.mu.Lock()
	session := c.desktopSessions[msg.SessionID]
	c.mu.Unlock()
	if session == nil {
		c.send(&protocol.Message{Type: protocol.MsgDesktopClose, SessionID: msg.SessionID, Error: "capture session expired"})
		return
	}
	select {
	case session.snapshot <- struct{}{}:
	default:
	}
}

func (s *desktopSession) stopped() bool {
	select {
	case <-s.stop:
		return true
	default:
		return false
	}
}

// Setup can finish after the viewer has already closed the session.
func (s *desktopSession) installInput(input desktopInput, clipboard desktopClipboard) bool {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	if s.stopped() {
		if input != nil {
			_ = input.Close()
		}
		return false
	}
	s.input, s.clipboard = input, clipboard
	return true
}

func (s *desktopSession) close() {
	s.closeOnce.Do(func() {
		close(s.stop)
		s.inputMu.Lock()
		if s.input != nil {
			if err := s.input.Close(); err != nil {
				log.Printf("desktop input cleanup: %v", err)
			}
			s.input = nil
		}
		s.clipboard = nil
		s.inputMu.Unlock()
		s.closeRTC()
		s.closeCua()
	})
}

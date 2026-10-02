package client

import (
	"encoding/base64"
	"errors"
	"fmt"
	"image"
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
	input     desktopInput
	clipboard desktopClipboard

	inputMu  sync.Mutex
	frameMu  sync.RWMutex
	bounds   image.Rectangle
	frameW   int
	frameH   int
	cursor   image.Point
	cursorOK bool
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
	mappedX := bounds.Min.X + x*bounds.Dx()/frameW
	mappedY := bounds.Min.Y + y*bounds.Dy()/frameH
	if mappedX < bounds.Min.X {
		mappedX = bounds.Min.X
	}
	if mappedY < bounds.Min.Y {
		mappedY = bounds.Min.Y
	}
	if mappedX >= bounds.Max.X {
		mappedX = bounds.Max.X - 1
	}
	if mappedY >= bounds.Max.Y {
		mappedY = bounds.Max.Y - 1
	}
	return mappedX, mappedY
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
	session := &desktopSession{stop: make(chan struct{})}
	c.desktopSessions[msg.SessionID] = session
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		if current, ok := c.desktopSessions[msg.SessionID]; ok && current == session {
			delete(c.desktopSessions, msg.SessionID)
		}
		c.mu.Unlock()
	}()

	availableInputs := desktopInputBackends()
	inputBackend := chooseDesktopInputBackend(msg.InputBackend, availableInputs)
	if inputBackend != "" {
		if input, err := newDesktopInput(inputBackend); err == nil {
			session.input = input
			inputBackend = input.Backend()
			defer input.Close()
		} else {
			log.Printf("desktop input backend %s unavailable: %v", inputBackend, err)
			inputBackend = ""
		}
	}
	if clipboard, err := newDesktopClipboard(); err == nil {
		session.clipboard = clipboard
	} else {
		log.Printf("desktop clipboard unavailable: %v", err)
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
	if boundsInput, ok := session.input.(desktopInputBounds); ok {
		boundsInput.SetBounds(bounds)
	}
	caps := desktopCapabilities()
	caps.Supported = true
	caps.ViewOnly = false
	caps.Input = session.input != nil
	if len(availableInputs) > 0 {
		caps.InputBackends = append([]string(nil), availableInputs...)
		caps.InputOptions = desktopInputOptions()
	}
	caps.Clipboard = session.clipboard != nil
	caps.Reason = ""
	size := scaledDimension(bounds.Dx(), bounds.Dy(), msg.Width, msg.Height)
	if normalizeDesktopFormat(msg.Format) == desktopFormatH264 {
		size.X, size.Y = normalizeH264FrameSize(size.X, size.Y)
	}
	session.setFrame(bounds, size.X, size.Y)
	sourceID := desktopSourceLabel(msg.Source)
	if reporter, ok := capturer.(desktopSourceReporter); ok {
		if source := reporter.Source(); source.ID != "" {
			sourceID = source.ID
		}
	}

	fps := msg.FPS
	if fps <= 0 {
		fps = 2
	}
	if fps > 12 {
		fps = 12
	}
	quality := msg.Quality
	if quality <= 0 {
		quality = 60
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
	requestedFormat := normalizeDesktopFormat(msg.Format)
	encoder := newDesktopEncoder(requestedFormat, size.X, size.Y, quality, fps)
	defer encoder.Close()
	codecInfo := encoder.CodecInfo()
	format := encoder.Format()
	if requestedFormat == desktopFormatH264 && format != desktopFormatH264 {
		log.Printf("desktop h264 encoder unavailable for source %s, falling back to mjpeg", sourceName)
	}
	c.send(&protocol.Message{
		Type:                protocol.MsgDesktopReady,
		SessionID:           msg.SessionID,
		DesktopCapabilities: caps,
		Width:               size.X,
		Height:              size.Y,
		Format:              format,
		Codec:               codecInfo.Codec,
		Description:         codecInfo.Description,
		Source:              sourceID,
		FPS:                 fps,
		InputBackend:        inputBackend,
	})
	sender := newDesktopFrameSender(encoder)
	var frameScratch *image.RGBA
	ticker := time.NewTicker(time.Second / time.Duration(fps))
	defer ticker.Stop()
	for {
		select {
		case <-session.stop:
			return
		case <-ticker.C:
			img, err := capturer.Capture()
			if err != nil {
				err = fmt.Errorf("desktop capture failed for source %s: %w", sourceName, err)
				log.Printf("%v", err)
				c.send(&protocol.Message{Type: protocol.MsgDesktopClose, SessionID: msg.SessionID, Error: err.Error()})
				return
			}
			frame, err := resizeDesktopFrameInto(frameScratch, img, size.X, size.Y)
			if err != nil && !errors.Is(err, errResizeNoPixels) {
				log.Printf("desktop resize error for source %s: %v", sourceName, err)
				continue
			}
			// Keep the scratch buffer only when the helper allocated it;
			// the zero-copy path returns the capturer's own image.
			if frame != img {
				frameScratch = frame
			}
			if err != nil {
				continue
			}
			bounds := capturer.Bounds()
			if msg.ShowCursor {
				if cursor, ok := desktopCursorPosition(session, capturer); ok {
					overlayDesktopCursor(frame, bounds, cursor)
				}
			}
			session.setFrame(bounds, size.X, size.Y)
			payload, send, err := sender.tick(frame, time.Now())
			if err != nil {
				log.Printf("desktop encode error for source %s: %v", sourceName, err)
				continue
			}
			if !send {
				continue
			}
			started := time.Now()
			if err := c.sendBinary(protocol.BinDesktopFrame, msg.SessionID, payload); err != nil {
				log.Printf("desktop frame send error for source %s: %s", sourceName, err)
				return
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

func (c *Client) handleDesktopClipboard(msg *protocol.Message) {
	if msg.SessionID == "" {
		return
	}
	c.mu.Lock()
	session := c.desktopSessions[msg.SessionID]
	c.mu.Unlock()
	if session == nil || session.clipboard == nil {
		return
	}

	switch msg.ClipboardAction {
	case "get":
		items, err := session.clipboard.Get()
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
		if err := session.clipboard.Set(items); err != nil {
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
	if msg.InputType == "mouse_move" || msg.InputType == "mouse_down" || msg.InputType == "mouse_up" || msg.InputType == "wheel" || msg.InputType == "cursor_move" {
		session.setCursor(x, y)
	}
	if msg.InputType == "cursor_move" || session.input == nil {
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
	if err := session.input.Apply(event); err != nil {
		log.Printf("desktop input error: %v", err)
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
		close(session.stop)
	}
}

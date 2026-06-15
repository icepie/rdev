package client

import (
	"bytes"
	"image"
	"image/jpeg"
	"log"
	"runtime"
	"time"

	"rdev/internal/protocol"
)

type desktopCapturer interface {
	Bounds() image.Rectangle
	Capture() (image.Image, error)
	Close() error
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
	stop := make(chan struct{})
	c.desktopSessions[msg.SessionID] = stop
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		if current, ok := c.desktopSessions[msg.SessionID]; ok && current == stop {
			delete(c.desktopSessions, msg.SessionID)
		}
		c.mu.Unlock()
	}()

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	capturer, err := newDesktopCapturer()
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
	caps := desktopCapabilities()
	caps.Supported = true
	caps.ViewOnly = true
	caps.Input = false
	caps.Clipboard = false
	caps.Reason = ""
	c.send(&protocol.Message{
		Type:                protocol.MsgDesktopReady,
		SessionID:           msg.SessionID,
		DesktopCapabilities: caps,
		Width:               bounds.Dx(),
		Height:              bounds.Dy(),
		Format:              "jpeg",
	})

	fps := msg.FPS
	if fps <= 0 {
		fps = 2
	}
	if fps > 10 {
		fps = 10
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

	ticker := time.NewTicker(time.Second / time.Duration(fps))
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			img, err := capturer.Capture()
			if err != nil {
				log.Printf("desktop capture error: %v", err)
				c.send(&protocol.Message{Type: protocol.MsgDesktopClose, SessionID: msg.SessionID, Error: err.Error()})
				return
			}
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
				log.Printf("desktop encode error: %v", err)
				continue
			}
			if err := c.sendBinary(protocol.BinDesktopFrame, msg.SessionID, buf.Bytes()); err != nil {
				return
			}
		}
	}
}

func (c *Client) handleDesktopClose(sessionID string) {
	if sessionID == "" {
		return
	}
	c.mu.Lock()
	stop := c.desktopSessions[sessionID]
	if stop != nil {
		delete(c.desktopSessions, sessionID)
	}
	c.mu.Unlock()
	if stop != nil {
		close(stop)
	}
}

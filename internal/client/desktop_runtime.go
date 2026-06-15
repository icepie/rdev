package client

import (
	"bytes"
	"hash/crc32"
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
	caps := desktopCapabilities()
	caps.Supported = true
	caps.ViewOnly = true
	caps.Input = false
	caps.Clipboard = false
	caps.Reason = ""
	size := scaledDimension(bounds.Dx(), bounds.Dy(), msg.Width, msg.Height)
	c.send(&protocol.Message{
		Type:                protocol.MsgDesktopReady,
		SessionID:           msg.SessionID,
		DesktopCapabilities: caps,
		Width:               size.X,
		Height:              size.Y,
		Format:              "jpeg",
		Source:              desktopSourceLabel(msg.Source),
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

	var lastChecksum uint32
	lastSent := time.Now().Add(-time.Hour)
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
			frame := resizeDesktopFrame(img, msg.Width, msg.Height)
			checksum := crc32.ChecksumIEEE(frame.Pix)
			if checksum == lastChecksum && time.Since(lastSent) < 2*time.Second {
				continue
			}
			lastChecksum = checksum
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, frame, &jpeg.Options{Quality: quality}); err != nil {
				log.Printf("desktop encode error: %v", err)
				continue
			}
			if err := c.sendBinary(protocol.BinDesktopFrame, msg.SessionID, buf.Bytes()); err != nil {
				return
			}
			lastSent = time.Now()
		}
	}
}

func desktopSourceLabel(source string) string {
	if source == "" || source == "auto" {
		return "auto"
	}
	return source
}

func resizeDesktopFrame(img image.Image, maxWidth, maxHeight int) *image.RGBA {
	bounds := img.Bounds()
	sourceWidth := bounds.Dx()
	sourceHeight := bounds.Dy()
	size := scaledDimension(sourceWidth, sourceHeight, maxWidth, maxHeight)
	dst := image.NewRGBA(image.Rect(0, 0, size.X, size.Y))
	if sourceWidth <= 0 || sourceHeight <= 0 || size.X <= 0 || size.Y <= 0 {
		return dst
	}
	if src, ok := img.(*image.RGBA); ok {
		for y := 0; y < size.Y; y++ {
			sourceY := bounds.Min.Y + y*sourceHeight/size.Y
			for x := 0; x < size.X; x++ {
				sourceX := bounds.Min.X + x*sourceWidth/size.X
				sourceOffset := src.PixOffset(sourceX, sourceY)
				destOffset := dst.PixOffset(x, y)
				copy(dst.Pix[destOffset:destOffset+4], src.Pix[sourceOffset:sourceOffset+4])
			}
		}
		return dst
	}
	for y := 0; y < size.Y; y++ {
		sourceY := bounds.Min.Y + y*sourceHeight/size.Y
		for x := 0; x < size.X; x++ {
			sourceX := bounds.Min.X + x*sourceWidth/size.X
			r, g, b, a := img.At(sourceX, sourceY).RGBA()
			offset := dst.PixOffset(x, y)
			dst.Pix[offset+0] = byte(r >> 8)
			dst.Pix[offset+1] = byte(g >> 8)
			dst.Pix[offset+2] = byte(b >> 8)
			dst.Pix[offset+3] = byte(a >> 8)
		}
	}
	return dst
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

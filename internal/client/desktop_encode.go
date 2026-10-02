package client

import (
	"bytes"
	"errors"
	"hash/crc32"
	"image"
	"image/jpeg"
	"time"
)

// Desktop stream formats negotiated via desktop_start.Format.
const (
	desktopFormatMJPEG = "mjpeg"
	desktopFormatH264  = "h264"

	// desktopIdleResendInterval is how often an unchanged MJPEG frame is
	// resent as a keep-alive so a viewer can recover a lost or cleared canvas.
	desktopIdleResendInterval = 2 * time.Second
)

func normalizeDesktopFormat(value string) string {
	switch value {
	case desktopFormatH264:
		return desktopFormatH264
	case desktopFormatMJPEG:
		return desktopFormatMJPEG
	default:
		return ""
	}
}

// normalizeH264FrameSize makes a requested maximum size safe for 4:2:0 H.264
// without ever exceeding it. A dimension below two cannot represent an H.264
// frame and is left untouched for the normal request validation to reject.
func normalizeH264FrameSize(width, height int) (int, int) {
	if width >= 2 {
		width &^= 1
	}
	if height >= 2 {
		height &^= 1
	}
	return width, height
}

type desktopCodecInfo struct {
	Codec       string
	Description []byte
}

// desktopEncoder turns captured RGBA frames into stream payloads.
type desktopEncoder interface {
	Format() string
	CodecInfo() desktopCodecInfo
	Encode(frame *image.RGBA, out *bytes.Buffer) error
	Close()
}

type mjpegEncoder struct{ quality int }

func (e *mjpegEncoder) Format() string              { return desktopFormatMJPEG }
func (e *mjpegEncoder) CodecInfo() desktopCodecInfo { return desktopCodecInfo{} }
func (e *mjpegEncoder) Close()                      {}

func (e *mjpegEncoder) Encode(frame *image.RGBA, out *bytes.Buffer) error {
	return jpeg.Encode(out, frame, &jpeg.Options{Quality: e.quality})
}

// newDesktopEncoder picks the requested encoder, falling back to MJPEG when
// the H.264 backend is unavailable on this machine.
func newDesktopEncoder(requestedFormat string, width, height, quality, fps int) desktopEncoder {
	if normalizeDesktopFormat(requestedFormat) == desktopFormatH264 {
		if enc, err := newX264Encoder(width, height, quality, fps); err == nil {
			return enc
		}
	}
	return &mjpegEncoder{quality: quality}
}

// desktopFrameSender encodes captured desktop frames and decides when a frame
// is worth sending. MJPEG frames whose pixels are unchanged are skipped
// inside the keep-alive window and resent outside it; keep-alive resends
// reuse the cached JPEG payload instead of re-encoding. H.264 encodes every
// tick because P-frames must follow the full frame sequence.
type desktopFrameSender struct {
	encoder   desktopEncoder
	encodeBuf bytes.Buffer

	lastChecksum uint32
	lastPayload  []byte
	lastSent     time.Time
}

func newDesktopFrameSender(encoder desktopEncoder) *desktopFrameSender {
	return &desktopFrameSender{encoder: encoder, lastSent: time.Now().Add(-time.Hour)}
}

// tick encodes frame and reports the payload to send and whether a send
// should happen. Identical MJPEG frames inside the keep-alive window produce
// (nil, false, nil); identical MJPEG frames outside the window resend the
// cached payload without re-encoding.
func (s *desktopFrameSender) tick(frame *image.RGBA, now time.Time) (payload []byte, send bool, err error) {
	if s.encoder.Format() != desktopFormatMJPEG {
		s.encodeBuf.Reset()
		if err := s.encoder.Encode(frame, &s.encodeBuf); err != nil {
			return nil, false, err
		}
		return s.encodeBuf.Bytes(), true, nil
	}
	checksum := crc32.ChecksumIEEE(frame.Pix)
	if checksum == s.lastChecksum && now.Sub(s.lastSent) < desktopIdleResendInterval {
		return nil, false, nil
	}
	changed := checksum != s.lastChecksum || len(s.lastPayload) == 0
	s.lastChecksum = checksum
	s.lastSent = now
	if !changed {
		return s.lastPayload, true, nil
	}
	s.encodeBuf.Reset()
	if err := s.encoder.Encode(frame, &s.encodeBuf); err != nil {
		return nil, false, err
	}
	s.lastPayload = append(s.lastPayload[:0], s.encodeBuf.Bytes()...)
	return s.lastPayload, true, nil
}

// errResizeNoPixels reports an unusable source image for the resize helpers.
var errResizeNoPixels = errors.New("desktop frame has no pixels")

// resizeDesktopFrameInto resizes img to fit maxWidth/maxHeight into dst,
// reusing dst when its geometry already matches. It returns img itself when
// no scaling is needed and img is a zero-origin *image.RGBA; callers should
// treat the result as valid only until the next capture.
func resizeDesktopFrameInto(dst *image.RGBA, img image.Image, maxWidth, maxHeight int) (*image.RGBA, error) {
	bounds := img.Bounds()
	sourceWidth := bounds.Dx()
	sourceHeight := bounds.Dy()
	size := scaledDimension(sourceWidth, sourceHeight, maxWidth, maxHeight)
	if sourceWidth == size.X && sourceHeight == size.Y {
		if src, ok := img.(*image.RGBA); ok && bounds.Min.X == 0 && bounds.Min.Y == 0 {
			return src, nil
		}
	}
	if dst == nil || dst.Bounds().Dx() != size.X || dst.Bounds().Dy() != size.Y || dst.Bounds().Min.X != 0 || dst.Bounds().Min.Y != 0 {
		dst = image.NewRGBA(image.Rect(0, 0, size.X, size.Y))
	}
	if sourceWidth <= 0 || sourceHeight <= 0 || size.X <= 0 || size.Y <= 0 {
		return dst, errResizeNoPixels
	}
	if src, ok := img.(*image.RGBA); ok {
		parallelDesktopRows(size.X, size.Y, func(y0, y1 int) {
			for y := y0; y < y1; y++ {
				sourceY := bounds.Min.Y + y*sourceHeight/size.Y
				for x := range size.X {
					sourceX := bounds.Min.X + x*sourceWidth/size.X
					sourceOffset := src.PixOffset(sourceX, sourceY)
					destOffset := dst.PixOffset(x, y)
					copy(dst.Pix[destOffset:destOffset+4], src.Pix[sourceOffset:sourceOffset+4])
				}
			}
		})
		return dst, nil
	}
	parallelDesktopRows(size.X, size.Y, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			sourceY := bounds.Min.Y + y*sourceHeight/size.Y
			for x := range size.X {
				sourceX := bounds.Min.X + x*sourceWidth/size.X
				r, g, b, a := img.At(sourceX, sourceY).RGBA()
				offset := dst.PixOffset(x, y)
				dst.Pix[offset+0] = byte(r >> 8)
				dst.Pix[offset+1] = byte(g >> 8)
				dst.Pix[offset+2] = byte(b >> 8)
				dst.Pix[offset+3] = byte(a >> 8)
			}
		}
	})
	return dst, nil
}

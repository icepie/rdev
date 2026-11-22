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

func normalizeDesktopFrameSize(format string, size image.Point) image.Point {
	if format != desktopFormatH264 {
		return size
	}
	if size.X < 2 {
		size.X = 2
	} else {
		size.X &^= 1
	}
	if size.Y < 2 {
		size.Y = 2
	} else {
		size.Y &^= 1
	}
	return size
}

type desktopCodecInfo struct {
	Codec       string
	Description []byte
}

// desktopEncoder turns captured RGBA frames into stream payloads.
type desktopEncoder interface {
	Format() string
	Backend() string
	CodecInfo() desktopCodecInfo
	Encode(frame *image.RGBA, out *bytes.Buffer) error
	Close()
}

type mjpegEncoder struct{ quality int }

func (e *mjpegEncoder) Format() string              { return desktopFormatMJPEG }
func (e *mjpegEncoder) Backend() string             { return "jpeg" }
func (e *mjpegEncoder) CodecInfo() desktopCodecInfo { return desktopCodecInfo{} }
func (e *mjpegEncoder) Close()                      {}

// Go's Media Foundation and VideoToolbox implementations require hardware
// when opening the encoder. The framework name alone is not proof on other clients.
func desktopEncoderAcceleration(backend string) string {
	switch backend {
	case "cpu", "jpeg":
		return "software"
	case "nvenc", "vaapi", "mediafoundation", "videotoolbox":
		return "hardware"
	default:
		return ""
	}
}

func desktopEncoderReady(encoder desktopEncoder) bool {
	return encoder.Format() != desktopFormatH264 || encoder.CodecInfo().Codec != ""
}

func (e *mjpegEncoder) Encode(frame *image.RGBA, out *bytes.Buffer) error {
	return jpeg.Encode(out, frame, &jpeg.Options{Quality: e.quality})
}

// desktopFrameSender encodes captured desktop frames and decides when a frame
// is worth sending. H.264 receives every frame so its prediction chain stays
// valid. Unchanged MJPEG frames are skipped inside the keep-alive window and
// resent outside it using the cached JPEG payload.
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
// should happen. H.264 always produces a new access unit. Identical MJPEG
// frames inside the keep-alive window produce (nil, false, nil); outside it,
// the cached payload is resent without re-encoding.
func (s *desktopFrameSender) tick(frame *image.RGBA, now time.Time) (payload []byte, send bool, err error) {
	if s.encoder.Format() == desktopFormatH264 {
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

// reusableDesktopRGBA returns a zero-origin RGBA frame with the requested
// geometry. Capture backends overwrite every pixel before publishing it, so
// retaining one backing buffer avoids a full-frame allocation on every tick.
func reusableDesktopRGBA(frame *image.RGBA, width, height int) *image.RGBA {
	if frame == nil || frame.Bounds() != image.Rect(0, 0, width, height) {
		return image.NewRGBA(image.Rect(0, 0, width, height))
	}
	return frame
}
func copyDesktopRGBA(dst, src *image.RGBA) *image.RGBA {
	bounds := src.Bounds()
	dst = reusableDesktopRGBA(dst, bounds.Dx(), bounds.Dy())
	for y := 0; y < bounds.Dy(); y++ {
		sourceOffset := src.PixOffset(bounds.Min.X, bounds.Min.Y+y)
		destOffset := dst.PixOffset(0, y)
		copy(dst.Pix[destOffset:destOffset+bounds.Dx()*4], src.Pix[sourceOffset:sourceOffset+bounds.Dx()*4])
	}
	return dst
}

// errResizeNoPixels reports an unusable source image for the resize helpers.
var errResizeNoPixels = errors.New("desktop frame has no pixels")

// resizeDesktopFrameToSize scales img to exactly width x height with nearest
// neighbor, reusing dst when its geometry already matches. It returns img
// itself when img already has the target size and is a zero-origin
// *image.RGBA; callers should treat the result as valid only until the next
// capture.
//
// The target geometry is authoritative: H.264 frames must match the encoder
// size exactly, so callers must pass the same size the encoder was created
// with instead of re-deriving a proportional size from it.
func resizeDesktopFrameToSize(dst *image.RGBA, img image.Image, width, height int) (*image.RGBA, error) {
	bounds := img.Bounds()
	sourceWidth := bounds.Dx()
	sourceHeight := bounds.Dy()
	if sourceWidth <= 0 || sourceHeight <= 0 || width <= 0 || height <= 0 {
		return dst, errResizeNoPixels
	}
	if sourceWidth == width && sourceHeight == height {
		if src, ok := img.(*image.RGBA); ok && bounds.Min.X == 0 && bounds.Min.Y == 0 {
			return src, nil
		}
	}
	if dst == nil || dst.Bounds().Dx() != width || dst.Bounds().Dy() != height || dst.Bounds().Min.X != 0 || dst.Bounds().Min.Y != 0 {
		dst = image.NewRGBA(image.Rect(0, 0, width, height))
	}
	if src, ok := img.(*image.RGBA); ok {
		parallelDesktopRows(width, height, func(y0, y1 int) {
			for y := y0; y < y1; y++ {
				sourceY := bounds.Min.Y + y*sourceHeight/height
				for x := range width {
					sourceX := bounds.Min.X + x*sourceWidth/width
					sourceOffset := src.PixOffset(sourceX, sourceY)
					destOffset := dst.PixOffset(x, y)
					copy(dst.Pix[destOffset:destOffset+4], src.Pix[sourceOffset:sourceOffset+4])
				}
			}
		})
		return dst, nil
	}
	parallelDesktopRows(width, height, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			sourceY := bounds.Min.Y + y*sourceHeight/height
			for x := range width {
				sourceX := bounds.Min.X + x*sourceWidth/width
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

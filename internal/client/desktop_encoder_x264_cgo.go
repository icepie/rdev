//go:build x264cgo

package client

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"

	x264 "github.com/gen2brain/x264-go"
)

// x264CGOEncoder embeds x264 through x264-go. It is intentionally isolated
// behind the x264cgo build tag so the default client remains CGO-free.
type x264CGOEncoder struct {
	encoder   *x264.Encoder
	payload   bytes.Buffer
	width     int
	height    int
	codecInfo desktopCodecInfo
}

func newDesktopEncoder(requestedFormat string, width, height, quality, fps int) desktopEncoder {
	if normalizeDesktopFormat(requestedFormat) != desktopFormatH264 {
		return &mjpegEncoder{quality: quality}
	}
	encoder, err := newX264CGOEncoder(width, height, quality, fps)
	if err != nil {
		return &mjpegEncoder{quality: quality}
	}
	return encoder
}

func newX264CGOEncoder(width, height, quality, fps int) (*x264CGOEncoder, error) {
	if width < 2 || height < 2 || width&1 != 0 || height&1 != 0 {
		return nil, fmt.Errorf("H.264 requires positive even desktop frame dimensions, got %dx%d", width, height)
	}
	if fps <= 0 {
		fps = 2
	}
	if fps > 12 {
		fps = 12
	}
	crf := 38 - float32(quality)*0.22
	if crf < 18 {
		crf = 18
	}
	if crf > 32 {
		crf = 32
	}

	encoder := &x264CGOEncoder{width: width, height: height}
	options := &x264.Options{
		Width:        width,
		Height:       height,
		FrameRate:    fps,
		Tune:         "zerolatency",
		Preset:       "veryfast",
		Profile:      "baseline",
		KeyInt:       max(20, fps*2),
		RateControl:  "crf",
		RateConstant: crf,
		LogLevel:     x264.LogNone,
	}
	var err error
	encoder.encoder, err = x264.NewEncoder(&encoder.payload, options)
	if err != nil {
		return nil, fmt.Errorf("create x264 encoder: %w", err)
	}
	codec, description, err := cgoX264CodecInfo(encoder.payload.Bytes())
	if err != nil {
		encoder.Close()
		return nil, err
	}
	encoder.payload.Reset()
	encoder.codecInfo = desktopCodecInfo{Codec: codec, Description: description}
	return encoder, nil
}

func (e *x264CGOEncoder) Format() string { return desktopFormatH264 }

func (e *x264CGOEncoder) CodecInfo() desktopCodecInfo { return e.codecInfo }

func (e *x264CGOEncoder) Close() {
	if e.encoder == nil {
		return
	}
	_ = e.encoder.Close()
	e.encoder = nil
}

// Encode sends one AVCC access unit with the existing leading keyframe byte.
func (e *x264CGOEncoder) Encode(frame *image.RGBA, out *bytes.Buffer) error {
	if e.encoder == nil {
		return errors.New("x264 encoder is closed")
	}
	if frame == nil || frame.Bounds().Dx() != e.width || frame.Bounds().Dy() != e.height {
		return fmt.Errorf("x264 frame size does not match encoder %dx%d", e.width, e.height)
	}
	e.payload.Reset()
	if err := e.encoder.Encode(frame); err != nil {
		return fmt.Errorf("x264 encode frame: %w", err)
	}
	annexB := e.payload.Bytes()
	if len(annexB) == 0 {
		return errors.New("x264 encoder produced empty access unit")
	}
	start := out.Len()
	out.WriteByte(0)
	keyframe, err := appendAnnexBToAVCC(out, annexB)
	if err != nil {
		return err
	}
	if keyframe {
		out.Bytes()[start] = 1
	}
	return nil
}

func cgoX264CodecInfo(annexB []byte) (string, []byte, error) {
	nals, err := splitAnnexBNALs(annexB)
	if err != nil {
		return "", nil, err
	}
	var sps, pps []byte
	for _, nal := range nals {
		if len(nal) == 0 {
			continue
		}
		switch nal[0] & 0x1f {
		case 7:
			sps = bytes.Clone(nal)
		case 8:
			pps = bytes.Clone(nal)
		}
	}
	if len(sps) < 4 || len(pps) == 0 {
		return "", nil, errors.New("x264 headers missing SPS/PPS")
	}
	return cgoAVCCodecString(sps), cgoBuildAVCC(sps, pps), nil
}

func cgoAVCCodecString(sps []byte) string {
	const hexDigits = "0123456789ABCDEF"
	out := make([]byte, 0, 14)
	out = append(out, "avc1."...)
	for _, b := range sps[1:4] {
		out = append(out, hexDigits[b>>4], hexDigits[b&0x0f])
	}
	return string(out)
}

func cgoBuildAVCC(sps, pps []byte) []byte {
	out := make([]byte, 0, 11+len(sps)+5+len(pps))
	out = append(out, 0x01, sps[1], sps[2], sps[3], 0xff, 0xe1)
	var length [2]byte
	binary.BigEndian.PutUint16(length[:], uint16(len(sps)))
	out = append(out, length[:]...)
	out = append(out, sps...)
	out = append(out, 0x01)
	binary.BigEndian.PutUint16(length[:], uint16(len(pps)))
	out = append(out, length[:]...)
	out = append(out, pps...)
	return out
}

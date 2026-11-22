package client

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"sync/atomic"

	h264 "github.com/oops1/go.264"
)

type go264DesktopEncoder struct {
	encoder   *h264.Encoder
	backend   string
	codecInfo desktopCodecInfo
	i420      []byte
	width     int
	height    int
}

var go264HardwareDisabled atomic.Bool

func newDesktopEncoder(format string, width, height, quality, fps int) desktopEncoder {
	if format == desktopFormatH264 {
		if encoder, err := newGo264DesktopEncoder(width, height, quality, fps); err == nil {
			return encoder
		}
	}
	return &mjpegEncoder{quality: quality}
}

func newGo264DesktopEncoder(width, height, quality, fps int) (*go264DesktopEncoder, error) {
	return openGo264DesktopEncoder(width, height, quality, fps, false)
}

func newSoftwareGo264DesktopEncoder(width, height, quality, fps int) (*go264DesktopEncoder, error) {
	return openGo264DesktopEncoder(width, height, quality, fps, true)
}

func openGo264DesktopEncoder(width, height, quality, fps int, forceSoftware bool) (*go264DesktopEncoder, error) {
	forceSoftware = forceSoftware || go264HardwareDisabled.Load()
	config := h264.EncoderConfig{
		Width:               width,
		Height:              height,
		FPSNum:              fps,
		FPSDen:              1,
		GOPSize:             max(1, fps),
		QP:                  go264QP(quality),
		RefFrames:           1,
		MotionSearch:        h264.MotionSearchHalf,
		ModeDecision:        h264.ModeDecisionFast,
		RepeatParameterSets: true,
		ForceSoftware:       forceSoftware,
	}
	encoder, err := h264.NewEncoder(config)
	if err != nil {
		return nil, err
	}
	backend := encoder.Backend()

	var codecInfo desktopCodecInfo
	headers, err := encoder.Headers()
	if err == nil {
		codecInfo, err = desktopAVCCodecInfo(headers)
	} else if errors.Is(err, h264.ErrHeadersUnavailable) {
		err = nil
	} else {
		encoder.Close()
		return nil, err
	}
	if err != nil {
		encoder.Close()
		return nil, err
	}
	return &go264DesktopEncoder{
		encoder:   encoder,
		backend:   backend,
		codecInfo: codecInfo,
		i420:      make([]byte, width*height*3/2),
		width:     width,
		height:    height,
	}, nil
}

func (e *go264DesktopEncoder) Format() string              { return desktopFormatH264 }
func (e *go264DesktopEncoder) Backend() string             { return e.backend }
func (e *go264DesktopEncoder) CodecInfo() desktopCodecInfo { return e.codecInfo }
func (e *go264DesktopEncoder) Close() {
	if e.encoder != nil {
		e.encoder.Close()
		e.encoder = nil
	}
}

func (e *go264DesktopEncoder) Encode(frame *image.RGBA, out *bytes.Buffer) error {
	if e.encoder == nil {
		return h264.ErrClosed
	}
	if frame == nil || frame.Bounds().Dx() != e.width || frame.Bounds().Dy() != e.height {
		return fmt.Errorf("desktop h264 frame size is not %dx%d", e.width, e.height)
	}
	rgbaToI420(e.i420, frame)
	packet, err := e.encoder.Encode(e.i420)
	if err != nil {
		e.disableFailedHardware()
		return err
	}
	if e.codecInfo.Codec == "" {
		headers, headerErr := e.encoder.Headers()
		if headerErr != nil {
			e.disableFailedHardware()
			return headerErr
		}
		e.codecInfo, headerErr = desktopAVCCodecInfo(headers)
		if headerErr != nil {
			e.disableFailedHardware()
			return headerErr
		}
	}
	return writeDesktopAVCCPayload(out, packet)
}

func (e *go264DesktopEncoder) disableFailedHardware() {
	if e.backend != "" && e.backend != "cpu" {
		go264HardwareDisabled.Store(true)
	}
}

func go264QP(quality int) int {
	qp := 40 - quality/4
	if qp < 18 {
		return 18
	}
	if qp > 35 {
		return 35
	}
	return qp
}

func rgbaToI420(dst []byte, frame *image.RGBA) {
	width := frame.Bounds().Dx()
	height := frame.Bounds().Dy()
	lumaSize := width * height
	chromaWidth := width / 2
	yPlane := dst[:lumaSize]
	cbPlane := dst[lumaSize : lumaSize+lumaSize/4]
	crPlane := dst[lumaSize+lumaSize/4:]
	minX := frame.Bounds().Min.X
	minY := frame.Bounds().Min.Y

	parallelDesktopRows(width*2, height/2, func(cy0, cy1 int) {
		for cy := cy0; cy < cy1; cy++ {
			y0 := cy * 2
			for cx := 0; cx < chromaWidth; cx++ {
				x0 := cx * 2
				var cbSum, crSum uint16
				for dy := 0; dy < 2; dy++ {
					row := y0 + dy
					for dx := 0; dx < 2; dx++ {
						column := x0 + dx
						offset := frame.PixOffset(minX+column, minY+row)
						r, g, b := unpremultipliedRGBA(frame.Pix, offset)
						y, cb, cr := color.RGBToYCbCr(r, g, b)
						yPlane[row*width+column] = y
						cbSum += uint16(cb)
						crSum += uint16(cr)
					}
				}
				chromaOffset := cy*chromaWidth + cx
				cbPlane[chromaOffset] = byte((cbSum + 2) / 4)
				crPlane[chromaOffset] = byte((crSum + 2) / 4)
			}
		}
	})
}

func unpremultipliedRGBA(pixels []byte, offset int) (r, g, b uint8) {
	r, g, b = pixels[offset], pixels[offset+1], pixels[offset+2]
	alpha := pixels[offset+3]
	if alpha == 0 {
		return 0, 0, 0
	}
	if alpha == 255 {
		return r, g, b
	}
	r = uint8(min(255, uint32(r)*255/uint32(alpha)))
	g = uint8(min(255, uint32(g)*255/uint32(alpha)))
	b = uint8(min(255, uint32(b)*255/uint32(alpha)))
	return r, g, b
}

func desktopAVCCodecInfo(annexB []byte) (desktopCodecInfo, error) {
	var sps, pps []byte
	err := eachAnnexBNAL(annexB, func(nal []byte) error {
		if len(nal) == 0 {
			return nil
		}
		switch nal[0] & 0x1f {
		case 7:
			sps = nal
		case 8:
			pps = nal
		}
		return nil
	})
	if err != nil {
		return desktopCodecInfo{}, err
	}
	if len(sps) < 4 || len(pps) == 0 || len(sps) > 0xffff || len(pps) > 0xffff {
		return desktopCodecInfo{}, errors.New("desktop h264 encoder returned invalid SPS/PPS")
	}

	description := make([]byte, 0, 11+len(sps)+len(pps))
	description = append(description, 1, sps[1], sps[2], sps[3], 0xff, 0xe1)
	description = binary.BigEndian.AppendUint16(description, uint16(len(sps)))
	description = append(description, sps...)
	description = append(description, 1)
	description = binary.BigEndian.AppendUint16(description, uint16(len(pps)))
	description = append(description, pps...)
	return desktopCodecInfo{
		Codec:       fmt.Sprintf("avc1.%02X%02X%02X", sps[1], sps[2], sps[3]),
		Description: description,
	}, nil
}

func writeDesktopAVCCPayload(out *bytes.Buffer, annexB []byte) error {
	flagOffset := out.Len()
	out.WriteByte(0)
	nalCount := 0
	keyframe := false
	err := eachAnnexBNAL(annexB, func(nal []byte) error {
		if len(nal) == 0 {
			return nil
		}
		if uint64(len(nal)) > uint64(^uint32(0)) {
			return errors.New("desktop h264 NAL unit is too large")
		}
		if nal[0]&0x1f == 5 {
			keyframe = true
		}
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(nal)))
		out.Write(length[:])
		out.Write(nal)
		nalCount++
		return nil
	})
	if err != nil {
		return err
	}
	if nalCount == 0 {
		return errors.New("desktop h264 encoder returned no NAL units")
	}
	if keyframe {
		out.Bytes()[flagOffset] = 1
	}
	return nil
}

func eachAnnexBNAL(data []byte, visit func([]byte) error) error {
	start, prefix := annexBStartCode(data, 0)
	if start < 0 {
		return errors.New("desktop h264 encoder returned non-Annex-B data")
	}
	for {
		nalStart := start + prefix
		next, nextPrefix := annexBStartCode(data, nalStart)
		nalEnd := len(data)
		if next >= 0 {
			nalEnd = next
		}
		if nalEnd > nalStart {
			if err := visit(data[nalStart:nalEnd]); err != nil {
				return err
			}
		}
		if next < 0 {
			return nil
		}
		start, prefix = next, nextPrefix
	}
}

func annexBStartCode(data []byte, from int) (int, int) {
	for i := from; i+2 < len(data); i++ {
		if data[i] != 0 || data[i+1] != 0 {
			continue
		}
		if data[i+2] == 1 {
			return i, 3
		}
		if i+3 < len(data) && data[i+2] == 0 && data[i+3] == 1 {
			return i, 4
		}
	}
	return -1, 0
}

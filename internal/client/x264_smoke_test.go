package client

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"testing"
)

func TestX264SmokeEncode(t *testing.T) {
	enc, err := newX264Encoder(320, 240, 50, 12)
	if err != nil {
		t.Skipf("x264 unavailable: %v", err)
	}
	defer enc.Close()

	frame := image.NewRGBA(image.Rect(0, 0, 320, 240))
	var stream bytes.Buffer
	// Validate AVCC framing: every generated access unit is length-prefixed
	// and the decoder configuration supplies the matching SPS/PPS.
	for n := range 30 {
		for y := range 240 {
			for x := range 320 {
				v := uint8((x + y + n*8) & 0xff)
				frame.SetRGBA(x, y, color.RGBA{R: v, G: uint8(255 - x), B: uint8(y), A: 255})
			}
		}
		var au bytes.Buffer
		if err := enc.Encode(frame, &au); err != nil {
			t.Fatalf("encode %d: %v", n, err)
		}
		data := au.Bytes()
		key := data[0] == 1
		if n == 0 && !key {
			t.Fatal("first frame not keyframe")
		}
		// Convert 4-byte-length NALUs to annexb for ffmpeg verification.
		if n == 0 {
			// Prepend SPS/PPS from the avcC record (b_repeat_headers=0).
			desc := enc.CodecInfo().Description
			spsLen := int(binary.BigEndian.Uint16(desc[6:8]))
			sps := desc[8 : 8+spsLen]
			ppsLenOff := 8 + spsLen + 1
			ppsLen := int(binary.BigEndian.Uint16(desc[ppsLenOff : ppsLenOff+2]))
			pps := desc[ppsLenOff+2 : ppsLenOff+2+ppsLen]
			for _, nal := range [][]byte{sps, pps} {
				stream.Write([]byte{0, 0, 0, 1})
				stream.Write(nal)
			}
		}
		payload := data[1:]
		for len(payload) >= 4 {
			l := int(binary.BigEndian.Uint32(payload[:4]))
			if l <= 0 || 4+l > len(payload) {
				t.Fatalf("frame %d: bad NAL length %d (avail %d)", n, l, len(payload))
			}
			stream.Write([]byte{0, 0, 0, 1})
			stream.Write(payload[4 : 4+l])
			payload = payload[4+l:]
		}
	}
	info := enc.CodecInfo()
	t.Logf("codec=%s descLen=%d streamBytes=%d", info.Codec, len(info.Description), stream.Len())
}

func TestX264RejectsOddFrameDimensions(t *testing.T) {
	if _, ok := loadX264API(); !ok {
		t.Skip("x264 unavailable")
	}
	if _, err := newX264Encoder(321, 240, 50, 8); err == nil {
		t.Fatal("odd x264 width was accepted")
	}
	if _, err := newX264Encoder(320, 241, 50, 8); err == nil {
		t.Fatal("odd x264 height was accepted")
	}
}

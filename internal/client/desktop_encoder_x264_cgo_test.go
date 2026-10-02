//go:build x264cgo

package client

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"testing"
)

func TestX264CGOEncoderProducesAVCC(t *testing.T) {
	encoder, err := newX264CGOEncoder(320, 240, 50, 12)
	if err != nil {
		t.Fatalf("newX264CGOEncoder() error = %v", err)
	}
	defer encoder.Close()

	info := encoder.CodecInfo()
	if info.Codec == "" || len(info.Description) < 11 {
		t.Fatalf("CodecInfo() = %#v, want WebCodecs codec and avcC description", info)
	}
	frame := image.NewRGBA(image.Rect(0, 0, 320, 240))
	for y := range 240 {
		for x := range 320 {
			frame.SetRGBA(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: uint8(x + y), A: 0xff})
		}
	}
	var packet bytes.Buffer
	if err := encoder.Encode(frame, &packet); err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	data := packet.Bytes()
	if len(data) < 5 || data[0] != 1 {
		t.Fatalf("first packet header = %x, want keyframe followed by AVCC", data)
	}
	for data = data[1:]; len(data) > 0; {
		if len(data) < 4 {
			t.Fatalf("truncated AVCC length prefix: %x", data)
		}
		length := int(binary.BigEndian.Uint32(data[:4]))
		if length == 0 || length > len(data)-4 {
			t.Fatalf("invalid AVCC NAL length %d for %d bytes", length, len(data)-4)
		}
		data = data[4+length:]
	}
}

func TestX264CGOEncoderRejectsOddFrameDimensions(t *testing.T) {
	if _, err := newX264CGOEncoder(321, 240, 50, 8); err == nil {
		t.Fatal("odd x264 width was accepted")
	}
	if _, err := newX264CGOEncoder(320, 241, 50, 8); err == nil {
		t.Fatal("odd x264 height was accepted")
	}
}

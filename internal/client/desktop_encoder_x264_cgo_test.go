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

// TestX264CGOEncoderAcceptsRuntimeResizedFrames reproduces the live desktop
// failure where an odd capture height (1600x905) made normalizeH264FrameSize
// create a 1600x904 encoder while the pipeline resized frames proportionally
// to 1598x904, so every frame was rejected as "frame size does not match
// encoder".
func TestX264CGOEncoderAcceptsRuntimeResizedFrames(t *testing.T) {
	size := scaledDimension(1600, 905, 1600, 905)
	size.X, size.Y = normalizeH264FrameSize(size.X, size.Y)
	encoder, err := newX264CGOEncoder(size.X, size.Y, 50, 12)
	if err != nil {
		t.Fatalf("newX264CGOEncoder(%d, %d) error = %v", size.X, size.Y, err)
	}
	defer encoder.Close()

	source := image.NewRGBA(image.Rect(0, 0, 1600, 905))
	frame, err := resizeDesktopFrameToSize(nil, source, size.X, size.Y)
	if err != nil {
		t.Fatalf("resizeDesktopFrameToSize() error = %v", err)
	}
	var packet bytes.Buffer
	if err := encoder.Encode(frame, &packet); err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	if packet.Len() == 0 {
		t.Fatal("Encode() produced no payload")
	}
}

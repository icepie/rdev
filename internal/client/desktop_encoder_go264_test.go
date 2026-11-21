package client

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"os/exec"
	"strings"
	"testing"

	h264 "github.com/oops1/go.264"
)

func TestGo264DesktopEncoderProducesDecodableAVCCKeyframe(t *testing.T) {
	encoder, err := newGo264DesktopEncoder(64, 48, 60, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.Close()

	codecInfo := encoder.CodecInfo()
	if !strings.HasPrefix(codecInfo.Codec, "avc1.") || len(codecInfo.Description) < 11 || codecInfo.Description[0] != 1 {
		t.Fatalf("codec info = %#v, want AVCDecoderConfigurationRecord", codecInfo)
	}

	frame := image.NewRGBA(image.Rect(0, 0, 64, 48))
	for y := range 48 {
		for x := range 64 {
			frame.SetRGBA(x, y, color.RGBA{R: uint8(x * 4), G: uint8(y * 5), B: uint8((x + y) * 2), A: 255})
		}
	}
	var payload bytes.Buffer
	if err := encoder.Encode(frame, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Len() < 6 || payload.Bytes()[0] != 1 {
		t.Fatalf("payload starts % x, want keyframe flag and AVCC data", payload.Bytes())
	}

	annexB, types, err := testAVCCToAnnexB(payload.Bytes()[1:])
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []byte{7, 8, 5} {
		if !bytes.Contains(types, []byte{required}) {
			t.Fatalf("NAL types = %v, want type %d", types, required)
		}
	}

	decoder := h264.NewDecoderWithConfig(h264.DecoderConfig{ForceSoftware: true})
	defer decoder.Close()
	frames, err := decoder.Decode(annexB)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) == 0 {
		frames, err = decoder.Flush()
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(frames) != 1 || frames[0].Width != 64 || frames[0].Height != 48 {
		t.Fatalf("decoded frames = %#v, want one 64x48 frame", frames)
	}
}

func TestGo264DesktopStreamDecodesWithFFmpeg(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	const width, height, frameCount = 64, 48, 16
	encoder, err := newGo264DesktopEncoder(width, height, 60, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.Close()

	frame := image.NewRGBA(image.Rect(0, 0, width, height))
	var stream bytes.Buffer
	for n := range frameCount {
		for y := range height {
			for x := range width {
				frame.SetRGBA(x, y, color.RGBA{
					R: uint8(x*4 + n*7),
					G: uint8(y*5 + n*3),
					B: uint8((x+y)*2 + n*11),
					A: 255,
				})
			}
		}
		var payload bytes.Buffer
		if err := encoder.Encode(frame, &payload); err != nil {
			t.Fatalf("encode frame %d: %v", n, err)
		}
		annexB, _, err := testAVCCToAnnexB(payload.Bytes()[1:])
		if err != nil {
			t.Fatalf("convert frame %d: %v", n, err)
		}
		stream.Write(annexB)
	}

	cmd := exec.Command(ffmpeg, "-v", "error", "-f", "h264", "-i", "pipe:0",
		"-f", "rawvideo", "-pix_fmt", "yuv420p", "pipe:1")
	cmd.Stdin = bytes.NewReader(stream.Bytes())
	var decoded, stderr bytes.Buffer
	cmd.Stdout = &decoded
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg decode: %v: %s", err, stderr.String())
	}
	if want := width * height * 3 / 2 * frameCount; decoded.Len() != want {
		t.Fatalf("ffmpeg decoded %d bytes, want %d complete I420 frames", decoded.Len(), want)
	}
}

func testAVCCToAnnexB(avcc []byte) ([]byte, []byte, error) {
	annexB := make([]byte, 0, len(avcc))
	var types []byte
	for len(avcc) > 0 {
		if len(avcc) < 4 {
			return nil, nil, errTestInvalidAVCC
		}
		length := int(binary.BigEndian.Uint32(avcc[:4]))
		avcc = avcc[4:]
		if length <= 0 || length > len(avcc) {
			return nil, nil, errTestInvalidAVCC
		}
		annexB = append(annexB, 0, 0, 0, 1)
		annexB = append(annexB, avcc[:length]...)
		types = append(types, avcc[0]&0x1f)
		avcc = avcc[length:]
	}
	return annexB, types, nil
}

var errTestInvalidAVCC = errors.New("invalid AVCC payload")

package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"golang.org/x/image/bmp"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"rdev/internal/agent"
)

func TestImagePreviewIsBoundedAndPersistsWithoutOriginal(t *testing.T) {
	var buf bytes.Buffer
	png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1200, 800)))
	preview := agentImagePreview(buf.Bytes())
	if !strings.HasPrefix(preview, "data:image/png;base64,") {
		t.Fatal("missing preview")
	}
	data, err := base64.StdEncoding.DecodeString(strings.SplitN(preview, ",", 2)[1])
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width != 640 || cfg.Height > 640 {
		t.Fatalf("bad preview: %+v %v", cfg, err)
	}
	msg := agent.Message{Display: json.RawMessage(`{"steering":true,"time":1000}`), Role: "user", Images: []agent.MessageImage{{Name: "photo.png", Data: "original-not-persisted", Preview: preview}}}
	raw := marshalAgentHistoryMsg(msg)
	if bytes.Contains(raw, []byte("original-not-persisted")) {
		t.Fatal("original image leaked into history")
	}
	var line agentHistoryLine
	if err := json.Unmarshal(raw, &line); err != nil {
		t.Fatal(err)
	}
	if len(line.ImagePreviews) != 1 || line.ImagePreviews[0] != preview {
		t.Fatal("preview missing from history")
	}
	st := newTestStore(t)
	st.save("preview-device", "thread", "fixture", []agent.Message{msg})
	loaded, _, _, _ := st.load("preview-device", "thread")
	if len(loaded) != 1 || loaded[0].Images[0].Preview != preview || string(loaded[0].Display) != string(msg.Display) {
		t.Fatal("presentation metadata did not survive reload")
	}
	if agentImagePreview([]byte("invalid image")) != "" {
		t.Fatal("invalid image accepted")
	}
}

func TestAttachmentPreviewPreservesAlphaButToolPreviewRemainsJPEG(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 20, 20))
	source.SetNRGBA(10, 10, color.NRGBA{R: 255, A: 128})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, source); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, preview, mime string
		alpha               bool
	}{
		{"attachment", agentImagePreview(encoded.Bytes()), "png", true},
		{"tool", boundedAgentImagePreview(encoded.Bytes(), 8192), "jpeg", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.HasPrefix(tc.preview, "data:image/"+tc.mime+";base64,") {
				t.Fatalf("wrong preview MIME: %.30s", tc.preview)
			}
			raw, err := base64.StdEncoding.DecodeString(strings.SplitN(tc.preview, ",", 2)[1])
			if err != nil {
				t.Fatal(err)
			}
			decoded, _, err := image.Decode(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			_, _, _, corner := decoded.At(0, 0).RGBA()
			_, _, _, middle := decoded.At(10, 10).RGBA()
			if tc.alpha && (corner != 0 || middle != 128*257) {
				t.Fatalf("alpha lost: corner=%d middle=%d", corner, middle)
			}
			if !tc.alpha && corner != 65535 {
				t.Fatalf("tool JPEG not opaque: %d", corner)
			}
		})
	}
}

func TestBMPAttachmentPreviewAndEncodedSizeBound(t *testing.T) {
	source := image.NewRGBA(image.Rect(0, 0, 80, 80))
	for y := 0; y < 80; y++ {
		for x := 0; x < 80; x++ {
			source.Set(x, y, color.RGBA{R: uint8(x * 3), G: uint8(y * 3), A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := bmp.Encode(&encoded, source); err != nil {
		t.Fatal(err)
	}
	preview := agentImagePreview(encoded.Bytes())
	if !strings.HasPrefix(preview, "data:image/jpeg;base64,") {
		t.Fatal("BMP preview missing")
	}
	encoded.Reset()
	source.Set(0, 0, color.Transparent)
	if err := png.Encode(&encoded, source); err != nil {
		t.Fatal(err)
	}
	preview = encodeAgentImagePreview(encoded.Bytes(), 200, true)
	if !strings.HasPrefix(preview, "data:image/png;base64,") {
		t.Fatal("bounded transparent preview missing")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.SplitN(preview, ",", 2)[1])
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 200 {
		t.Fatalf("size limit exceeded: %d", len(raw))
	}
	if encodeAgentImagePreview(encoded.Bytes(), 1, true) != "" {
		t.Fatal("impossible size limit accepted")
	}
}

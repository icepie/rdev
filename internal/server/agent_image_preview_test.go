package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"strings"
	"testing"

	"rdev/internal/agent"
)

func TestImagePreviewIsBoundedAndPersistsWithoutOriginal(t *testing.T) {
	var buf bytes.Buffer
	png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1200, 800)))
	preview := agentImagePreview(buf.Bytes())
	if !strings.HasPrefix(preview, "data:image/jpeg;base64,") {
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

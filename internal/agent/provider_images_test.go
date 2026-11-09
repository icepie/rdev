package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func testToolPreview(t *testing.T) string {
	t.Helper()
	var data bytes.Buffer
	if err := jpeg.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil); err != nil {
		t.Fatal(err)
	}
	return "[image preview]\ndata:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data.Bytes())
}

func TestProviderToolImagesFollowCompleteResultGroup(t *testing.T) {
	preview := testToolPreview(t)
	history := []Message{
		{Role: "user", Content: "Compare the pictures"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "a", Name: "read_file"}, {ID: "b", Name: "shell"}, {ID: "c", Name: "read_file"}}},
		{Role: "tool", ToolCallID: "a", Content: preview},
		{Role: "tool", ToolCallID: "b", Content: "plain output"},
		{Role: "tool", ToolCallID: "c", Content: preview},
		{Role: "assistant", Content: "Comparison"},
	}
	before, _ := json.Marshal(history)
	payload, err := marshalChatRequest(Config{Model: "fixture", SystemPrompt: "system"}, history)
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		Messages []struct {
			Role       string          `json:"role"`
			Content    json.RawMessage `json:"content"`
			ToolCallID string          `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(payload, &request); err != nil {
		t.Fatal(err)
	}
	var roles []string
	for _, message := range request.Messages {
		roles = append(roles, message.Role)
	}
	if !reflect.DeepEqual(roles, []string{"system", "user", "assistant", "tool", "tool", "tool", "user", "assistant"}) {
		t.Fatalf("invalid result ordering: %v", roles)
	}
	for index, id := range []string{"a", "b", "c"} {
		message := request.Messages[3+index]
		var text string
		if json.Unmarshal(message.Content, &text) != nil || message.ToolCallID != id || strings.Contains(text, "base64") {
			t.Fatalf("invalid text result: %+v", message)
		}
	}
	var parts []messageContentPart
	if err := json.Unmarshal(request.Messages[6].Content, &parts); err != nil {
		t.Fatal(err)
	}
	if len(parts) != 3 || parts[0].Type != "text" {
		t.Fatalf("bad attachment batch: %+v", parts)
	}
	for _, part := range parts[1:] {
		if part.Type != "image_url" || part.ImageURL == nil || part.ImageURL.URL != strings.TrimPrefix(preview, "[image preview]\n") {
			t.Fatalf("missing image: %+v", part)
		}
	}
	after, _ := json.Marshal(history)
	if !bytes.Equal(before, after) {
		t.Fatal("provider conversion mutated persisted history")
	}
	// A group at the end of history must flush too; later requests must not
	// accumulate synthetic user messages in session state.
	if got := providerMessages(history[:5]); len(got) != 6 || got[5].Role != "user" {
		t.Fatalf("missing final attachment: %+v", got)
	}
}

func TestProviderLeavesNonPreviewAndTruncatedContentAsText(t *testing.T) {
	preview := testToolPreview(t)
	encoded := strings.TrimPrefix(preview, "[image preview]\ndata:image/jpeg;base64,")
	raw, _ := base64.StdEncoding.DecodeString(encoded)
	for _, content := range []string{
		"ordinary text", preview + "\nextra", strings.Replace(preview, "base64,", "base64,!", 1),
		"[image preview]\ndata:image/jpeg;base64," + base64.StdEncoding.EncodeToString(raw[:len(raw)-2]),
		preview + strings.Repeat("a", toolOutputMax),
	} {
		history := []Message{{Role: "tool", ToolCallID: "a", Content: content}}
		if got := providerMessages(history); !reflect.DeepEqual(got, history) {
			t.Fatal("non-preview content was converted")
		}
	}
	// User text containing the same envelope must not be reinterpreted as a
	// tool attachment or changed into another user turn.
	history := []Message{{Role: "user", Content: preview}}
	if got := providerMessages(history); !reflect.DeepEqual(got, history) {
		t.Fatal("rewrote user content")
	}
}

func TestSanitizeNativeMCPImagesPreservesResultAndAttachesImages(t *testing.T) {
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	jpegPreview := testToolPreview(t)
	jpegEncoded := strings.TrimPrefix(jpegPreview, "[image preview]\ndata:image/jpeg;base64,")
	input, err := json.Marshal(map[string]any{"result": map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": "visible"},
			map[string]any{"type": "image", "mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(pngData.Bytes())},
			map[string]any{"type": "image", "mimeType": "image/jpeg", "data": jpegEncoded},
		},
		"structuredContent": map[string]any{"ok": true}, "isError": false,
	}})
	if err != nil {
		t.Fatal(err)
	}
	clean, images := sanitizeNativeToolResult(string(input))
	if len(images) != 2 || images[0].Mime != "image/png" || images[1].Mime != "image/jpeg" {
		t.Fatalf("images = %+v", images)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(clean), &got); err != nil {
		t.Fatal(err)
	}
	result := got["result"].(map[string]any)
	if result["isError"] != false || result["structuredContent"].(map[string]any)["ok"] != true {
		t.Fatalf("metadata lost: %s", clean)
	}
	parts := result["content"].([]any)
	if parts[0].(map[string]any)["text"] != "visible" || !strings.Contains(parts[1].(map[string]any)["data"].(string), "omitted") {
		t.Fatalf("content not sanitized: %s", clean)
	}
	provider := providerMessages([]Message{{Role: "tool", Content: clean, Images: images}})
	if len(provider) != 2 || len(provider[1].Images) != 2 || strings.Contains(provider[0].Content, "base64") {
		t.Fatalf("provider messages: %+v", provider)
	}
}

func TestSanitizeNativeMCPInvalidImageDoesNotRetainBase64(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("not an image"))
	input := `{"result":{"content":[{"type":"image","mimeType":"image/png","data":"` + encoded + `"}],"structuredContent":{"ok":true},"isError":true}}`
	clean, images := sanitizeNativeToolResult(input)
	if len(images) != 0 || strings.Contains(clean, encoded) || !strings.Contains(clean, nativeImagePlaceholder) || !strings.Contains(clean, `"isError":true`) || !json.Valid([]byte(clean)) {
		t.Fatalf("invalid image was not safely omitted: %s (%v)", clean, images)
	}
}

type previewResultTool struct {
	fakeTool
	preview string
}

func (tool *previewResultTool) Execute(context.Context, json.RawMessage) (string, error) {
	return tool.preview, nil
}

func TestSessionSendsReadPreviewAsImageOnNextRound(t *testing.T) {
	preview := testToolPreview(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []json.RawMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests.Add(1) == 1 {
			fmt.Fprint(w, sseChunk("", "", sseToolCall("read-image", "read_file", `{"path":"fixture.jpg"}`))+sseDone)
			return
		}
		var last struct {
			Role    string               `json:"role"`
			Content []messageContentPart `json:"content"`
		}
		if err := json.Unmarshal(request.Messages[len(request.Messages)-1], &last); err != nil {
			t.Error(err)
		}
		if last.Role != "user" || len(last.Content) != 2 || last.Content[1].Type != "image_url" {
			t.Errorf("provider did not receive image: %+v", last)
		}
		fmt.Fprint(w, sseChunk("Image received", "")+sseDone)
	}))
	defer server.Close()
	tool := &previewResultTool{fakeTool: fakeTool{name: "read_file"}, preview: preview}
	session := New(testConfig(server.URL, []Tool{tool}))
	var displayed string
	reply, err := session.Run(context.Background(), "Read fixture.jpg", func(event Event) {
		if event.Type == EventToolEnd {
			displayed = event.ToolResult
		}
	})
	if err != nil || reply != "Image received" || requests.Load() != 2 {
		t.Fatalf("run failed: %q %v (%d rounds)", reply, err, requests.Load())
	}
	if displayed != preview {
		t.Fatal("UI preview was changed")
	}
	for _, message := range session.History() {
		if message.Role == "user" && message.Content != "Read fixture.jpg" {
			t.Fatal("synthetic provider message leaked into history")
		}
	}
}

func TestLargeToolScreenshotSurvivesTruncationAndReachesVision(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 640, 480))
	for i := range img.Pix {
		img.Pix[i] = byte((i*37 + i/13) % 255)
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	preview := "[image preview]\ndata:image/jpeg;base64," + base64.StdEncoding.EncodeToString(encoded.Bytes())
	if len(preview) <= toolOutputMax {
		t.Fatal("fixture too small")
	}
	if got := truncateToolOutput(preview); got != preview {
		t.Fatal("screenshot truncated")
	}
	messages := providerMessages([]Message{{Role: "tool", Content: preview}})
	if len(messages) != 2 || len(messages[1].Images) != 1 || strings.Contains(messages[0].Content, "base64") {
		t.Fatal("image did not reach vision message")
	}
	if _, ok := toolPreviewImage(preview + strings.Repeat("a", toolImageMax)); ok {
		t.Fatal("oversized image accepted")
	}
}

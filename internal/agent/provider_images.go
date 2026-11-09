package agent

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/jpeg"
	"image/png"
	"strings"
)

// providerMessages follows OMP's chat-completions conversion: image attachments
// belong in a user message AFTER the whole consecutive tool-result group, since
// tool-role content only supports text. Keep the persisted/UI preview envelope
// unchanged; this conversion is only for the provider request.
func providerMessages(history []Message) []Message {
	messages := make([]Message, 0, len(history))
	var images []MessageImage
	flush := func() {
		if len(images) > 0 {
			messages = append(messages, Message{Role: "user", Content: "Attached image(s) from tool results, in result order:", Images: images})
			images = nil
		}
	}
	for _, message := range history {
		if message.Role == "tool" {
			if preview, ok := toolPreviewImage(message.Content); ok {
				message.Content = "Image returned by tool (see attached image)."
				images = append(images, preview)
			} else if len(message.Images) > 0 {
				message.Content = strings.ReplaceAll(message.Content, nativeImagePlaceholder, "Image returned by tool (see attached image).")
				images = append(images, message.Images...)
			}
		} else {
			flush()
		}
		messages = append(messages, message)
	}
	flush()
	return messages
}

const (
	nativeImagePlaceholder = "[image data omitted; attached separately]"
	nativeImageCountMax    = 4
	nativeImageBytesMax    = 4 * 1024 * 1024
)

// sanitizeNativeToolResult preserves the native result JSON while removing image
// payloads from text. Only bounded, signature-checked MCP image content is attached.
func sanitizeNativeToolResult(content string) (string, []MessageImage) {
	var root map[string]json.RawMessage
	if json.Unmarshal([]byte(content), &root) != nil {
		return content, nil
	}
	result := root["result"]
	if len(result) == 0 {
		result = json.RawMessage(content)
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(result, &envelope) != nil {
		return content, nil
	}
	rawContent := envelope["content"]
	var parts []map[string]json.RawMessage
	if json.Unmarshal(rawContent, &parts) != nil {
		return content, nil
	}
	images := make([]MessageImage, 0, nativeImageCountMax)
	used := 0
	changed := false
	for _, part := range parts {
		var kind, mime, encoded string
		_ = json.Unmarshal(part["type"], &kind)
		_ = json.Unmarshal(part["mimeType"], &mime)
		_ = json.Unmarshal(part["data"], &encoded)
		if kind != "image" || encoded == "" {
			continue
		}
		var data []byte
		var err error
		if len(encoded) <= (nativeImageBytesMax*4/3)+8 && (mime == "image/png" || mime == "image/jpeg") && len(images) < nativeImageCountMax {
			data, err = base64.StdEncoding.Strict().DecodeString(encoded)
		}
		if err == nil && len(data) > 0 && used+len(data) <= nativeImageBytesMax && validNativeImage(data, mime) {
			used += len(data)
			images = append(images, MessageImage{Mime: mime, Data: encoded})
		}
		part["data"], _ = json.Marshal(nativeImagePlaceholder)
		changed = true
	}
	if !changed {
		return content, nil
	}
	partsJSON, err := json.Marshal(parts)
	if err != nil {
		return content, nil
	}
	envelope["content"] = partsJSON
	updated, err := json.Marshal(envelope)
	if err != nil {
		return content, nil
	}
	if len(root["result"]) > 0 {
		root["result"] = updated
	} else {
		root = envelope
	}
	output, err := json.Marshal(root)
	if err != nil {
		return content, nil
	}
	return string(output), images
}

func isNativeComputerCall(toolName string, arguments json.RawMessage) bool {
	if toolName != "computer" {
		return false
	}
	var input struct {
		Action string `json:"action"`
	}
	if json.Unmarshal(arguments, &input) != nil {
		return false
	}
	return input.Action == "native" || input.Action == "native_tools"
}

func validNativeImage(data []byte, mime string) bool {
	var cfg image.Config
	var err error
	switch mime {
	case "image/png":
		if !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
			return false
		}
		cfg, err = png.DecodeConfig(bytes.NewReader(data))
	case "image/jpeg":
		if !bytes.HasPrefix(data, []byte{0xff, 0xd8}) || !bytes.HasSuffix(data, []byte{0xff, 0xd9}) {
			return false
		}
		cfg, err = jpeg.DecodeConfig(bytes.NewReader(data))
	}
	return err == nil && cfg.Width > 0 && cfg.Height > 0 && int64(cfg.Width)*int64(cfg.Height) <= 16_000_000
}

// Recognize complete bounded tool image envelopes (file previews and captures). Older
// compacted/truncated previews remain text instead of sending broken images.
func toolPreviewImage(content string) (MessageImage, bool) {
	const prefix = "[image preview]\ndata:image/jpeg;base64,"
	if !strings.HasPrefix(content, prefix) || len(content) > toolImageMax {
		return MessageImage{}, false
	}
	encoded := strings.TrimPrefix(content, prefix)
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || !validNativeImage(data, "image/jpeg") {
		return MessageImage{}, false
	}
	return MessageImage{Mime: "image/jpeg", Data: encoded}, true
}

const toolImageMax = 1024 * 1024

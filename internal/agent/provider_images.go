package agent

import (
	"bytes"
	"encoding/base64"
	"image/jpeg"
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
		if message.Role != "tool" {
			flush()
		} else if preview, ok := toolPreviewImage(message.Content); ok {
			message.Content = "Image returned by tool (see attached image)."
			images = append(images, preview)
		}
		messages = append(messages, message)
	}
	flush()
	return messages
}

// Recognize complete bounded tool image envelopes (file previews and captures). Older
// compacted/truncated previews remain text instead of sending broken images.
const toolImageMax = 1024 * 1024

func toolPreviewImage(content string) (MessageImage, bool) {
	const prefix = "[image preview]\ndata:image/jpeg;base64,"
	if !strings.HasPrefix(content, prefix) || len(content) > toolImageMax {
		return MessageImage{}, false
	}
	encoded := strings.TrimPrefix(content, prefix)
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || !bytes.HasSuffix(data, []byte{0xff, 0xd9}) {
		return MessageImage{}, false
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 16_000_000 {
		return MessageImage{}, false
	}
	return MessageImage{Mime: "image/jpeg", Data: encoded}, true
}

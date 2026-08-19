package client

import (
	"encoding/base64"
	"fmt"
	"strings"

	"golang.design/x/clipboard"
	"rdev/internal/protocol"
)

const maxDesktopClipboardBytes = 16 * 1024 * 1024

var desktopClipboardFormats = []string{"text/plain", "text/html", "image/png", "text/uri-list"}

type desktopClipboard interface {
	Get() ([]protocol.ClipboardItem, error)
	Set([]protocol.ClipboardItem) error
}

type systemDesktopClipboard struct{}

func newDesktopClipboard() (desktopClipboard, error) {
	if err := clipboard.Init(); err != nil {
		return nil, err
	}
	return systemDesktopClipboard{}, nil
}

func (systemDesktopClipboard) Get() ([]protocol.ClipboardItem, error) {
	items := make([]protocol.ClipboardItem, 0, len(desktopClipboardFormats))
	for _, format := range clipboard.Formats() {
		mime := canonicalDesktopClipboardMIME(format.MIME())
		if !supportedDesktopClipboardMIME(mime) {
			continue
		}
		data := clipboard.Read(format)
		if data == nil {
			continue
		}
		items = append(items, protocol.ClipboardItem{MIME: mime, Data: base64.StdEncoding.EncodeToString(data)})
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("clipboard has no supported content")
	}
	if err := validateDesktopClipboardItems(items); err != nil {
		return nil, err
	}
	return items, nil
}

func (systemDesktopClipboard) Set(items []protocol.ClipboardItem) error {
	if err := validateDesktopClipboardItems(items); err != nil {
		return err
	}
	for _, mime := range []string{"text/html", "image/png", "text/uri-list", "text/plain"} {
		for _, item := range items {
			if canonicalDesktopClipboardMIME(item.MIME) != mime {
				continue
			}
			data, _ := base64.StdEncoding.DecodeString(item.Data)
			format := clipboard.FmtText
			switch mime {
			case "image/png":
				format = clipboard.FmtImage
			case "text/html", "text/uri-list":
				format = clipboard.Register(mime)
			}
			if clipboard.Write(format, data) == nil {
				return fmt.Errorf("clipboard write failed for %s", mime)
			}
			return nil
		}
	}
	return fmt.Errorf("clipboard payload has no supported format")
}

func validateDesktopClipboardItems(items []protocol.ClipboardItem) error {
	if len(items) == 0 {
		return fmt.Errorf("clipboard payload is empty")
	}
	total := 0
	for _, item := range items {
		mime := canonicalDesktopClipboardMIME(item.MIME)
		if !supportedDesktopClipboardMIME(mime) {
			return fmt.Errorf("unsupported clipboard format: %s", item.MIME)
		}
		data, err := base64.StdEncoding.DecodeString(item.Data)
		if err != nil {
			return fmt.Errorf("invalid base64 clipboard payload: %w", err)
		}
		total += len(data)
		if total > maxDesktopClipboardBytes {
			return fmt.Errorf("clipboard payload exceeds %d bytes", maxDesktopClipboardBytes)
		}
	}
	return nil
}

func canonicalDesktopClipboardMIME(mime string) string {
	if strings.HasPrefix(strings.ToLower(mime), "text/plain") {
		return "text/plain"
	}
	return strings.ToLower(strings.TrimSpace(mime))
}

func supportedDesktopClipboardMIME(mime string) bool {
	for _, supported := range desktopClipboardFormats {
		if mime == supported {
			return true
		}
	}
	return false
}

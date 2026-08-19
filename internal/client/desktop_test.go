package client

import (
	"encoding/base64"
	"image"
	"runtime"
	"strings"
	"testing"

	"rdev/internal/protocol"
)

func TestDesktopCursorPositionFallsBackWhenProviderOutOfBounds(t *testing.T) {
	session := &desktopSession{}
	session.setCursor(5, 6)
	capturer := fakeCursorCapturer{bounds: image.Rect(0, 0, 10, 10), cursor: image.Pt(20, 20), cursorOK: true}
	point, ok := desktopCursorPosition(session, capturer)
	if !ok {
		t.Fatal("desktopCursorPosition returned no point")
	}
	if point != image.Pt(5, 6) {
		t.Fatalf("cursor = %v, want fallback cursor", point)
	}
}

type fakeCursorCapturer struct {
	bounds   image.Rectangle
	cursor   image.Point
	cursorOK bool
}

func (f fakeCursorCapturer) Bounds() image.Rectangle       { return f.bounds }
func (f fakeCursorCapturer) Capture() (image.Image, error) { return image.NewRGBA(f.bounds), nil }
func (f fakeCursorCapturer) Close() error                  { return nil }
func (f fakeCursorCapturer) CursorPosition() (image.Point, bool) {
	return f.cursor, f.cursorOK
}

func TestDesktopCapabilitiesReportsCurrentPlatform(t *testing.T) {
	caps := desktopCapabilities()
	if caps == nil {
		t.Fatal("desktopCapabilities() returned nil")
	}
	if caps.Platform != runtime.GOOS {
		t.Fatalf("Platform = %q, want %q", caps.Platform, runtime.GOOS)
	}
	if caps.DisplayServer == "x11" {
		if !caps.Supported || caps.ViewOnly || !caps.Input {
			t.Fatalf("X11 capability = %#v, want supported interactive desktop", caps)
		}
		return
	}
	if caps.DisplayServer == "windows" {
		if !caps.Supported || caps.ViewOnly || !caps.Input {
			t.Fatalf("Windows capability = %#v, want supported interactive desktop", caps)
		}
		return
	}
	if caps.DisplayServer == "drm-kms" || caps.DisplayServer == "fbdev" {
		if !caps.Supported {
			t.Fatalf("Linux fallback capability = %#v, want supported fallback", caps)
		}
		if caps.Input && caps.ViewOnly {
			t.Fatalf("Linux fallback with input should not be view-only: %#v", caps)
		}
		if !caps.Input && !caps.ViewOnly {
			t.Fatalf("Linux fallback without input should be view-only: %#v", caps)
		}
		return
	}
	if caps.Supported {
		t.Fatalf("unsupported desktop capability should be unavailable, got %#v", caps)
	}
	if caps.Reason == "" {
		t.Fatal("expected an unavailable reason")
	}
}

func TestValidateDesktopClipboardItemsAcceptsSupportedFormats(t *testing.T) {
	items := []protocol.ClipboardItem{
		{MIME: "text/plain;charset=utf-8", Data: base64.StdEncoding.EncodeToString([]byte("hello 世界"))},
		{MIME: "image/png", Data: base64.StdEncoding.EncodeToString([]byte("png"))},
	}
	if err := validateDesktopClipboardItems(items); err != nil {
		t.Fatalf("validateDesktopClipboardItems() error = %v", err)
	}
}

func TestValidateDesktopClipboardItemsRejectsInvalidPayloads(t *testing.T) {
	tests := []struct {
		name  string
		items []protocol.ClipboardItem
		want  string
	}{
		{name: "empty", want: "empty"},
		{name: "unsupported", items: []protocol.ClipboardItem{{MIME: "application/octet-stream", Data: "eA=="}}, want: "unsupported"},
		{name: "invalid base64", items: []protocol.ClipboardItem{{MIME: "text/plain", Data: "!"}}, want: "base64"},
		{name: "oversized", items: []protocol.ClipboardItem{{MIME: "image/png", Data: base64.StdEncoding.EncodeToString(make([]byte, maxDesktopClipboardBytes+1))}}, want: "exceeds"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateDesktopClipboardItems(test.items)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

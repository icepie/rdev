package client

import (
	"image"
	"runtime"
	"testing"
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

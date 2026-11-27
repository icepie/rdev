package client

import (
	"errors"
	"image"
	"testing"
)

type fakeDesktopCapturer struct {
	bounds image.Rectangle
	closed bool
}

func (f *fakeDesktopCapturer) Bounds() image.Rectangle { return f.bounds }
func (f *fakeDesktopCapturer) Capture() (image.Image, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeDesktopCapturer) Close() error {
	f.closed = true
	return nil
}

// A backend that only reports itself unavailable must keep the session's
// capturer open when no other backend covers the same area: closing it here
// would end the stream with no replacement.
func TestSwitchDesktopCapturerWithoutFallbackKeepsTheCapturer(t *testing.T) {
	capturer := &fakeDesktopCapturer{bounds: image.Rect(0, 0, 64, 48)}
	next, ok := switchDesktopCapturer(capturer, "test:source", errDesktopCapturerUnavailable)
	if ok || next != nil {
		t.Fatalf("fallback = %v, want none for a fake capturer", ok)
	}
	if capturer.closed {
		t.Fatal("capturer was closed without a replacement")
	}
}

// The sentinel must survive the wrapping the capture loops add, otherwise the
// switch is never attempted.
func TestDesktopCapturerUnavailableSurvivesWrapping(t *testing.T) {
	wrapped := errors.Join(errors.New("desktop capture failed"), errDesktopCapturerUnavailable)
	if !errors.Is(wrapped, errDesktopCapturerUnavailable) {
		t.Fatal("wrapped error lost the unavailable sentinel")
	}
}

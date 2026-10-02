package client

import (
	"bytes"
	"encoding/base64"
	"image"
	"runtime"
	"strings"
	"testing"
	"time"

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

func TestChooseDesktopInputBackendUsesAdvertisedPreferenceOrder(t *testing.T) {
	available := []string{"win32-touch", "win32"}
	if got := chooseDesktopInputBackend("auto", available); got != "win32-touch" {
		t.Fatalf("auto backend = %q, want first advertised backend", got)
	}
	if got := chooseDesktopInputBackend("win32", available); got != "win32" {
		t.Fatalf("explicit backend = %q, want win32", got)
	}
	if got := chooseDesktopInputBackend("missing", available); got != "win32-touch" {
		t.Fatalf("unavailable backend fallback = %q, want first advertised backend", got)
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

type testDesktopEncoder struct {
	format  string
	encoded int
}

func (e *testDesktopEncoder) Format() string              { return e.format }
func (e *testDesktopEncoder) CodecInfo() desktopCodecInfo { return desktopCodecInfo{} }
func (e *testDesktopEncoder) Close()                      {}
func (e *testDesktopEncoder) Encode(_ *image.RGBA, out *bytes.Buffer) error {
	e.encoded++
	out.WriteByte(byte(e.encoded))
	return nil
}

func TestDesktopFrameSenderMJPEGSkipsUnchangedFramesAndResendsCachedPayload(t *testing.T) {
	encoder := &testDesktopEncoder{format: desktopFormatMJPEG}
	sender := newDesktopFrameSender(encoder)
	frame := image.NewRGBA(image.Rect(0, 0, 2, 2))
	now := time.Unix(100, 0)

	payload, send, err := sender.tick(frame, now)
	if err != nil || !send || !bytes.Equal(payload, []byte{1}) {
		t.Fatalf("first tick = (%v, %t, %v), want encoded frame", payload, send, err)
	}
	if payload, send, err = sender.tick(frame, now.Add(time.Second)); err != nil || send || payload != nil {
		t.Fatalf("unchanged tick = (%v, %t, %v), want skipped", payload, send, err)
	}
	payload, send, err = sender.tick(frame, now.Add(desktopIdleResendInterval))
	if err != nil || !send || !bytes.Equal(payload, []byte{1}) || encoder.encoded != 1 {
		t.Fatalf("idle resend = (%v, %t, %v), encodes=%d; want cached frame", payload, send, err, encoder.encoded)
	}
}

func TestDesktopFrameSenderH264EncodesEveryFrame(t *testing.T) {
	encoder := &testDesktopEncoder{format: desktopFormatH264}
	sender := newDesktopFrameSender(encoder)
	frame := image.NewRGBA(image.Rect(0, 0, 2, 2))
	now := time.Unix(100, 0)
	for i := 1; i <= 2; i++ {
		payload, send, err := sender.tick(frame, now.Add(time.Duration(i)*time.Second))
		if err != nil || !send || !bytes.Equal(payload, []byte{byte(i)}) {
			t.Fatalf("tick %d = (%v, %t, %v), want newly encoded frame", i, payload, send, err)
		}
	}
	if encoder.encoded != 2 {
		t.Fatalf("H264 encoded %d frames, want 2", encoder.encoded)
	}
}

func TestNormalizeH264FrameSizeRoundsDownWithoutExceedingLimit(t *testing.T) {
	for _, test := range []struct {
		width, height int
		wantW, wantH  int
	}{
		{1600, 1000, 1600, 1000},
		{1601, 1001, 1600, 1000},
		{1, 1, 1, 1},
	} {
		gotW, gotH := normalizeH264FrameSize(test.width, test.height)
		if gotW != test.wantW || gotH != test.wantH {
			t.Fatalf("normalizeH264FrameSize(%d, %d) = %d, %d; want %d, %d", test.width, test.height, gotW, gotH, test.wantW, test.wantH)
		}
	}
}

func TestAnnexBToAVCCHandlesMixedStartCodesAndKeyframes(t *testing.T) {
	annexB := []byte{
		0, 0, 0, 1, 0x67, 0x42, 0xC0, 0x1F, 0,
		0, 0, 1, 0x68, 0xCE, 0x06, 0xE2,
		0, 0, 0, 1, 0x65, 0x88, 0x84, 0,
	}
	keyframe, avcc, err := annexBToAVCC(annexB)
	if err != nil {
		t.Fatalf("annexBToAVCC() error = %v", err)
	}
	if !keyframe {
		t.Fatal("annexBToAVCC() did not identify the IDR NAL as a keyframe")
	}
	want := []byte{
		0, 0, 0, 4, 0x67, 0x42, 0xC0, 0x1F,
		0, 0, 0, 4, 0x68, 0xCE, 0x06, 0xE2,
		0, 0, 0, 3, 0x65, 0x88, 0x84,
	}
	if !bytes.Equal(avcc, want) {
		t.Fatalf("AVCC = %x, want %x", avcc, want)
	}
}

func TestAnnexBToAVCCRejectsDataWithoutStartCode(t *testing.T) {
	if _, _, err := annexBToAVCC([]byte{0x65, 0x88, 0x84}); err == nil {
		t.Fatal("annexBToAVCC() accepted data without an Annex B start code")
	}
}

func TestResizeDesktopFrameIntoReusesMatchingDestination(t *testing.T) {
	source := image.NewRGBA(image.Rect(0, 0, 4, 4))
	destination := image.NewRGBA(image.Rect(0, 0, 2, 2))
	first, err := resizeDesktopFrameInto(destination, source, 2, 2)
	if err != nil || first != destination {
		t.Fatalf("first resize = (%p, %v), want provided buffer %p", first, err, destination)
	}
	second, err := resizeDesktopFrameInto(first, source, 2, 2)
	if err != nil || second != destination {
		t.Fatalf("second resize = (%p, %v), want reused buffer %p", second, err, destination)
	}
}

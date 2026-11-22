package client

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"rdev/internal/protocol"
)

func TestDesktopPointerMapsScaledMonitorEdges(t *testing.T) {
	s := &desktopSession{}
	s.setFrame(image.Rect(-3840, -200, 0, 1960), 800, 450)
	for from, want := range map[image.Point]image.Point{
		image.Pt(0, 0):     image.Pt(-3840, -200),
		image.Pt(799, 449): image.Pt(-1, 1959),
		image.Pt(-9, 500):  image.Pt(-3840, 1959),
	} {
		x, y := s.mapPoint(from.X, from.Y)
		if got := image.Pt(x, y); got != want {
			t.Errorf("mapPoint(%v) = %v, want %v", from, got, want)
		}
	}
}

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

// TestOpenDesktopInputFallsBackAcrossBackends: a preferred backend that
// fails to initialize (e.g. Windows touch injection on a non-interactive
// session) must not kill control — the next advertised backend takes over.
func TestOpenDesktopInputFallsBackAcrossBackends(t *testing.T) {
	available := []string{"win32-touch", "win32"}
	factory := func(backend string) (desktopInput, error) {
		if backend == "win32-touch" {
			return nil, fmt.Errorf("InitializeTouchInjection failed")
		}
		return fakeDesktopInput{backend: backend}, nil
	}

	input, active := openDesktopInput("win32-touch", available, factory)
	if input == nil || active != "win32" {
		t.Fatalf("fallback = (%v, %q), want win32 input", input, active)
	}

	// All backends failing yields no input instead of a half-dead one.
	input, active = openDesktopInput("win32-touch", available, func(string) (desktopInput, error) {
		return nil, fmt.Errorf("boom")
	})
	if input != nil || active != "" {
		t.Fatalf("all-failed = (%v, %q), want nil input", input, active)
	}

	// Preferred backend succeeding is used directly, no fallback noise.
	calls := 0
	input, active = openDesktopInput("win32", available, func(backend string) (desktopInput, error) {
		calls++
		return fakeDesktopInput{backend: backend}, nil
	})
	if input == nil || active != "win32" || calls != 1 {
		t.Fatalf("direct = (%v, %q, %d calls), want win32 once", input, active, calls)
	}
}

type fakeDesktopInput struct{ backend string }

func (f fakeDesktopInput) Backend() string               { return f.backend }
func (f fakeDesktopInput) Apply(desktopInputEvent) error { return nil }
func (f fakeDesktopInput) Close() error                  { return nil }

type recordingDesktopInput struct {
	events []desktopInputEvent
	closed bool
}

func (r *recordingDesktopInput) Backend() string { return "recording" }
func (r *recordingDesktopInput) Apply(event desktopInputEvent) error {
	r.events = append(r.events, event)
	return nil
}
func (r *recordingDesktopInput) Close() error {
	r.closed = true
	return nil
}

func TestStatefulDesktopInputReleasesHeldKeysAndPointersOnClose(t *testing.T) {
	recorder := &recordingDesktopInput{}
	input := newStatefulDesktopInput(recorder)
	if err := input.Apply(desktopInputEvent{Type: "key_down", Code: "ControlLeft", Key: "Control", CtrlKey: true}); err != nil {
		t.Fatal(err)
	}
	if err := input.Apply(desktopInputEvent{Type: "mouse_down", X: 10, Y: 20, Button: 2, PointerType: "mouse", PointerID: 7, Pressure: 1}); err != nil {
		t.Fatal(err)
	}
	if err := input.Apply(desktopInputEvent{Type: "mouse_move", X: 30, Y: 40, PointerType: "mouse", PointerID: 7, Pressure: 1}); err != nil {
		t.Fatal(err)
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	if !recorder.closed {
		t.Fatal("underlying desktop input was not closed")
	}
	var releasedKey, releasedPointer bool
	for _, event := range recorder.events[3:] {
		switch event.Type {
		case "key_up":
			releasedKey = event.Code == "ControlLeft" && !event.CtrlKey
		case "mouse_up":
			releasedPointer = event.Button == 2 && event.X == 30 && event.Y == 40 && event.Pressure == 0
		}
	}
	if !releasedKey || !releasedPointer {
		t.Fatalf("release events = %#v, want key and pointer releases", recorder.events[3:])
	}
}

func TestStatefulDesktopInputTracksPhysicalKeyAcrossModifierChanges(t *testing.T) {
	recorder := &recordingDesktopInput{}
	input := newStatefulDesktopInput(recorder)
	for _, event := range []desktopInputEvent{
		{Type: "key_down", Code: "KeyA", Key: "A", ShiftKey: true},
		{Type: "key_down", Code: "KeyA", Key: "a"},
		{Type: "key_up", Code: "KeyA", Key: "a"},
		{Type: "key_down", Key: "b"},
	} {
		if err := input.Apply(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	if got := recorder.events[4:]; len(got) != 1 || got[0] != (desktopInputEvent{Type: "key_up", Key: "b"}) {
		t.Fatalf("cleanup events = %+v, want only b up", got)
	}
}

func TestDesktopSessionCloseDuringInputSetup(t *testing.T) {
	// Whether close wins before or after setup, the backend is closed once.
	for range 50 {
		session := &desktopSession{stop: make(chan struct{})}
		recorder := &recordingDesktopInput{}
		done := make(chan struct{})
		go func() {
			session.installInput(recorder, nil)
			close(done)
		}()
		session.close()
		<-done
		session.close()
		if !recorder.closed || session.input != nil {
			t.Fatalf("closed=%v session.input=%v", recorder.closed, session.input)
		}
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
	format    string
	codecInfo desktopCodecInfo
	encoded   int
}

func (e *testDesktopEncoder) Format() string              { return e.format }
func (e *testDesktopEncoder) Backend() string             { return "test" }
func (e *testDesktopEncoder) CodecInfo() desktopCodecInfo { return e.codecInfo }
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

	first, send, err := sender.tick(frame, now)
	if err != nil || !send || !bytes.Equal(first, []byte{1}) {
		t.Fatalf("first tick = (%v, %t, %v), want first access unit", first, send, err)
	}
	second, send, err := sender.tick(frame, now.Add(time.Second))
	if err != nil || !send || !bytes.Equal(second, []byte{2}) || encoder.encoded != 2 {
		t.Fatalf("second tick = (%v, %t, %v), encodes=%d; want new access unit", second, send, err, encoder.encoded)
	}
}

func TestDesktopEncoderReadyWaitsForH264Headers(t *testing.T) {
	encoder := &testDesktopEncoder{format: desktopFormatH264}
	if desktopEncoderReady(encoder) {
		t.Fatal("H264 encoder reported ready without codec headers")
	}
	encoder.codecInfo.Codec = "avc1.42C01E"
	if !desktopEncoderReady(encoder) {
		t.Fatal("H264 encoder did not report ready after codec headers arrived")
	}
	if !desktopEncoderReady(&testDesktopEncoder{format: desktopFormatMJPEG}) {
		t.Fatal("MJPEG encoder should not wait for codec headers")
	}
}

func TestNormalizeDesktopFrameSizeMakesH264DimensionsEven(t *testing.T) {
	if got := normalizeDesktopFrameSize(desktopFormatH264, image.Pt(1919, 1079)); got != image.Pt(1918, 1078) {
		t.Fatalf("normalized H264 size = %v, want 1918x1078", got)
	}
	if got := normalizeDesktopFrameSize(desktopFormatH264, image.Pt(1, 1)); got != image.Pt(2, 2) {
		t.Fatalf("minimum H264 size = %v, want 2x2", got)
	}
	if got := normalizeDesktopFrameSize(desktopFormatMJPEG, image.Pt(1919, 1079)); got != image.Pt(1919, 1079) {
		t.Fatalf("MJPEG size = %v, want unchanged", got)
	}
}

func TestResizeDesktopFrameToSizeReusesMatchingDestination(t *testing.T) {
	source := image.NewRGBA(image.Rect(0, 0, 4, 4))
	destination := image.NewRGBA(image.Rect(0, 0, 2, 2))
	first, err := resizeDesktopFrameToSize(destination, source, 2, 2)
	if err != nil || first != destination {
		t.Fatalf("first resize = (%p, %v), want provided buffer %p", first, err, destination)
	}
	second, err := resizeDesktopFrameToSize(first, source, 2, 2)
	if err != nil || second != destination {
		t.Fatalf("second resize = (%p, %v), want reused buffer %p", second, err, destination)
	}
}

func TestReusableDesktopRGBAReusesMatchingGeometry(t *testing.T) {
	first := reusableDesktopRGBA(nil, 4, 3)
	if second := reusableDesktopRGBA(first, 4, 3); second != first {
		t.Fatalf("matching frame = %p, want reused %p", second, first)
	}
	if resized := reusableDesktopRGBA(first, 5, 3); resized == first || resized.Bounds() != image.Rect(0, 0, 5, 3) {
		t.Fatalf("resized frame = %p %v, want new 5x3 frame", resized, resized.Bounds())
	}
}

func TestCopyDesktopRGBADoesNotAliasCaptureFrame(t *testing.T) {
	source := image.NewRGBA(image.Rect(0, 0, 2, 1))
	source.Pix[0] = 10
	copied := copyDesktopRGBA(nil, source)
	copied.Pix[0] = 99
	if source.Pix[0] != 10 {
		t.Fatalf("capture frame changed to %d through cursor scratch copy", source.Pix[0])
	}
}

func TestDesktopVideoCodecsAdvertiseCPUEncoder(t *testing.T) {
	codecs, backends := desktopVideoCodecs()
	if !slices.Contains(codecs, desktopFormatH264) || !slices.Contains(codecs, desktopFormatMJPEG) {
		t.Fatalf("desktop codecs = %v, want h264 and mjpeg", codecs)
	}
	if !slices.Contains(backends, "cpu") {
		t.Fatalf("desktop encoder backends = %v, want cpu fallback", backends)
	}
}

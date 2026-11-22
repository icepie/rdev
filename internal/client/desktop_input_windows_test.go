//go:build windows

package client

import (
	"errors"
	"slices"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
	"rdev/internal/protocol"
)

func TestWindowsInputABI(t *testing.T) {
	var in windowsInput
	var key windowsKeyboardInput
	ptr := unsafe.Sizeof(uintptr(0))
	// INPUT is 28 bytes on 386, 40 on amd64/arm64. Its union starts at 4 or 8.
	if unsafe.Sizeof(in) != 16+3*ptr || unsafe.Offsetof(in.mouse) != ptr ||
		unsafe.Offsetof(in.mouse.flags) != 12 || unsafe.Offsetof(in.mouse.extra) != 16+ptr ||
		unsafe.Offsetof(key.scan) != 2 || unsafe.Offsetof(key.flags) != 4 || unsafe.Offsetof(key.extra) != 8+ptr {
		t.Fatalf("unexpected INPUT layout: ptr=%d size=%d union=%d mi.extra=%d ki.extra=%d", ptr, unsafe.Sizeof(in), unsafe.Offsetof(in.mouse), unsafe.Offsetof(in.mouse.extra), unsafe.Offsetof(key.extra))
	}
}

func TestWindowsPhysicalKeys(t *testing.T) {
	for _, tc := range []struct {
		code     string
		scan     uint16
		extended bool
	}{
		{"KeyQ", 0x10, false}, {"IntlBackslash", 0x56, false},
		{"ShiftLeft", 0x2a, false}, {"ShiftRight", 0x36, false},
		{"ControlLeft", 0x1d, false}, {"ControlRight", 0x1d, true},
		{"AltLeft", 0x38, false}, {"AltRight", 0x38, true},
		{"MetaLeft", 0x5b, true}, {"MetaRight", 0x5c, true},
		{"Enter", 0x1c, false}, {"NumpadEnter", 0x1c, true},
		{"Numpad4", 0x4b, false}, {"ArrowLeft", 0x4b, true},
	} {
		flags := uint32(keyEventScanCode)
		if tc.extended {
			flags |= keyEventExtendedKey
		}
		for _, typ := range []string{"key_down", "key_up"} {
			if typ == "key_up" {
				flags |= keyEventKeyUp
			}
			got, ok := windowsKeyInput(desktopInputEvent{Type: typ, Code: tc.code, Key: "A", ShiftKey: true})
			if !ok || got != (windowsKeyboardInput{scan: tc.scan, flags: flags}) {
				t.Errorf("%s %s: %+v, want scan %#x flags %#x", typ, tc.code, got, tc.scan, flags)
			}
		}
	}
	if key, ok := windowsKeyInput(desktopInputEvent{Type: "key_up", Key: "a"}); !ok || key != (windowsKeyboardInput{vk: 'A', flags: keyEventKeyUp}) {
		t.Fatalf("logical fallback: %+v, ok=%t", key, ok)
	}
	if _, ok := windowsKeyInput(desktopInputEvent{Type: "key_down", Code: "Key?"}); ok {
		t.Fatal("unrecognized key generated input")
	}
}

func TestWindowsMouseInputs(t *testing.T) {
	desktop := protocol.DesktopSource{X: -1920, Y: -200, Width: 3840, Height: 1280}
	var inputs [3]windowsInput
	event := desktopInputEvent{Type: "wheel", X: -1000, Y: 1030, DeltaX: 3, DeltaY: 100}
	n := windowsMouseInputs(&inputs, event, desktop)
	move := inputs[0].mouse
	if n != 3 || move.flags != mouseEventMove|mouseEventVirtualDesk|mouseEventAbsolute ||
		int64(move.dx)*3840/65536 != 920 || int64(move.dy)*1280/65536 != 1230 {
		t.Fatalf("wheel must move to mapped pixel first: n=%d, move=%+v", n, move)
	}
	if inputs[1].mouse.flags != mouseEventWheel || int32(inputs[1].mouse.data) != -100 || inputs[2].mouse.flags != mouseEventHWheel || inputs[2].mouse.data != 3 {
		t.Fatalf("wheel axis or direction lost: %+v", inputs[1:])
	}
	for _, typ := range []string{"mouse_down", "mouse_up"} {
		event = desktopInputEvent{Type: typ, Button: 4, X: -2000, Y: 1200}
		n = windowsMouseInputs(&inputs, event, desktop)
		want := uint32(mouseEventXDown)
		if typ == "mouse_up" {
			want = mouseEventXUp
		}
		if n != 2 || inputs[0].mouse.dx != 0 || inputs[0].mouse.dy != 65535 || inputs[1].mouse.flags != want || inputs[1].mouse.data != 2 {
			t.Fatalf("clamped %s for XBUTTON2: %+v", typ, inputs[:n])
		}
	}
}

func TestWindowsTouchABI(t *testing.T) {
	ptr := unsafe.Sizeof(uintptr(0))
	infoSize := 80 + 2*ptr
	var info pointerInfo
	var touch pointerTouchInfo
	if unsafe.Sizeof(info) != infoSize || unsafe.Offsetof(info.PtPixelLocation) != 16+2*ptr ||
		unsafe.Offsetof(info.PerformanceCount) != 64+2*ptr || unsafe.Offsetof(touch.TouchFlags) != infoSize ||
		unsafe.Sizeof(touch) != infoSize+48 {
		t.Fatalf("unexpected Win32 layout: pointer=%d info=%d touch=%d", ptr, unsafe.Sizeof(info), unsafe.Sizeof(touch))
	}
}

func TestWindowsTouchFrames(t *testing.T) {
	var frames [][]pointerTouchInfo
	in := windowsTouchInjector{inject: func(frame []pointerTouchInfo) error {
		frames = append(frames, slices.Clone(frame))
		return nil
	}}
	a, b := &windowsTouchInput{injector: &in}, &windowsTouchInput{injector: &in}
	apply := func(owner *windowsTouchInput, typ string, x int) {
		t.Helper()
		if err := owner.Apply(desktopInputEvent{Type: typ, PointerID: 7, X: x, Y: 10}); err != nil {
			t.Fatal(err)
		}
	}
	apply(a, "mouse_move", 0) // hover
	apply(a, "mouse_down", 10)
	apply(a, "mouse_down", 10) // duplicate
	apply(b, "mouse_down", 20)
	apply(a, "mouse_up", 15) // coalesced motion must precede release
	apply(a, "mouse_up", 15)
	if err := b.close(); err != nil {
		t.Fatal(err)
	}
	const down = pointerFlagDown | pointerFlagInRange | pointerFlagInContact
	const move = pointerFlagUpdate | pointerFlagInRange | pointerFlagInContact
	want := [][]uint32{{down}, {move, down}, {move, move}, {pointerFlagUp, move}, {pointerFlagUp}}
	if len(frames) != len(want) {
		t.Fatalf("expected %d frames, got %d", len(want), len(frames))
	}
	for i, flags := range want {
		if len(frames[i]) != len(flags) {
			t.Fatalf("frame %d: %d contacts, want %d", i, len(frames[i]), len(flags))
		}
		for j, flag := range flags {
			if got := frames[i][j].PointerInfo.PointerFlags; got != flag {
				t.Errorf("frame %d, contact %d: flags %#x, want %#x", i, j, got, flag)
			}
		}
	}
	if frames[2][0].PointerInfo.PtPixelLocation.X != 15 || frames[3][0].PointerInfo.PtPixelLocation.X != 15 ||
		frames[1][0].PointerInfo.PointerID == frames[1][1].PointerInfo.PointerID ||
		frames[4][0].PointerInfo.PointerID != frames[1][1].PointerInfo.PointerID {
		t.Fatal("lost release point or contact identity across frames")
	}
}

func TestWindowsTouchFailureRecovery(t *testing.T) {
	var calls int
	var next error
	in := windowsTouchInjector{inject: func([]pointerTouchInfo) error {
		calls++
		err := next
		next = nil
		return err
	}}
	touch := &windowsTouchInput{injector: &in}
	apply := func(typ string) error { return touch.Apply(desktopInputEvent{Type: typ, PointerID: 1}) }
	next = windows.ERROR_NOT_READY
	if err := apply("mouse_down"); err != nil || calls != 2 {
		t.Fatalf("not-ready retry: calls=%d, err=%v", calls, err)
	}
	next = windows.ERROR_INVALID_PARAMETER
	if err := apply("mouse_move"); !errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		t.Fatal(err)
	}
	if err := apply("mouse_up"); err != nil || calls != 3 {
		t.Fatalf("cancelled contact was injected: calls=%d, err=%v", calls, err)
	}
	next = windows.ERROR_ACCESS_DENIED
	if err := apply("mouse_down"); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatal(err)
	}
	if err := apply("mouse_down"); err != nil || calls != 4 {
		t.Fatalf("denied session attempted another injection: calls=%d, err=%v", calls, err)
	}
}

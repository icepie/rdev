//go:build windows

package client

import (
	"errors"
	"slices"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsKeyFlagsMarksExtendedKeysAndReleases(t *testing.T) {
	if got := windowsKeyFlags(desktopInputEvent{Code: "ArrowLeft"}, false); got != keyEventExtendedKey {
		t.Fatalf("ArrowLeft flags = %#x, want extended", got)
	}
	if got := windowsKeyFlags(desktopInputEvent{Code: "ControlRight"}, true); got != keyEventExtendedKey|keyEventKeyUp {
		t.Fatalf("ControlRight release flags = %#x, want extended key-up", got)
	}
	if got := windowsKeyFlags(desktopInputEvent{Code: "KeyA"}, true); got != keyEventKeyUp {
		t.Fatalf("KeyA release flags = %#x, want key-up", got)
	}
}

func TestWindowsVKDistinguishesPhysicalModifiers(t *testing.T) {
	for code, want := range map[string]byte{
		"ShiftLeft": 0xa0, "ShiftRight": 0xa1, "ControlLeft": 0xa2, "ControlRight": 0xa3,
		"AltLeft": 0xa4, "AltRight": 0xa5, "MetaLeft": 0x5b, "MetaRight": 0x5c,
		"Key?": 0, "DigitX": 0,
	} {
		if got := windowsVK(desktopInputEvent{Code: code}); got != want {
			t.Errorf("%s = %#x, want %#x", code, got, want)
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

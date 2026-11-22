//go:build darwin

package client

import (
	"image"
	"testing"
	"time"
)

func TestDarwinKeycodeDistinguishesAFromUnsupportedCodes(t *testing.T) {
	if got := darwinKeycode(desktopInputEvent{Code: "KeyA"}); got != 0x00 {
		t.Fatalf("KeyA = %#x, want 0x00", got)
	}
	if got := darwinKeycode(desktopInputEvent{Code: "Key?"}); got != 0xff {
		t.Fatalf("unsupported key = %#x, want 0xff", got)
	}
}

func TestDarwinKeycodePreservesRightModifiersAndKeypad(t *testing.T) {
	tests := map[string]uint16{
		"MetaRight":      0x36,
		"ShiftRight":     0x3c,
		"AltRight":       0x3d,
		"ControlRight":   0x3e,
		"NumpadEnter":    0x4c,
		"NumpadDivide":   0x4b,
		"NumpadSubtract": 0x4e,
		"Numpad0":        0x52,
		"Numpad9":        0x5c,
	}
	for code, want := range tests {
		if got := darwinKeycode(desktopInputEvent{Code: code}); got != want {
			t.Errorf("%s = %#x, want %#x", code, got, want)
		}
	}
}

type quartzRecordedEvent struct {
	typ, button uint32
	point       cgPoint
	count       int64
	flags       uint64
	key         uint16
	down        bool
	wheel       [3]int32
}

// Record at the native call boundary. No frameworks, Accessibility grant or live desktop required.
func recordingQuartzInput(t *testing.T) (*darwinDesktopInput, *[]quartzRecordedEvent) {
	t.Helper()
	const ref = 1
	var pending quartzRecordedEvent
	var posted []quartzRecordedEvent
	api := &darwinInputAPI{
		mouse: func(_ uintptr, typ uint32, point cgPoint, button uint32) uintptr {
			pending = quartzRecordedEvent{typ: typ, point: point, button: button}
			return ref
		},
		keyboard: func(_ uintptr, key uint16, down bool) uintptr {
			pending = quartzRecordedEvent{key: key, down: down}
			return ref
		},
		wheel: func(_ uintptr, unit, axes uint32, y, x, z int32) uintptr {
			if unit != 0 || axes != 2 {
				t.Fatalf("wheel units/axes = %d/%d, want pixels on both axes", unit, axes)
			}
			pending = quartzRecordedEvent{wheel: [3]int32{y, x, z}}
			return ref
		},
		setLocation: func(_ uintptr, p cgPoint) { pending.point = p },
		getFlags:    func(uintptr) uint64 { return 0x10000 | cgEventFlagMaskControl | rightControl },
		setFlags:    func(_ uintptr, flags uint64) { pending.flags = flags },
		setInteger: func(_ uintptr, field uint32, value int64) {
			if field != cgMouseEventClickState {
				t.Fatalf("unexpected field %d", field)
			}
			pending.count = value
		},
		post: func(tap uint32, ev uintptr) {
			if tap != cgHIDEventTap || ev != ref {
				t.Fatalf("invalid post tap/event: %d/%d", tap, ev)
			}
			posted = append(posted, pending)
		},
		release: func(ev uintptr) {
			if ev != ref {
				t.Fatalf("invalid released event %d", ev)
			}
		},
	}
	return &darwinDesktopInput{api: api, clickInterval: time.Hour}, &posted
}

func TestDarwinDragsWithHeldButton(t *testing.T) {
	for _, tc := range []struct {
		button       int
		native, drag uint32
	}{{0, 0, 6}, {2, 1, 7}, {1, 2, 27}, {3, 3, 27}, {4, 4, 27}} {
		d, events := recordingQuartzInput(t)
		for _, e := range []desktopInputEvent{
			{Type: "mouse_down", Button: tc.button, X: -600, Y: 50},
			{Type: "mouse_move", Button: -1, X: -550, Y: 60, ShiftKey: true},
			{Type: "mouse_up", Button: tc.button, X: -540, Y: 65},
			{Type: "mouse_move", Button: -1},
		} {
			if err := d.Apply(e); err != nil {
				t.Fatal(err)
			}
		}
		drag := (*events)[1]
		if drag.typ != tc.drag || drag.button != tc.native || drag.point != (cgPoint{-550, 60}) || drag.flags != 0x10000|cgEventFlagMaskShift {
			t.Errorf("DOM button %d drag = %+v", tc.button, drag)
		}
		if (*events)[2].count != 1 || (*events)[3].typ != cgEventMouseMoved {
			t.Errorf("DOM button %d up and move = %+v", tc.button, (*events)[2:])
		}
	}
}

func TestDarwinClickSequence(t *testing.T) {
	d, events := recordingQuartzInput(t)
	click := func(button, x int, want int64) {
		t.Helper()
		for _, typ := range []string{"mouse_down", "mouse_up"} {
			if err := d.Apply(desktopInputEvent{Type: typ, Button: button, X: x, Y: -60}); err != nil {
				t.Fatal(err)
			}
			got := (*events)[len(*events)-1]
			if got.count != want || got.point != (cgPoint{float64(x), -60}) {
				t.Errorf("%s at %d = %+v, want count %d", typ, x, got, want)
			}
		}
	}
	click(0, 80, 1)
	click(0, 82, 2)
	click(0, 81, 3)
	click(0, 100, 1) // movement beyond click tolerance
	click(1, 100, 1) // different button
	click(0, 100, 1)
	d.lastClick.when = d.lastClick.when.Add(-2 * d.clickInterval)
	click(0, 100, 1)
}

func TestDarwinModifierSidesAndPixelWheel(t *testing.T) {
	d, events := recordingQuartzInput(t)
	const caps = 0x10000 // an unrelated native flag is preserved
	for _, tc := range []struct {
		e     desktopInputEvent
		flags uint64
	}{
		{desktopInputEvent{Type: "key_down", Code: "ShiftRight"}, cgEventFlagMaskShift | rightShift},
		{desktopInputEvent{Type: "key_down", Code: "ShiftLeft"}, cgEventFlagMaskShift | rightShift | leftShift},
		{desktopInputEvent{Type: "key_up", Code: "ShiftRight"}, cgEventFlagMaskShift | leftShift},
		{desktopInputEvent{Type: "key_down", Code: "KeyA", Key: "q", MetaKey: true}, cgEventFlagMaskCommand | cgEventFlagMaskShift | leftShift},
		{desktopInputEvent{Type: "mouse_down", CtrlKey: true}, cgEventFlagMaskControl | cgEventFlagMaskShift | leftShift},
		{desktopInputEvent{Type: "key_up", Code: "ShiftLeft"}, 0},
		{desktopInputEvent{Type: "wheel", X: -300, Y: 78, DeltaY: 3, DeltaX: -7, AltKey: true}, cgEventFlagMaskAlternate},
	} {
		if err := d.Apply(tc.e); err != nil {
			t.Fatal(err)
		}
		got := (*events)[len(*events)-1]
		if got.flags != tc.flags|caps {
			t.Errorf("%+v flags = %#x, want %#x", tc.e, got.flags, tc.flags|caps)
		}
	}
	if ev := (*events)[3]; ev.key != 0 || !ev.down {
		t.Errorf("physical KeyA with non-US key = %+v", ev)
	}
	if wheel := (*events)[6]; wheel.wheel != [3]int32{-3, 7, 0} || wheel.point != (cgPoint{-300, 78}) {
		t.Errorf("wheel axes/location = %+v", wheel)
	}
}

func TestDarwinFailedCreationDoesNotAdvanceSession(t *testing.T) {
	d, events := recordingQuartzInput(t)
	if err := d.Apply(desktopInputEvent{Type: "mouse_down", Button: 1}); err != nil {
		t.Fatal(err)
	}
	before := *d
	d.api.mouse = func(uintptr, uint32, cgPoint, uint32) uintptr { return 0 }
	d.api.keyboard = func(uintptr, uint16, bool) uintptr { return 0 }
	d.api.wheel = func(uintptr, uint32, uint32, int32, int32, int32) uintptr { return 0 }
	for _, e := range []desktopInputEvent{
		{Type: "mouse_up", Button: 1}, {Type: "mouse_down", Button: 2}, {Type: "mouse_move", X: 100},
		{Type: "key_down", Code: "ControlRight"}, {Type: "wheel", DeltaX: 1},
	} {
		if err := d.Apply(e); err == nil || *d != before || len(*events) != 1 {
			t.Fatalf("%+v: err=%v state=%+v posted=%d", e, err, d, len(*events))
		}
	}
}

func TestDarwinExtendedPhysicalCodes(t *testing.T) {
	for _, tc := range []struct {
		code, key string
		want      uint16
	}{
		{"IntlBackslash", "", 0x0a}, {"IntlRo", "", 0x5e}, {"NumpadComma", ",", 0x5f},
		{"NumLock", "", 0x47}, {"F17", "", 0x40}, {"", "F20", 0x5a},
		{"Unidentified", "Control", 0x3b}, {"", "Meta", 0x37}, {"Unsupported", "a", 0xff},
	} {
		if got := darwinKeycode(desktopInputEvent{Code: tc.code, Key: tc.key}); got != tc.want {
			t.Errorf("code=%q key=%q: %#x, want %#x", tc.code, tc.key, got, tc.want)
		}
	}
}

func TestDarwinCursorPointOnNegativeDisplay(t *testing.T) {
	if got := quartzCursorPoint(cgPoint{-1200, -4.6}); got != image.Pt(-1200, -5) {
		t.Errorf("negative display point = %v, want (-1200,-5)", got)
	}
}

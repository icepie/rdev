//go:build darwin

package client

import "testing"

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

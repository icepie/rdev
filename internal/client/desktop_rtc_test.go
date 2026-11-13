package client

import (
	"testing"

	"rdev/internal/protocol"
)

func TestValidRTCDesktopInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  protocol.Message
		want bool
	}{
		{"mouse move", protocol.Message{InputType: "mouse_move", X: 1920, Y: 1080, PointerType: "mouse"}, true},
		{"wheel boundary", protocol.Message{InputType: "wheel", DeltaX: -2000, DeltaY: 2000}, true},
		{"wheel too large", protocol.Message{InputType: "wheel", DeltaY: 2001}, false},
		{"negative coordinate", protocol.Message{InputType: "mouse_down", X: -1}, false},
		{"invalid button", protocol.Message{InputType: "mouse_up", Button: 5}, false},
		{"unsupported input", protocol.Message{InputType: "shell"}, false},
		{"key event", protocol.Message{InputType: "key_down", Key: "A", Code: "KeyA"}, true},
		{"oversized key", protocol.Message{InputType: "key_up", Key: string(make([]byte, 65))}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := validRTCDesktopInput(&tc.msg); got != tc.want {
				t.Fatalf("validRTCDesktopInput(%+v) = %v, want %v", tc.msg, got, tc.want)
			}
		})
	}
}

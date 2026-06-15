//go:build linux

package client

import (
	"fmt"
	"strings"

	"github.com/BurntSushi/xgb"
	"github.com/BurntSushi/xgb/xproto"
	"github.com/BurntSushi/xgb/xtest"
)

type x11DesktopInput struct {
	conn   *xgb.Conn
	screen *xproto.ScreenInfo
}

func newDesktopInput() (desktopInput, error) {
	conn, err := xgb.NewConn()
	if err != nil {
		return nil, fmt.Errorf("connect X11 input: %w", err)
	}
	if err := xtest.Init(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("XTEST input unavailable: %w", err)
	}
	return &x11DesktopInput{conn: conn, screen: xproto.Setup(conn).DefaultScreen(conn)}, nil
}

func (x *x11DesktopInput) Close() error {
	x.conn.Close()
	return nil
}

func (x *x11DesktopInput) Apply(event desktopInputEvent) error {
	switch event.Type {
	case "mouse_move":
		x.fake(xproto.MotionNotify, 0, event.X, event.Y)
	case "mouse_down":
		x.fake(xproto.MotionNotify, 0, event.X, event.Y)
		x.fake(xproto.ButtonPress, x11Button(event.Button), event.X, event.Y)
	case "mouse_up":
		x.fake(xproto.MotionNotify, 0, event.X, event.Y)
		x.fake(xproto.ButtonRelease, x11Button(event.Button), event.X, event.Y)
	case "wheel":
		if event.DeltaY != 0 {
			button := byte(5)
			if event.DeltaY < 0 {
				button = 4
			}
			x.fake(xproto.ButtonPress, button, event.X, event.Y)
			x.fake(xproto.ButtonRelease, button, event.X, event.Y)
		}
		if event.DeltaX != 0 {
			button := byte(7)
			if event.DeltaX < 0 {
				button = 6
			}
			x.fake(xproto.ButtonPress, button, event.X, event.Y)
			x.fake(xproto.ButtonRelease, button, event.X, event.Y)
		}
	case "key_down":
		if keycode := x11Keycode(event); keycode != 0 {
			x.fake(xproto.KeyPress, keycode, 0, 0)
		}
	case "key_up":
		if keycode := x11Keycode(event); keycode != 0 {
			x.fake(xproto.KeyRelease, keycode, 0, 0)
		}
	}
	return nil
}

func (x *x11DesktopInput) fake(typ, detail byte, rootX, rootY int) {
	xtest.FakeInput(x.conn, typ, detail, xproto.TimeCurrentTime, x.screen.Root, int16(rootX), int16(rootY), 0)
}

func x11Button(button int) byte {
	switch button {
	case 1:
		return 2
	case 2:
		return 3
	default:
		return 1
	}
}

func x11Keycode(event desktopInputEvent) byte {
	code := event.Code
	if strings.HasPrefix(code, "Key") && len(code) == 4 {
		return map[byte]byte{
			'A': 38, 'B': 56, 'C': 54, 'D': 40, 'E': 26, 'F': 41, 'G': 42, 'H': 43, 'I': 31, 'J': 44, 'K': 45, 'L': 46, 'M': 58,
			'N': 57, 'O': 32, 'P': 33, 'Q': 24, 'R': 27, 'S': 39, 'T': 28, 'U': 30, 'V': 55, 'W': 25, 'X': 53, 'Y': 29, 'Z': 52,
		}[code[3]]
	}
	if strings.HasPrefix(code, "Digit") && len(code) == 6 {
		return map[byte]byte{'1': 10, '2': 11, '3': 12, '4': 13, '5': 14, '6': 15, '7': 16, '8': 17, '9': 18, '0': 19}[code[5]]
	}
	switch code {
	case "Escape":
		return 9
	case "Backspace":
		return 22
	case "Tab":
		return 23
	case "Enter", "NumpadEnter":
		return 36
	case "ControlLeft":
		return 37
	case "ShiftLeft":
		return 50
	case "ShiftRight":
		return 62
	case "AltLeft":
		return 64
	case "Space":
		return 65
	case "CapsLock":
		return 66
	case "F1":
		return 67
	case "F2":
		return 68
	case "F3":
		return 69
	case "F4":
		return 70
	case "F5":
		return 71
	case "F6":
		return 72
	case "F7":
		return 73
	case "F8":
		return 74
	case "F9":
		return 75
	case "F10":
		return 76
	case "F11":
		return 95
	case "F12":
		return 96
	case "Home":
		return 110
	case "ArrowUp":
		return 111
	case "PageUp":
		return 112
	case "ArrowLeft":
		return 113
	case "ArrowRight":
		return 114
	case "End":
		return 115
	case "ArrowDown":
		return 116
	case "PageDown":
		return 117
	case "Insert":
		return 118
	case "Delete":
		return 119
	case "MetaLeft":
		return 133
	case "MetaRight":
		return 134
	case "ControlRight":
		return 105
	case "AltRight":
		return 108
	case "Minus":
		return 20
	case "Equal":
		return 21
	case "BracketLeft":
		return 34
	case "BracketRight":
		return 35
	case "Backslash":
		return 51
	case "Semicolon":
		return 47
	case "Quote":
		return 48
	case "Backquote":
		return 49
	case "Comma":
		return 59
	case "Period":
		return 60
	case "Slash":
		return 61
	case "Numpad0":
		return 90
	case "Numpad1":
		return 87
	case "Numpad2":
		return 88
	case "Numpad3":
		return 89
	case "Numpad4":
		return 83
	case "Numpad5":
		return 84
	case "Numpad6":
		return 85
	case "Numpad7":
		return 79
	case "Numpad8":
		return 80
	case "Numpad9":
		return 81
	case "NumpadAdd":
		return 86
	case "NumpadSubtract":
		return 82
	case "NumpadMultiply":
		return 63
	case "NumpadDivide":
		return 106
	case "NumpadDecimal":
		return 91
	}
	return 0
}

//go:build windows

package client

import "strings"

const (
	mouseEventMove       = 0x0001
	mouseEventLeftDown   = 0x0002
	mouseEventLeftUp     = 0x0004
	mouseEventRightDown  = 0x0008
	mouseEventRightUp    = 0x0010
	mouseEventMiddleDown = 0x0020
	mouseEventMiddleUp   = 0x0040
	mouseEventWheel      = 0x0800
	mouseEventHWheel     = 0x01000
	keyEventKeyUp        = 0x0002
)

var (
	procSetCursorPos = user32.NewProc("SetCursorPos")
	procMouseEvent   = user32.NewProc("mouse_event")
	procKeybdEvent   = user32.NewProc("keybd_event")
)

type windowsDesktopInput struct{}

func newDesktopInput() (desktopInput, error) { return windowsDesktopInput{}, nil }

func (windowsDesktopInput) Close() error { return nil }

func (windowsDesktopInput) Apply(event desktopInputEvent) error {
	switch event.Type {
	case "mouse_move":
		setCursorPos(event.X, event.Y)
	case "mouse_down":
		setCursorPos(event.X, event.Y)
		mouseButton(event.Button, true)
	case "mouse_up":
		setCursorPos(event.X, event.Y)
		mouseButton(event.Button, false)
	case "wheel":
		if event.DeltaY != 0 {
			procMouseEvent.Call(mouseEventWheel, 0, 0, uintptr(int32(-event.DeltaY)), 0)
		}
		if event.DeltaX != 0 {
			procMouseEvent.Call(mouseEventHWheel, 0, 0, uintptr(int32(event.DeltaX)), 0)
		}
	case "key_down":
		if vk := windowsVK(event); vk != 0 {
			procKeybdEvent.Call(uintptr(vk), 0, 0, 0)
		}
	case "key_up":
		if vk := windowsVK(event); vk != 0 {
			procKeybdEvent.Call(uintptr(vk), 0, keyEventKeyUp, 0)
		}
	}
	return nil
}

func setCursorPos(x, y int) {
	procSetCursorPos.Call(uintptr(int32(x)), uintptr(int32(y)))
	procMouseEvent.Call(mouseEventMove, 0, 0, 0, 0)
}

func mouseButton(button int, down bool) {
	var flag uintptr
	switch button {
	case 0:
		if down {
			flag = mouseEventLeftDown
		} else {
			flag = mouseEventLeftUp
		}
	case 1:
		if down {
			flag = mouseEventMiddleDown
		} else {
			flag = mouseEventMiddleUp
		}
	case 2:
		if down {
			flag = mouseEventRightDown
		} else {
			flag = mouseEventRightUp
		}
	}
	if flag != 0 {
		procMouseEvent.Call(flag, 0, 0, 0, 0)
	}
}

func windowsVK(event desktopInputEvent) byte {
	code := event.Code
	if strings.HasPrefix(code, "Key") && len(code) == 4 {
		return code[3]
	}
	if strings.HasPrefix(code, "Digit") && len(code) == 6 {
		return code[5]
	}
	switch code {
	case "Backspace":
		return 0x08
	case "Tab":
		return 0x09
	case "Enter", "NumpadEnter":
		return 0x0d
	case "ShiftLeft", "ShiftRight":
		return 0x10
	case "ControlLeft", "ControlRight":
		return 0x11
	case "AltLeft", "AltRight":
		return 0x12
	case "Pause":
		return 0x13
	case "CapsLock":
		return 0x14
	case "Escape":
		return 0x1b
	case "Space":
		return 0x20
	case "PageUp":
		return 0x21
	case "PageDown":
		return 0x22
	case "End":
		return 0x23
	case "Home":
		return 0x24
	case "ArrowLeft":
		return 0x25
	case "ArrowUp":
		return 0x26
	case "ArrowRight":
		return 0x27
	case "ArrowDown":
		return 0x28
	case "Insert":
		return 0x2d
	case "Delete":
		return 0x2e
	case "MetaLeft", "MetaRight":
		return 0x5b
	case "Numpad0":
		return 0x60
	case "Numpad1":
		return 0x61
	case "Numpad2":
		return 0x62
	case "Numpad3":
		return 0x63
	case "Numpad4":
		return 0x64
	case "Numpad5":
		return 0x65
	case "Numpad6":
		return 0x66
	case "Numpad7":
		return 0x67
	case "Numpad8":
		return 0x68
	case "Numpad9":
		return 0x69
	case "NumpadMultiply":
		return 0x6a
	case "NumpadAdd":
		return 0x6b
	case "NumpadSubtract":
		return 0x6d
	case "NumpadDecimal":
		return 0x6e
	case "NumpadDivide":
		return 0x6f
	case "F1":
		return 0x70
	case "F2":
		return 0x71
	case "F3":
		return 0x72
	case "F4":
		return 0x73
	case "F5":
		return 0x74
	case "F6":
		return 0x75
	case "F7":
		return 0x76
	case "F8":
		return 0x77
	case "F9":
		return 0x78
	case "F10":
		return 0x79
	case "F11":
		return 0x7a
	case "F12":
		return 0x7b
	case "Semicolon":
		return 0xba
	case "Equal":
		return 0xbb
	case "Comma":
		return 0xbc
	case "Minus":
		return 0xbd
	case "Period":
		return 0xbe
	case "Slash":
		return 0xbf
	case "Backquote":
		return 0xc0
	case "BracketLeft":
		return 0xdb
	case "Backslash":
		return 0xdc
	case "BracketRight":
		return 0xdd
	case "Quote":
		return 0xde
	}
	if len(event.Key) == 1 {
		ch := strings.ToUpper(event.Key)[0]
		if (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') {
			return ch
		}
	}
	return 0
}

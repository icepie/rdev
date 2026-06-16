//go:build windows

package client

import (
	"fmt"
	"strings"
	"unsafe"

	"rdev/internal/protocol"
)

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
	procSetCursorPos             = user32.NewProc("SetCursorPos")
	procMouseEvent               = user32.NewProc("mouse_event")
	procKeybdEvent               = user32.NewProc("keybd_event")
	procInitializeTouchInjection = user32.NewProc("InitializeTouchInjection")
	procInjectTouchInput         = user32.NewProc("InjectTouchInput")
)

type windowsDesktopInput struct {
	touch *windowsTouchInput
}

type windowsTouchInput struct {
	active map[uint32]bool
}

type pointerInfo struct {
	PointerType           uint32
	PointerID             uint32
	FrameID               uint32
	PointerFlags          uint32
	SourceDevice          uintptr
	HwndTarget            uintptr
	PtPixelLocation       winPoint
	PtHimetricLocation    winPoint
	PtPixelLocationRaw    winPoint
	PtHimetricLocationRaw winPoint
	Time                  uint32
	HistoryCount          uint32
	InputData             int32
	KeyStates             uint32
	PerformanceCount      uint64
	ButtonChangeType      uint32
}

type pointerTouchInfo struct {
	PointerInfo pointerInfo
	TouchFlags  uint32
	TouchMask   uint32
	Contact     winRect
	ContactRaw  winRect
	Orientation uint32
	Pressure    uint32
}

func platformDesktopInputOptions() []protocol.DesktopInputBackend {
	options := []protocol.DesktopInputBackend{{ID: "win32", Label: "Win32 input", Kinds: []string{"mouse", "keyboard"}, Requires: []string{"Windows 7+"}}}
	if windowsTouchInjectionAvailable() {
		options = append(options, protocol.DesktopInputBackend{ID: "win32-touch", Label: "Win32 Touch Injection", Kinds: []string{"mouse", "keyboard", "touch", "pen"}, Requires: []string{"Windows 8+", "interactive desktop"}, Reason: "pen input is accepted and injected through the Windows touch injection path until native pen injection is available"})
	}
	return options
}

func newDesktopInput(backend string) (desktopInput, error) {
	switch backend {
	case "", "auto", "win32":
		return windowsDesktopInput{}, nil
	case "win32-touch":
		touch, err := newWindowsTouchInput()
		if err != nil {
			return nil, err
		}
		return windowsDesktopInput{touch: touch}, nil
	default:
		return nil, fmt.Errorf("unsupported desktop input backend %q", backend)
	}
}

func (w windowsDesktopInput) Backend() string {
	if w.touch != nil {
		return "win32-touch"
	}
	return "win32"
}

func (windowsDesktopInput) Close() error { return nil }

func (w windowsDesktopInput) Apply(event desktopInputEvent) error {
	if (event.PointerType == "touch" || event.PointerType == "pen") && w.touch != nil {
		return w.touch.Apply(event)
	}
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

func windowsTouchInjectionAvailable() bool {
	return procInitializeTouchInjection.Find() == nil && procInjectTouchInput.Find() == nil
}

func newWindowsTouchInput() (*windowsTouchInput, error) {
	if !windowsTouchInjectionAvailable() {
		return nil, fmt.Errorf("Windows touch injection is unavailable")
	}
	r1, _, err := procInitializeTouchInjection.Call(8, 0)
	if r1 == 0 {
		return nil, fmt.Errorf("InitializeTouchInjection failed: %w", err)
	}
	return &windowsTouchInput{active: make(map[uint32]bool)}, nil
}

func (t *windowsTouchInput) Apply(event desktopInputEvent) error {
	pointerID := uint32(event.PointerID)
	if pointerID == 0 {
		pointerID = 1
	}
	flags := uint32(0x00000002)
	switch event.Type {
	case "mouse_down":
		flags |= 0x00010000 | 0x00000004
		t.active[pointerID] = true
	case "mouse_move":
		flags |= 0x00020000
		if t.active[pointerID] {
			flags |= 0x00000004
		}
	case "mouse_up":
		flags |= 0x00040000
		delete(t.active, pointerID)
	default:
		return nil
	}
	pressure := uint32(event.Pressure * 1024)
	if pressure == 0 && flags&0x00000004 != 0 {
		pressure = 512
	}
	if pressure > 1024 {
		pressure = 1024
	}
	contact := winRect{Left: int32(event.X - 2), Top: int32(event.Y - 2), Right: int32(event.X + 2), Bottom: int32(event.Y + 2)}
	info := pointerTouchInfo{
		PointerInfo: pointerInfo{PointerType: 2, PointerID: pointerID, PointerFlags: flags, PtPixelLocation: winPoint{X: int32(event.X), Y: int32(event.Y)}},
		TouchMask:   0x00000001 | 0x00000002 | 0x00000004,
		Contact:     contact,
		ContactRaw:  contact,
		Orientation: 90,
		Pressure:    pressure,
	}
	r1, _, err := procInjectTouchInput.Call(1, uintptr(unsafe.Pointer(&info)))
	if r1 == 0 {
		return fmt.Errorf("InjectTouchInput failed: %w", err)
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

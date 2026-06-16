//go:build darwin

package client

import (
	"fmt"
	"strings"
	"sync"

	"github.com/ebitengine/purego"
	"rdev/internal/protocol"
)

const (
	cgEventLeftMouseDown  = 1
	cgEventLeftMouseUp    = 2
	cgEventRightMouseDown = 3
	cgEventRightMouseUp   = 4
	cgEventMouseMoved     = 5
	cgEventKeyDown        = 10
	cgEventKeyUp          = 11
	cgEventScrollWheel    = 22
	cgEventOtherMouseDown = 25
	cgEventOtherMouseUp   = 26

	cgMouseButtonLeft   = 0
	cgMouseButtonRight  = 1
	cgMouseButtonCenter = 2

	cgHIDEventTap              = 0
	cgAnnotatedSessionEventTap = 2

	cgEventFlagMaskShift     = 0x00020000
	cgEventFlagMaskControl   = 0x00040000
	cgEventFlagMaskAlternate = 0x00080000
	cgEventFlagMaskCommand   = 0x00100000

	cgMouseEventClickState   = 1
	cgMouseEventButtonNumber = 3
)

type cgPoint struct {
	X float64
	Y float64
}

type darwinDesktopInput struct{}

var (
	darwinInputOnce sync.Once
	darwinInputErr  error

	cgEventCreateKeyboardEvent  func(uintptr, uint16, bool) uintptr
	cgEventCreateMouseEvent     func(uintptr, uint32, cgPoint, uint32) uintptr
	cgEventSetFlags             func(uintptr, uint64)
	cgEventSetIntegerValueField func(uintptr, uint32, int64)
	cgEventPost                 func(uint32, uintptr)
	cfRelease                   func(uintptr)
	cgEventCreateScrollWheel    uintptr
	axIsProcessTrusted          func() bool
)

func platformDesktopInputOptions() []protocol.DesktopInputBackend {
	if initDarwinInput() != nil || !darwinInputTrusted() {
		return nil
	}
	return []protocol.DesktopInputBackend{{ID: "quartz", Label: "macOS Quartz", Kinds: []string{"mouse", "keyboard"}, Requires: []string{"Accessibility permission"}}}
}

func newDesktopInput(backend string) (desktopInput, error) {
	switch backend {
	case "", "auto", "quartz":
		if err := initDarwinInput(); err != nil {
			return nil, err
		}
		if !darwinInputTrusted() {
			return nil, fmt.Errorf("macOS Accessibility permission is required for Quartz desktop input")
		}
		return darwinDesktopInput{}, nil
	default:
		return nil, fmt.Errorf("unsupported desktop input backend %q", backend)
	}
}

func initDarwinInput() error {
	darwinInputOnce.Do(func() {
		appServices, err := purego.Dlopen("/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			darwinInputErr = fmt.Errorf("open ApplicationServices: %w", err)
			return
		}
		purego.RegisterLibFunc(&cgEventCreateKeyboardEvent, appServices, "CGEventCreateKeyboardEvent")
		purego.RegisterLibFunc(&cgEventCreateMouseEvent, appServices, "CGEventCreateMouseEvent")
		purego.RegisterLibFunc(&cgEventSetFlags, appServices, "CGEventSetFlags")
		purego.RegisterLibFunc(&cgEventSetIntegerValueField, appServices, "CGEventSetIntegerValueField")
		purego.RegisterLibFunc(&cgEventPost, appServices, "CGEventPost")
		purego.RegisterLibFunc(&cfRelease, appServices, "CFRelease")
		purego.RegisterLibFunc(&axIsProcessTrusted, appServices, "AXIsProcessTrusted")
		cgEventCreateScrollWheel, _ = purego.Dlsym(appServices, "CGEventCreateScrollWheelEvent")
	})
	return darwinInputErr
}

func darwinInputTrusted() bool {
	return axIsProcessTrusted != nil && axIsProcessTrusted()
}

func (darwinDesktopInput) Backend() string { return "quartz" }
func (darwinDesktopInput) Close() error    { return nil }

func (darwinDesktopInput) Apply(event desktopInputEvent) error {
	switch event.Type {
	case "mouse_move":
		return postDarwinMouse(cgEventMouseMoved, event, cgMouseButtonLeft)
	case "mouse_down":
		return postDarwinMouse(darwinMouseEvent(event.Button, true), event, darwinMouseButton(event.Button))
	case "mouse_up":
		return postDarwinMouse(darwinMouseEvent(event.Button, false), event, darwinMouseButton(event.Button))
	case "wheel":
		return postDarwinWheel(event)
	case "key_down", "key_up":
		key := darwinKeycode(event)
		if key == 0xff {
			return nil
		}
		return postDarwinKey(key, event.Type == "key_down", darwinEventFlags(event))
	}
	return nil
}

func postDarwinKey(key uint16, down bool, flags uint64) error {
	event := cgEventCreateKeyboardEvent(0, key, down)
	if event == 0 {
		return fmt.Errorf("CGEventCreateKeyboardEvent failed")
	}
	if flags != 0 {
		cgEventSetFlags(event, flags)
	}
	cgEventPost(cgAnnotatedSessionEventTap, event)
	cfRelease(event)
	return nil
}

func postDarwinMouse(eventType uint32, event desktopInputEvent, button uint32) error {
	cgEvent := cgEventCreateMouseEvent(0, eventType, cgPoint{X: float64(event.X), Y: float64(event.Y)}, button)
	if cgEvent == 0 {
		return fmt.Errorf("CGEventCreateMouseEvent failed")
	}
	if eventType == cgEventLeftMouseDown || eventType == cgEventLeftMouseUp || eventType == cgEventRightMouseDown || eventType == cgEventRightMouseUp || eventType == cgEventOtherMouseDown || eventType == cgEventOtherMouseUp {
		cgEventSetIntegerValueField(cgEvent, cgMouseEventClickState, 1)
		cgEventSetIntegerValueField(cgEvent, cgMouseEventButtonNumber, int64(button))
	}
	cgEventPost(cgHIDEventTap, cgEvent)
	cfRelease(cgEvent)
	return nil
}

func postDarwinWheel(event desktopInputEvent) error {
	if cgEventCreateScrollWheel == 0 || (event.DeltaX == 0 && event.DeltaY == 0) {
		return nil
	}
	wheelY := uintptr(int32(-darwinWheelStep(event.DeltaY)))
	wheelX := uintptr(int32(darwinWheelStep(event.DeltaX)))
	r1, _, _ := purego.SyscallN(cgEventCreateScrollWheel, 0, 0, 2, wheelY, wheelX)
	if r1 == 0 {
		return nil
	}
	cgEventPost(cgHIDEventTap, r1)
	cfRelease(r1)
	return nil
}

func darwinWheelStep(delta int) int {
	if delta == 0 {
		return 0
	}
	step := delta / 120
	if step == 0 {
		if delta > 0 {
			return 1
		}
		return -1
	}
	return step
}

func darwinMouseButton(button int) uint32 {
	switch button {
	case 1:
		return cgMouseButtonCenter
	case 2:
		return cgMouseButtonRight
	case 3, 4:
		return uint32(button)
	default:
		return cgMouseButtonLeft
	}
}

func darwinMouseEvent(button int, down bool) uint32 {
	switch button {
	case 1, 3, 4:
		if down {
			return cgEventOtherMouseDown
		}
		return cgEventOtherMouseUp
	case 2:
		if down {
			return cgEventRightMouseDown
		}
		return cgEventRightMouseUp
	default:
		if down {
			return cgEventLeftMouseDown
		}
		return cgEventLeftMouseUp
	}
}

func darwinEventFlags(event desktopInputEvent) uint64 {
	var flags uint64
	if event.ShiftKey {
		flags |= cgEventFlagMaskShift
	}
	if event.CtrlKey {
		flags |= cgEventFlagMaskControl
	}
	if event.AltKey {
		flags |= cgEventFlagMaskAlternate
	}
	if event.MetaKey {
		flags |= cgEventFlagMaskCommand
	}
	return flags
}

func darwinCodeFromKey(key string) string {
	if key == "" {
		return ""
	}
	lower := strings.ToLower(key)
	if len(lower) == 1 {
		ch := lower[0]
		if ch >= 'a' && ch <= 'z' {
			return "Key" + strings.ToUpper(lower)
		}
		if ch >= '0' && ch <= '9' {
			return "Digit" + lower
		}
		switch ch {
		case ' ':
			return "Space"
		case '-':
			return "Minus"
		case '=':
			return "Equal"
		case '[':
			return "BracketLeft"
		case ']':
			return "BracketRight"
		case '\\':
			return "Backslash"
		case ';':
			return "Semicolon"
		case '\'':
			return "Quote"
		case ',':
			return "Comma"
		case '.':
			return "Period"
		case '/':
			return "Slash"
		case '`':
			return "Backquote"
		}
	}
	switch key {
	case "Enter", "Tab", "Backspace", "Escape", "Delete", "Home", "End", "PageUp", "PageDown":
		return key
	case " ":
		return "Space"
	case "ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown":
		return key
	}
	return ""
}

func darwinKeycode(event desktopInputEvent) uint16 {
	code := event.Code
	if code == "" {
		code = darwinCodeFromKey(event.Key)
	}
	if strings.HasPrefix(code, "Key") && len(code) == 4 {
		return map[byte]uint16{
			'A': 0x00, 'S': 0x01, 'D': 0x02, 'F': 0x03, 'H': 0x04, 'G': 0x05, 'Z': 0x06, 'X': 0x07, 'C': 0x08, 'V': 0x09, 'B': 0x0b,
			'Q': 0x0c, 'W': 0x0d, 'E': 0x0e, 'R': 0x0f, 'Y': 0x10, 'T': 0x11, 'O': 0x1f, 'U': 0x20, 'I': 0x22, 'P': 0x23,
			'L': 0x25, 'J': 0x26, 'K': 0x28, 'N': 0x2d, 'M': 0x2e,
		}[code[3]]
	}
	if strings.HasPrefix(code, "Digit") && len(code) == 6 {
		return map[byte]uint16{'1': 0x12, '2': 0x13, '3': 0x14, '4': 0x15, '6': 0x16, '5': 0x17, '9': 0x19, '7': 0x1a, '8': 0x1c, '0': 0x1d}[code[5]]
	}
	switch code {
	case "Enter", "NumpadEnter":
		return 0x24
	case "Tab":
		return 0x30
	case "Space":
		return 0x31
	case "Backspace":
		return 0x33
	case "Escape":
		return 0x35
	case "MetaLeft", "MetaRight":
		return 0x37
	case "ShiftLeft", "ShiftRight":
		return 0x38
	case "CapsLock":
		return 0x39
	case "AltLeft", "AltRight":
		return 0x3a
	case "ControlLeft", "ControlRight":
		return 0x3b
	case "ArrowLeft":
		return 0x7b
	case "ArrowRight":
		return 0x7c
	case "ArrowDown":
		return 0x7d
	case "ArrowUp":
		return 0x7e
	case "Minus":
		return 0x1b
	case "Equal":
		return 0x18
	case "BracketRight":
		return 0x1e
	case "BracketLeft":
		return 0x21
	case "Quote":
		return 0x27
	case "Semicolon":
		return 0x29
	case "Backslash":
		return 0x2a
	case "Comma":
		return 0x2b
	case "Slash":
		return 0x2c
	case "Period":
		return 0x2f
	case "Backquote":
		return 0x32
	case "Home":
		return 0x73
	case "PageUp":
		return 0x74
	case "Delete":
		return 0x75
	case "End":
		return 0x77
	case "PageDown":
		return 0x79
	case "F1":
		return 0x7a
	case "F2":
		return 0x78
	case "F3":
		return 0x63
	case "F4":
		return 0x76
	case "F5":
		return 0x60
	case "F6":
		return 0x61
	case "F7":
		return 0x62
	case "F8":
		return 0x64
	case "F9":
		return 0x65
	case "F10":
		return 0x6d
	case "F11":
		return 0x67
	case "F12":
		return 0x6f
	}
	return 0xff
}

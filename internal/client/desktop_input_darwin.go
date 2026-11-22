//go:build darwin

package client

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"rdev/internal/protocol"
)

const (
	cgEventLeftMouseDown     = 1
	cgEventLeftMouseUp       = 2
	cgEventRightMouseDown    = 3
	cgEventRightMouseUp      = 4
	cgEventMouseMoved        = 5
	cgEventLeftMouseDragged  = 6
	cgEventRightMouseDragged = 7
	cgEventOtherMouseDown    = 25
	cgEventOtherMouseUp      = 26
	cgEventOtherMouseDragged = 27

	cgMouseButtonLeft   = 0
	cgMouseButtonRight  = 1
	cgMouseButtonCenter = 2

	cgHIDEventTap = 0

	cgEventFlagMaskShift     = 0x00020000
	cgEventFlagMaskControl   = 0x00040000
	cgEventFlagMaskAlternate = 0x00080000
	cgEventFlagMaskCommand   = 0x00100000

	cgMouseEventClickState = 1
	cgScrollEventUnitPixel = 0
)

type cgPoint struct {
	X float64
	Y float64
}

type darwinDesktopInput struct {
	api           *darwinInputAPI
	clickInterval time.Duration
	lastClick     darwinClick
	buttonCounts  [5]int64 // nonzero while held; retained through drags and the matching up
	modifierSides uint64
}

type darwinClick struct {
	when   time.Time
	point  cgPoint
	button uint32
	count  int64
}

func (c darwinClick) near(point cgPoint) bool {
	const slop = 4 // global display points, independent of Retina backing scale
	dx, dy := point.X-c.point.X, point.Y-c.point.Y
	return dx >= -slop && dx <= slop && dy >= -slop && dy <= slop
}

type darwinInputAPI struct {
	keyboard    func(uintptr, uint16, bool) uintptr
	mouse       func(uintptr, uint32, cgPoint, uint32) uintptr
	wheel       func(uintptr, uint32, uint32, int32, int32, int32) uintptr
	setLocation func(uintptr, cgPoint)
	getFlags    func(uintptr) uint64
	setFlags    func(uintptr, uint64)
	setInteger  func(uintptr, uint32, int64)
	post        func(uint32, uintptr)
	release     func(uintptr)
}

var (
	darwinInputOnce             sync.Once
	darwinInputErr              error
	quartzInput                 darwinInputAPI
	axIsProcessTrusted          func() bool
	doubleClickIntervalSelector objc.SEL
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
		interval := objc.Send[float64](objc.ID(objc.GetClass("NSEvent")), doubleClickIntervalSelector)
		return &darwinDesktopInput{api: &quartzInput, clickInterval: time.Duration(interval * float64(time.Second))}, nil
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
		if _, err := purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_NOW|purego.RTLD_GLOBAL); err != nil {
			darwinInputErr = fmt.Errorf("open AppKit: %w", err)
			return
		}
		doubleClickIntervalSelector = objc.RegisterName("doubleClickInterval")
		purego.RegisterLibFunc(&quartzInput.keyboard, appServices, "CGEventCreateKeyboardEvent")
		purego.RegisterLibFunc(&quartzInput.mouse, appServices, "CGEventCreateMouseEvent")
		purego.RegisterLibFunc(&quartzInput.setLocation, appServices, "CGEventSetLocation")
		purego.RegisterLibFunc(&quartzInput.getFlags, appServices, "CGEventGetFlags")
		purego.RegisterLibFunc(&quartzInput.setFlags, appServices, "CGEventSetFlags")
		purego.RegisterLibFunc(&quartzInput.setInteger, appServices, "CGEventSetIntegerValueField")
		purego.RegisterLibFunc(&quartzInput.post, appServices, "CGEventPost")
		purego.RegisterLibFunc(&quartzInput.release, appServices, "CFRelease")
		purego.RegisterLibFunc(&axIsProcessTrusted, appServices, "AXIsProcessTrusted")
		// Unlike the variadic constructor, both signed axes use the ordinary C ABI on Apple Silicon.
		purego.RegisterLibFunc(&quartzInput.wheel, appServices, "CGEventCreateScrollWheelEvent2")
	})
	return darwinInputErr
}

func darwinInputTrusted() bool {
	return axIsProcessTrusted != nil && axIsProcessTrusted()
}

func (*darwinDesktopInput) Backend() string { return "quartz" }
func (*darwinDesktopInput) Close() error    { return nil }

func (d *darwinDesktopInput) Apply(e desktopInputEvent) error {
	point := cgPoint{X: float64(e.X), Y: float64(e.Y)} // mapPoint already returns global display points
	flags := darwinEventFlags(e)
	switch e.Type {
	case "mouse_down", "mouse_up":
		if e.Button < 0 || e.Button >= len(d.buttonCounts) {
			return fmt.Errorf("unsupported Quartz mouse button %d", e.Button)
		}
		button := darwinMouseButton(e.Button)
		down := e.Type == "mouse_down"
		click := d.lastClick
		count := d.buttonCounts[button]
		if down {
			now := time.Now()
			count = 1
			if d.buttonCounts[button] == 0 && click.button == button && click.near(point) && now.Sub(click.when) <= d.clickInterval {
				count += click.count
			}
			click = darwinClick{when: now, point: point, button: button, count: count}
		}
		if err := d.mouse(darwinMouseEvent(e.Button, down), point, button, max(1, count), flags); err != nil {
			return err
		}
		if down {
			d.lastClick = click
			d.buttonCounts[button] = count
		} else {
			d.buttonCounts[button] = 0
			if !click.near(point) {
				d.lastClick = darwinClick{}
			}
		}
	case "mouse_move":
		// Pointermove.button identifies a transition, not the button being held.
		typ, button := uint32(cgEventMouseMoved), uint32(cgMouseButtonLeft)
		for i, count := range d.buttonCounts {
			if count != 0 {
				button = uint32(i)
				typ = cgEventOtherMouseDragged
				if i == cgMouseButtonLeft {
					typ = cgEventLeftMouseDragged
				} else if i == cgMouseButtonRight {
					typ = cgEventRightMouseDragged
				}
				break
			}
		}
		if err := d.mouse(typ, point, button, d.buttonCounts[button], flags); err != nil {
			return err
		}
		if !d.lastClick.near(point) {
			d.lastClick = darwinClick{}
		}
	case "wheel":
		if e.DeltaX == 0 && e.DeltaY == 0 {
			return nil
		}
		// DOM positive deltas scroll down/right; Quartz positive deltas scroll up/left.
		ev := d.api.wheel(0, cgScrollEventUnitPixel, 2, -int32(e.DeltaY), -int32(e.DeltaX), 0)
		if ev == 0 {
			return fmt.Errorf("CGEventCreateScrollWheelEvent2 failed")
		}
		d.api.setLocation(ev, point)
		d.post(ev, flags, d.modifierSides)
	case "key_down", "key_up":
		key := darwinKeycode(e)
		if key == 0xff {
			return nil
		}
		down := e.Type == "key_down"
		ev := d.api.keyboard(0, key, down)
		if ev == 0 {
			return fmt.Errorf("CGEventCreateKeyboardEvent failed")
		}
		sides := d.modifierSides
		if side := darwinModifierSide(key); down {
			sides |= side
		} else {
			sides &^= side
		}
		d.post(ev, flags, sides)
		d.modifierSides = sides
	}
	return nil
}

func (d *darwinDesktopInput) mouse(typ uint32, point cgPoint, button uint32, count int64, flags uint64) error {
	ev := d.api.mouse(0, typ, point, button)
	if ev == 0 {
		return fmt.Errorf("CGEventCreateMouseEvent failed")
	}
	if count != 0 {
		d.api.setInteger(ev, cgMouseEventClickState, count)
	}
	d.post(ev, flags, d.modifierSides)
	return nil
}

// IOLLEvent.h: NX_DEVICE*KEYMASK. Tracking both sides avoids releasing Shift,
// for example, when only one of two held Shift keys goes up. VNC sends key
// transitions without aggregate flags; a browser can also send flags alone.
const (
	leftControl  = 0x0001
	leftShift    = 0x0002
	rightShift   = 0x0004
	leftCommand  = 0x0008
	rightCommand = 0x0010
	leftOption   = 0x0020
	rightOption  = 0x0040
	rightControl = 0x2000
)

func darwinModifierSide(key uint16) uint64 {
	switch key {
	case 0x3b:
		return leftControl
	case 0x3e:
		return rightControl
	case 0x38:
		return leftShift
	case 0x3c:
		return rightShift
	case 0x37:
		return leftCommand
	case 0x36:
		return rightCommand
	case 0x3a:
		return leftOption
	case 0x3d:
		return rightOption
	}
	return 0
}

func (d *darwinDesktopInput) post(ev uintptr, flags, sides uint64) {
	const modifiers = cgEventFlagMaskShift | cgEventFlagMaskControl | cgEventFlagMaskCommand | cgEventFlagMaskAlternate |
		leftControl | rightControl | leftShift | rightShift | leftCommand | rightCommand | leftOption | rightOption
	if sides&(leftShift|rightShift) != 0 {
		flags |= cgEventFlagMaskShift
	}
	if sides&(leftControl|rightControl) != 0 {
		flags |= cgEventFlagMaskControl
	}
	if sides&(leftOption|rightOption) != 0 {
		flags |= cgEventFlagMaskAlternate
	}
	if sides&(leftCommand|rightCommand) != 0 {
		flags |= cgEventFlagMaskCommand
	}
	// Preserve unrelated native flags such as Caps Lock, Fn and numeric pad.
	d.api.setFlags(ev, d.api.getFlags(ev)&^modifiers|flags|sides)
	d.api.post(cgHIDEventTap, ev)
	d.api.release(ev)
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
	case "Shift":
		return "ShiftLeft"
	case "Control":
		return "ControlLeft"
	case "Alt":
		return "AltLeft"
	case "Meta":
		return "MetaLeft"
	default:
		return key // named keys such as Insert, F1 and the arrows already match a DOM code
	}
}

var darwinLetterKeycodes = map[byte]uint16{
	'A': 0x00, 'S': 0x01, 'D': 0x02, 'F': 0x03, 'H': 0x04, 'G': 0x05, 'Z': 0x06, 'X': 0x07, 'C': 0x08, 'V': 0x09, 'B': 0x0b,
	'Q': 0x0c, 'W': 0x0d, 'E': 0x0e, 'R': 0x0f, 'Y': 0x10, 'T': 0x11, 'O': 0x1f, 'U': 0x20, 'I': 0x22, 'P': 0x23,
	'L': 0x25, 'J': 0x26, 'K': 0x28, 'N': 0x2d, 'M': 0x2e,
}

var darwinDigitKeycodes = map[byte]uint16{
	'1': 0x12, '2': 0x13, '3': 0x14, '4': 0x15, '6': 0x16,
	'5': 0x17, '9': 0x19, '7': 0x1a, '8': 0x1c, '0': 0x1d,
}

func darwinKeycode(event desktopInputEvent) uint16 {
	code := event.Code
	if code == "" || code == "Unidentified" {
		code = darwinCodeFromKey(event.Key)
	}
	if strings.HasPrefix(code, "Key") && len(code) == 4 {
		if key, ok := darwinLetterKeycodes[code[3]]; ok {
			return key
		}
	}
	if strings.HasPrefix(code, "Digit") && len(code) == 6 {
		if key, ok := darwinDigitKeycodes[code[5]]; ok {
			return key
		}
	}
	switch code {
	case "Enter":
		return 0x24
	case "NumpadEnter":
		return 0x4c
	case "Tab":
		return 0x30
	case "Space":
		return 0x31
	case "Backspace":
		return 0x33
	case "Escape":
		return 0x35
	case "MetaLeft":
		return 0x37
	case "MetaRight":
		return 0x36
	case "ShiftLeft":
		return 0x38
	case "ShiftRight":
		return 0x3c
	case "CapsLock":
		return 0x39
	case "AltLeft":
		return 0x3a
	case "AltRight":
		return 0x3d
	case "ControlLeft":
		return 0x3b
	case "ControlRight":
		return 0x3e
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
	case "IntlBackslash":
		return 0x0a // ISO Section
	case "IntlYen":
		return 0x5d
	case "IntlRo":
		return 0x5e // JIS Underscore
	case "Lang1":
		return 0x68 // JIS Kana
	case "Lang2":
		return 0x66 // JIS Eisu
	case "Comma":
		return 0x2b
	case "Slash":
		return 0x2c
	case "Period":
		return 0x2f
	case "Backquote":
		return 0x32
	case "Insert":
		return 0x72
	case "NumpadDecimal":
		return 0x41
	case "NumpadComma":
		return 0x5f
	case "NumLock", "NumpadClear", "Clear":
		return 0x47 // Apple keyboards have Clear in this position
	case "NumpadMultiply":
		return 0x43
	case "NumpadAdd":
		return 0x45
	case "NumpadDivide":
		return 0x4b
	case "NumpadSubtract":
		return 0x4e
	case "NumpadEqual":
		return 0x51
	case "Numpad0":
		return 0x52
	case "Numpad1":
		return 0x53
	case "Numpad2":
		return 0x54
	case "Numpad3":
		return 0x55
	case "Numpad4":
		return 0x56
	case "Numpad5":
		return 0x57
	case "Numpad6":
		return 0x58
	case "Numpad7":
		return 0x59
	case "Numpad8":
		return 0x5b
	case "Numpad9":
		return 0x5c
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
	case "F13":
		return 0x69
	case "F14":
		return 0x6b
	case "F15":
		return 0x71
	case "F16":
		return 0x6a
	case "F17":
		return 0x40
	case "F18":
		return 0x4f
	case "F19":
		return 0x50
	case "F20":
		return 0x5a
	}
	return 0xff
}

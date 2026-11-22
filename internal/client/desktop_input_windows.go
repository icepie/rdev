//go:build windows

package client

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
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
	mouseEventXDown      = 0x0080
	mouseEventXUp        = 0x0100
	mouseEventWheel      = 0x0800
	mouseEventHWheel     = 0x01000
	keyEventExtendedKey  = 0x0001
	keyEventKeyUp        = 0x0002
	maxTouchContacts     = 8

	pointerFlagInRange   = 0x00000002
	pointerFlagInContact = 0x00000004
	pointerFlagDown      = 0x00010000
	pointerFlagUpdate    = 0x00020000
	pointerFlagUp        = 0x00040000

	touchMaskContactArea = 0x00000001
	touchMaskOrientation = 0x00000002
	touchMaskPressure    = 0x00000004
	touchFeedbackDefault = 0x00000001
)

var (
	procSetCursorPos             = user32.NewProc("SetCursorPos")
	procMouseEvent               = user32.NewProc("mouse_event")
	procKeybdEvent               = user32.NewProc("keybd_event")
	procInitializeTouchInjection = user32.NewProc("InitializeTouchInjection")
	procInjectTouchInput         = user32.NewProc("InjectTouchInput")
	processTouch                 = windowsTouchInjector{inject: injectWindowsTouchFrame}
)

type windowsDesktopInput struct {
	touch *windowsTouchInput
}

type windowsTouchInput struct {
	injector *windowsTouchInjector
	denied   bool
}

// InitializeTouchInjection and the set of contacts in a frame are process-wide.
type windowsTouchInjector struct {
	mu          sync.Mutex
	initialized bool
	active      [maxTouchContacts]windowsTouchContact
	frame       [maxTouchContacts]pointerTouchInfo
	inject      func([]pointerTouchInfo) error
}

type windowsTouchContact struct {
	owner     *windowsTouchInput
	pointerID int
	info      pointerTouchInfo
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
	_                     uint32 // Win32 aligns UINT64 (and the enclosing structure) to 8 even on x86.
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
	win32 := protocol.DesktopInputBackend{
		ID:       "win32",
		Label:    "Win32 mouse and keyboard",
		Kinds:    []string{"mouse", "keyboard"},
		Requires: []string{"Windows 7+"},
	}
	if !windowsTouchInjectionAvailable() {
		return []protocol.DesktopInputBackend{win32}
	}
	return []protocol.DesktopInputBackend{
		{
			ID:       "win32-touch",
			Label:    "Windows touch and pen",
			Kinds:    []string{"mouse", "keyboard", "touch", "pen"},
			Requires: []string{"Windows 8+", "interactive desktop"},
			Reason:   "Mouse and keyboard use Win32 input; touch and pen use Windows Touch Injection.",
		},
		win32,
	}
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

func (w windowsDesktopInput) Close() error {
	if w.touch != nil {
		return w.touch.close()
	}
	return nil
}

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
			procKeybdEvent.Call(uintptr(vk), 0, windowsKeyFlags(event, false), 0)
		}
	case "key_up":
		if vk := windowsVK(event); vk != 0 {
			procKeybdEvent.Call(uintptr(vk), 0, windowsKeyFlags(event, true), 0)
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
	processTouch.mu.Lock()
	defer processTouch.mu.Unlock()
	if !processTouch.initialized {
		r1, _, err := procInitializeTouchInjection.Call(maxTouchContacts, touchFeedbackDefault)
		if r1 == 0 {
			return nil, fmt.Errorf("InitializeTouchInjection failed: %w", err)
		}
		processTouch.initialized = true
	}
	return &windowsTouchInput{injector: &processTouch}, nil
}

func injectWindowsTouchFrame(frame []pointerTouchInfo) error {
	r1, _, err := procInjectTouchInput.Call(uintptr(len(frame)), uintptr(unsafe.Pointer(&frame[0])))
	if r1 == 0 {
		return err
	}
	return nil
}

func (t *windowsTouchInput) Apply(event desktopInputEvent) error {
	in := t.injector
	in.mu.Lock()
	defer in.mu.Unlock()
	if t.denied {
		return nil
	}
	slot, free := -1, -1
	for i := range in.active {
		c := &in.active[i]
		if c.owner == t && c.pointerID == event.PointerID {
			slot = i
			break
		}
		if c.owner == nil && free < 0 {
			free = i
		}
	}
	const update = pointerFlagUpdate | pointerFlagInRange | pointerFlagInContact
	flags := uint32(update)
	switch event.Type {
	case "mouse_down":
		if slot >= 0 || free < 0 {
			return nil
		}
		slot = free
		flags = pointerFlagDown | pointerFlagInRange | pointerFlagInContact
	case "mouse_move", "mouse_up":
		if slot < 0 {
			return nil // Hover or a contact Windows already cancelled.
		}
	default:
		return nil
	}
	pos := winPoint{X: int32(event.X), Y: int32(event.Y)}
	pressure := uint32(min(1, max(0, event.Pressure)) * 1024)
	if pressure == 0 {
		pressure = 512
	}
	c := windowsTouchContact{owner: t, pointerID: event.PointerID, info: pointerTouchInfo{
		PointerInfo: pointerInfo{PointerType: 2, PointerID: uint32(slot + 1), PointerFlags: flags, PtPixelLocation: pos},
		TouchMask:   touchMaskPressure, Pressure: pressure,
	}}
	// UP must match the previous frame at this point. A final browser move
	// can be coalesced, so send an UPDATE first when the location changed.
	if event.Type == "mouse_up" {
		if in.active[slot].info.PointerInfo.PtPixelLocation != pos {
			if err := in.submit(slot, c); err != nil {
				return err
			}
		}
		c.info.PointerInfo.PointerFlags = pointerFlagUp // End contact, including hover.
		c.info.Pressure = 0
	}
	return in.submit(slot, c)
}

// Called with mu held. Unchanged contacts are UPDATEs in every frame,
// including those belonging to another viewer with the same browser pointer ID.
func (in *windowsTouchInjector) submit(slot int, changed windowsTouchContact) error {
	n := 0
	for i := range in.active {
		if i == slot {
			in.frame[n] = changed.info
		} else if in.active[i].owner != nil {
			in.frame[n] = in.active[i].info
			in.frame[n].PointerInfo.PointerFlags = pointerFlagUpdate | pointerFlagInRange | pointerFlagInContact
		} else {
			continue
		}
		n++
	}
	frame := in.frame[:n]
	err := in.inject(frame)
	if errors.Is(err, windows.ERROR_NOT_READY) {
		// Windows assigns timestamps in 0.1 ms intervals. Retry the identical frame.
		time.Sleep(time.Millisecond)
		err = in.inject(frame)
	}
	if err != nil {
		switch {
		case errors.Is(err, windows.ERROR_INVALID_PARAMETER):
			clear(in.active[:]) // Windows cancels active contacts on an invalid transition.
		case errors.Is(err, windows.ERROR_ACCESS_DENIED):
			changed.owner.denied = true
			in.forget(changed.owner)
			return fmt.Errorf("Windows denied touch injection on this desktop; touch disabled for this session: %w", err)
		}
		return fmt.Errorf("InjectTouchInput failed: %w", err)
	}
	if changed.info.PointerInfo.PointerFlags == pointerFlagUp {
		in.active[slot] = windowsTouchContact{}
	} else {
		in.active[slot] = changed
	}
	return nil
}

func (in *windowsTouchInjector) forget(owner *windowsTouchInput) {
	for i := range in.active {
		if in.active[i].owner == owner {
			in.active[i] = windowsTouchContact{}
		}
	}
}

func (t *windowsTouchInput) close() error {
	in := t.injector
	in.mu.Lock()
	defer in.mu.Unlock()
	defer in.forget(t)
	var first error
	for i, c := range in.active {
		if in.active[i].owner != t {
			continue
		}
		c.info.PointerInfo.PointerFlags = pointerFlagUp
		c.info.Pressure = 0
		if err := in.submit(i, c); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func setCursorPos(x, y int) {
	procSetCursorPos.Call(uintptr(int32(x)), uintptr(int32(y)))
	procMouseEvent.Call(mouseEventMove, 0, 0, 0, 0)
}

func mouseButton(button int, down bool) {
	var flag uintptr
	var data uintptr
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
	case 3:
		if down {
			flag = mouseEventXDown
		} else {
			flag = mouseEventXUp
		}
		data = 1
	case 4:
		if down {
			flag = mouseEventXDown
		} else {
			flag = mouseEventXUp
		}
		data = 2
	}
	if flag != 0 {
		procMouseEvent.Call(flag, 0, 0, data, 0)
	}
}

func windowsKeyFlags(event desktopInputEvent, keyUp bool) uintptr {
	var flags uintptr
	if keyUp {
		flags |= keyEventKeyUp
	}
	switch event.Code {
	case "ControlRight", "AltRight", "MetaLeft", "MetaRight",
		"Insert", "Delete", "Home", "End", "PageUp", "PageDown",
		"ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown",
		"NumpadDivide", "NumpadEnter":
		flags |= keyEventExtendedKey
	}
	return flags
}

func windowsVK(event desktopInputEvent) byte {
	code := event.Code
	if strings.HasPrefix(code, "Key") && len(code) == 4 && code[3] >= 'A' && code[3] <= 'Z' {
		return code[3]
	}
	if strings.HasPrefix(code, "Digit") && len(code) == 6 && code[5] >= '0' && code[5] <= '9' {
		return code[5]
	}
	switch code {
	case "Backspace":
		return 0x08
	case "Tab":
		return 0x09
	case "Enter", "NumpadEnter":
		return 0x0d
	case "ShiftLeft":
		return 0xa0
	case "ShiftRight":
		return 0xa1
	case "ControlLeft":
		return 0xa2
	case "ControlRight":
		return 0xa3
	case "AltLeft":
		return 0xa4
	case "AltRight":
		return 0xa5
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
	case "MetaLeft":
		return 0x5b
	case "MetaRight":
		return 0x5c
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

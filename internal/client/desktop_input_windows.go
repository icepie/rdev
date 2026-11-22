//go:build windows

package client

import (
	"errors"
	"fmt"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"rdev/internal/protocol"
)

const (
	mouseEventMove        = 0x0001
	mouseEventLeftDown    = 0x0002
	mouseEventLeftUp      = 0x0004
	mouseEventRightDown   = 0x0008
	mouseEventRightUp     = 0x0010
	mouseEventMiddleDown  = 0x0020
	mouseEventMiddleUp    = 0x0040
	mouseEventXDown       = 0x0080
	mouseEventXUp         = 0x0100
	mouseEventWheel       = 0x0800
	mouseEventHWheel      = 0x1000
	mouseEventVirtualDesk = 0x4000
	mouseEventAbsolute    = 0x8000
	keyEventExtendedKey   = 0x0001
	keyEventKeyUp         = 0x0002
	keyEventScanCode      = 0x0008
	maxTouchContacts      = 8

	inputMouse    = 0
	inputKeyboard = 1

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
	procSendInput                = user32.NewProc("SendInput")
	procInitializeTouchInjection = user32.NewProc("InitializeTouchInjection")
	procInjectTouchInput         = user32.NewProc("InjectTouchInput")
	processTouch                 = windowsTouchInjector{inject: injectWindowsTouchFrame}
)

// INPUT's union has the size and alignment of its largest member, MOUSEINPUT.
// The uintptr field gives both layouts native alignment on x86 and x64.
type windowsInput struct {
	kind  uint32
	mouse windowsMouseInput
}

type windowsMouseInput struct {
	dx, dy      int32
	data, flags uint32
	time        uint32
	extra       uintptr
}

type windowsKeyboardInput struct {
	vk, scan    uint16
	flags, time uint32
	extra       uintptr
}

func (in *windowsInput) keyboard() *windowsKeyboardInput {
	return (*windowsKeyboardInput)(unsafe.Pointer(&in.mouse))
}

func sendWindowsInputs(inputs []windowsInput) error {
	if len(inputs) == 0 {
		return nil
	}
	n, _, err := procSendInput.Call(uintptr(len(inputs)), uintptr(unsafe.Pointer(&inputs[0])), unsafe.Sizeof(inputs[0]))
	if n != uintptr(len(inputs)) {
		if n == 0 && err != nil && err != windows.ERROR_SUCCESS {
			return fmt.Errorf("SendInput: %w", err)
		}
		return fmt.Errorf("SendInput inserted %d of %d events", n, len(inputs))
	}
	return nil
}

type windowsDesktopInput struct {
	touch  *windowsTouchInput
	buffer [3]windowsInput // Apply is serialized by desktopSession.inputMu.
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
		return &windowsDesktopInput{}, nil
	case "win32-touch":
		touch, err := newWindowsTouchInput()
		if err != nil {
			return nil, err
		}
		return &windowsDesktopInput{touch: touch}, nil
	default:
		return nil, fmt.Errorf("unsupported desktop input backend %q", backend)
	}
}

func (w *windowsDesktopInput) Backend() string {
	if w.touch != nil {
		return "win32-touch"
	}
	return "win32"
}

func (w *windowsDesktopInput) Close() error {
	if w.touch != nil {
		return w.touch.close()
	}
	return nil
}

func (w *windowsDesktopInput) Apply(event desktopInputEvent) error {
	if (event.PointerType == "touch" || event.PointerType == "pen") && w.touch != nil {
		return w.touch.Apply(event)
	}
	switch event.Type {
	case "key_down", "key_up":
		key, ok := windowsKeyInput(event)
		if !ok {
			return nil
		}
		w.buffer[0] = windowsInput{kind: inputKeyboard}
		*w.buffer[0].keyboard() = key
		return sendWindowsInputs(w.buffer[:1])
	case "mouse_move", "mouse_down", "mouse_up", "wheel":
		desktop := virtualScreenSource()
		if desktop.Width <= 0 || desktop.Height <= 0 {
			return fmt.Errorf("invalid virtual desktop size %dx%d", desktop.Width, desktop.Height)
		}
		n := windowsMouseInputs(&w.buffer, event, desktop)
		return sendWindowsInputs(w.buffer[:n])
	}
	return nil
}

// Position first, then click or scroll in the same SendInput batch: wheel-only
// records (and button records without MOVE) do not position the cursor.
func windowsMouseInputs(buffer *[3]windowsInput, event desktopInputEvent, desktop protocol.DesktopSource) int {
	buffer[0] = windowsInput{kind: inputMouse, mouse: windowsMouseInput{
		dx:    windowsAbsoluteCoord(event.X, desktop.X, desktop.Width),
		dy:    windowsAbsoluteCoord(event.Y, desktop.Y, desktop.Height),
		flags: mouseEventMove | mouseEventAbsolute | mouseEventVirtualDesk,
	}}
	n := 1
	switch event.Type {
	case "mouse_move":
	case "mouse_down", "mouse_up":
		buttons := [...]struct{ down, up, data uint32 }{
			{mouseEventLeftDown, mouseEventLeftUp, 0}, {mouseEventMiddleDown, mouseEventMiddleUp, 0},
			{mouseEventRightDown, mouseEventRightUp, 0}, {mouseEventXDown, mouseEventXUp, 1}, {mouseEventXDown, mouseEventXUp, 2},
		}
		if event.Button < 0 || event.Button >= len(buttons) {
			return 0
		}
		button := buttons[event.Button]
		flags := button.down
		if event.Type == "mouse_up" {
			flags = button.up
		}
		buffer[n] = windowsInput{kind: inputMouse, mouse: windowsMouseInput{flags: flags, data: button.data}}
		n++
	case "wheel":
		// Keep high-resolution wheel deltas rather than rounding every event to
		// an entire notch. WM_MOUSEWHEEL carries the signed value in a SHORT.
		if event.DeltaY != 0 {
			delta := -max(-32767, min(32767, event.DeltaY))
			buffer[n] = windowsInput{kind: inputMouse, mouse: windowsMouseInput{flags: mouseEventWheel, data: uint32(delta)}}
			n++
		}
		if event.DeltaX != 0 {
			delta := max(-32767, min(32767, event.DeltaX))
			buffer[n] = windowsInput{kind: inputMouse, mouse: windowsMouseInput{flags: mouseEventHWheel, data: uint32(delta)}}
			n++
		}
		if n == 1 {
			return 0
		}
	default:
		return 0
	}
	return n
}

// Aim at the center of the pixel's bin in Windows' 16-bit coordinate space.
func windowsAbsoluteCoord(value, origin, size int) int32 {
	if value <= origin || size <= 1 {
		return 0
	}
	if int64(value) >= int64(origin)+int64(size)-1 {
		return 65535
	}
	return int32(((int64(value)-int64(origin))*65536 + 32768) / int64(size))
}

func windowsKeyInput(event desktopInputEvent) (windowsKeyboardInput, bool) {
	var key windowsKeyboardInput
	if event.Type == "key_up" {
		key.flags = keyEventKeyUp
	}
	if scan := windowsPhysicalScanCodes[event.Code]; scan != 0 {
		key.scan = scan & 0xff
		key.flags |= keyEventScanCode
		if scan>>8 == 0xe0 {
			key.flags |= keyEventExtendedKey
		}
	} else if event.Code == "Pause" {
		// Pause's E1 sequence cannot be expressed by KEYEVENTF_EXTENDEDKEY.
		key.vk = 0x13
	} else if len(event.Key) == 1 {
		// Preserve logical alphanumeric shortcuts from clients without a physical code.
		ch := event.Key[0]
		if ch >= 'a' && ch <= 'z' {
			ch -= 'a' - 'A'
		}
		if (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') {
			key.vk = uint16(ch)
		}
	}
	return key, key.scan != 0 || key.vk != 0
}

// DOM KeyboardEvent.code to Windows Scan 1 make code. The active Windows
// layout, not the viewer's translated event.Key, selects the character.
var windowsPhysicalScanCodes = map[string]uint16{
	"Escape": 0x01, "Digit1": 0x02, "Digit2": 0x03, "Digit3": 0x04, "Digit4": 0x05,
	"Digit5": 0x06, "Digit6": 0x07, "Digit7": 0x08, "Digit8": 0x09, "Digit9": 0x0a,
	"Digit0": 0x0b, "Minus": 0x0c, "Equal": 0x0d, "Backspace": 0x0e, "Tab": 0x0f,
	"KeyQ": 0x10, "KeyW": 0x11, "KeyE": 0x12, "KeyR": 0x13, "KeyT": 0x14, "KeyY": 0x15,
	"KeyU": 0x16, "KeyI": 0x17, "KeyO": 0x18, "KeyP": 0x19, "BracketLeft": 0x1a, "BracketRight": 0x1b,
	"Enter": 0x1c, "ControlLeft": 0x1d, "KeyA": 0x1e, "KeyS": 0x1f, "KeyD": 0x20, "KeyF": 0x21,
	"KeyG": 0x22, "KeyH": 0x23, "KeyJ": 0x24, "KeyK": 0x25, "KeyL": 0x26, "Semicolon": 0x27,
	"Quote": 0x28, "Backquote": 0x29, "ShiftLeft": 0x2a, "Backslash": 0x2b, "KeyZ": 0x2c, "KeyX": 0x2d,
	"KeyC": 0x2e, "KeyV": 0x2f, "KeyB": 0x30, "KeyN": 0x31, "KeyM": 0x32, "Comma": 0x33, "Period": 0x34,
	"Slash": 0x35, "ShiftRight": 0x36, "NumpadMultiply": 0x37, "AltLeft": 0x38, "Space": 0x39, "CapsLock": 0x3a,
	"F1": 0x3b, "F2": 0x3c, "F3": 0x3d, "F4": 0x3e, "F5": 0x3f, "F6": 0x40,
	"F7": 0x41, "F8": 0x42, "F9": 0x43, "F10": 0x44, "ScrollLock": 0x46,
	"Numpad7": 0x47, "Numpad8": 0x48, "Numpad9": 0x49, "NumpadSubtract": 0x4a, "Numpad4": 0x4b,
	"Numpad5": 0x4c, "Numpad6": 0x4d, "NumpadAdd": 0x4e, "Numpad1": 0x4f, "Numpad2": 0x50,
	"Numpad3": 0x51, "Numpad0": 0x52, "NumpadDecimal": 0x53, "IntlBackslash": 0x56, "F11": 0x57, "F12": 0x58,
	"IntlRo": 0x73, "IntlYen": 0x7d, "NumpadEnter": 0xe01c, "ControlRight": 0xe01d, "NumpadDivide": 0xe035,
	"PrintScreen": 0xe037, "AltRight": 0xe038, "NumLock": 0xe045, "Home": 0xe047, "ArrowUp": 0xe048,
	"PageUp": 0xe049, "ArrowLeft": 0xe04b, "ArrowRight": 0xe04d, "End": 0xe04f, "ArrowDown": 0xe050,
	"PageDown": 0xe051, "Insert": 0xe052, "Delete": 0xe053, "MetaLeft": 0xe05b, "MetaRight": 0xe05c, "ContextMenu": 0xe05d,
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

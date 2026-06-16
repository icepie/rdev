//go:build linux

package client

import (
	"encoding/binary"
	"fmt"
	"image"
	"log"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/BurntSushi/xgb"
	"github.com/BurntSushi/xgb/xproto"
	"github.com/BurntSushi/xgb/xtest"
	"rdev/internal/protocol"
)

const (
	evSyn = 0x00
	evKey = 0x01
	evRel = 0x02
	evAbs = 0x03

	synReport = 0

	relX      = 0x00
	relY      = 0x01
	relHWheel = 0x06
	relWheel  = 0x08

	absX        = 0x00
	absY        = 0x01
	absPressure = 0x18

	btnLeft       = 0x110
	btnRight      = 0x111
	btnMiddle     = 0x112
	btnToolPen    = 0x140
	btnToolFinger = 0x145
	btnTouch      = 0x14a

	busUSB = 0x03

	uinputReadyTimeout = 2 * time.Second
	uinputCreateDelay  = 150 * time.Millisecond
)

const (
	uinputIOCTLBase = uintptr('U')
	uiDevCreate     = uintptr(0x5501)
	uiDevDestroy    = uintptr(0x5502)
)

type x11DesktopInput struct {
	conn   *xgb.Conn
	screen *xproto.ScreenInfo
}

type uinputDesktopInput struct {
	keyboard *uinputDevice
	mouse    *uinputDevice
	touch    *uinputDevice
	pen      *uinputDevice
	bounds   image.Rectangle
}

type uinputDevice struct {
	file  *os.File
	lastX int
	lastY int
	hasPt bool
}

type uinputAbsConfig struct {
	Code       int
	Minimum    int32
	Maximum    int32
	Resolution int32
}

type inputID struct {
	BusType uint16
	Vendor  uint16
	Product uint16
	Version uint16
}

type uinputSetup struct {
	ID           inputID
	Name         [80]byte
	FFEffectsMax uint32
}

type inputAbsInfo struct {
	Value      int32
	Minimum    int32
	Maximum    int32
	Fuzz       int32
	Flat       int32
	Resolution int32
}

type uinputAbsSetup struct {
	Code    uint16
	Pad     [3]uint16
	AbsInfo inputAbsInfo
}

type inputEvent struct {
	Time  syscall.Timeval
	Type  uint16
	Code  uint16
	Value int32
}

func platformDesktopInputOptions() []protocol.DesktopInputBackend {
	var options []protocol.DesktopInputBackend
	if x11InputAvailable() {
		options = append(options, protocol.DesktopInputBackend{ID: "x11-xtest", Label: "X11 XTEST", Kinds: []string{"mouse", "keyboard"}})
	}
	if uinputAvailable() {
		options = append(options, protocol.DesktopInputBackend{ID: "uinput", Label: "Linux uinput", Kinds: []string{"mouse", "keyboard", "touch", "pen"}})
	}
	return options
}

func newDesktopInput(backend string) (desktopInput, error) {
	switch backend {
	case "", "auto", "x11-xtest":
		return newX11DesktopInput()
	case "uinput":
		return newUInputDesktopInput()
	default:
		return nil, fmt.Errorf("unsupported desktop input backend %q", backend)
	}
}

func x11InputAvailable() bool {
	conn, err := xgb.NewConn()
	if err != nil {
		return false
	}
	defer conn.Close()
	return xtest.Init(conn) == nil
}

func newX11DesktopInput() (desktopInput, error) {
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

func (x *x11DesktopInput) Backend() string { return "x11-xtest" }

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

func uinputAvailable() bool {
	file, err := prepareUInputFile()
	if err != nil {
		return false
	}
	file.Close()
	return true
}

func prepareUInputFile() (*os.File, error) {
	file, err := openUInputFile()
	if err == nil {
		return file, nil
	}
	if os.Geteuid() != 0 {
		return nil, err
	}
	loadUInputModule()
	deadline := time.Now().Add(uinputReadyTimeout)
	for {
		file, err = openUInputFile()
		if err == nil {
			return file, nil
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func openUInputFile() (*os.File, error) {
	paths := []string{"/dev/uinput", "/dev/input/uinput"}
	var errors []string
	for _, path := range paths {
		file, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			return file, nil
		}
		errors = append(errors, fmt.Sprintf("%s: %v", path, err))
	}
	return nil, fmt.Errorf("open uinput: %s", strings.Join(errors, "; "))
}

func loadUInputModule() {
	candidates := []string{"modprobe", "/sbin/modprobe", "/usr/sbin/modprobe"}
	for _, command := range candidates {
		if strings.Contains(command, "/") {
			if _, err := os.Stat(command); err != nil {
				continue
			}
		} else if _, err := exec.LookPath(command); err != nil {
			continue
		}
		if err := exec.Command(command, "uinput").Run(); err == nil {
			return
		}
	}
}

func newUInputDesktopInput() (desktopInput, error) {
	input := &uinputDesktopInput{}
	var errors []string
	var err error
	input.keyboard, err = newUInputDevice("RDev keyboard", uinputKeyboardKeys(), nil, nil)
	if err != nil {
		errors = append(errors, err.Error())
	}
	input.mouse, err = newUInputDevice("RDev mouse", []int{btnLeft, btnRight, btnMiddle}, []int{relX, relY, relWheel, relHWheel}, nil)
	if err != nil {
		errors = append(errors, err.Error())
	}
	input.touch, err = newUInputDevice("RDev touch", []int{btnTouch, btnToolFinger}, nil, []uinputAbsConfig{{Code: absX, Maximum: 65535, Resolution: 10}, {Code: absY, Maximum: 65535, Resolution: 10}})
	if err != nil {
		errors = append(errors, err.Error())
	}
	input.pen, err = newUInputDevice("RDev pen", []int{btnTouch, btnToolPen}, nil, []uinputAbsConfig{{Code: absX, Maximum: 65535, Resolution: 10}, {Code: absY, Maximum: 65535, Resolution: 10}, {Code: absPressure, Maximum: 1024}})
	if err != nil {
		errors = append(errors, err.Error())
	}
	if input.keyboard == nil && input.mouse == nil && input.touch == nil && input.pen == nil {
		input.Close()
		return nil, fmt.Errorf("initialize uinput devices: %s", strings.Join(errors, "; "))
	}
	if len(errors) > 0 {
		log.Printf("uinput initialized with partial device support: %s", strings.Join(errors, "; "))
	}
	time.Sleep(uinputCreateDelay)
	return input, nil
}

func (u *uinputDesktopInput) Backend() string { return "uinput" }

func (u *uinputDesktopInput) SetBounds(bounds image.Rectangle) { u.bounds = bounds }

func (u *uinputDesktopInput) Close() error {
	var first error
	for _, device := range []*uinputDevice{u.keyboard, u.mouse, u.touch, u.pen} {
		if device == nil {
			continue
		}
		if err := device.Close(); err != nil && first == nil {
			first = err
		}
	}
	u.keyboard, u.mouse, u.touch, u.pen = nil, nil, nil, nil
	return first
}

func (u *uinputDesktopInput) Apply(event desktopInputEvent) error {
	switch event.Type {
	case "key_down", "key_up":
		if u.keyboard == nil {
			return nil
		}
		if key := uinputKeycode(event); key != 0 {
			value := int32(1)
			if event.Type == "key_up" {
				value = 0
			}
			return u.keyboard.key(key, value)
		}
		return nil
	case "mouse_move", "mouse_down", "mouse_up":
		if event.PointerType == "touch" && u.touch != nil {
			return u.applyAbsolutePointer(u.touch, event, btnToolFinger, false)
		}
		if event.PointerType == "pen" && u.pen != nil {
			return u.applyAbsolutePointer(u.pen, event, btnToolPen, true)
		}
		if u.mouse == nil {
			return nil
		}
		return u.applyMouse(event)
	case "wheel":
		if u.mouse == nil {
			return nil
		}
		return u.applyWheel(event)
	}
	return nil
}

func (u *uinputDesktopInput) applyMouse(event desktopInputEvent) error {
	switch event.Type {
	case "mouse_move":
		return u.mouse.moveTo(event.X, event.Y)
	case "mouse_down":
		if err := u.mouse.moveTo(event.X, event.Y); err != nil {
			return err
		}
		return u.mouse.key(uinputButton(event.Button), 1)
	case "mouse_up":
		if err := u.mouse.moveTo(event.X, event.Y); err != nil {
			return err
		}
		return u.mouse.key(uinputButton(event.Button), 0)
	}
	return nil
}

func (u *uinputDesktopInput) applyWheel(event desktopInputEvent) error {
	if event.DeltaY != 0 {
		if err := u.mouse.emit(evRel, relWheel, int32(wheelStep(-event.DeltaY))); err != nil {
			return err
		}
	}
	if event.DeltaX != 0 {
		if err := u.mouse.emit(evRel, relHWheel, int32(wheelStep(event.DeltaX))); err != nil {
			return err
		}
	}
	return u.mouse.sync()
}

func (u *uinputDesktopInput) applyAbsolutePointer(device *uinputDevice, event desktopInputEvent, tool int, pressure bool) error {
	if err := device.emit(evAbs, absX, int32(u.scaleAbsX(event.X))); err != nil {
		return err
	}
	if err := device.emit(evAbs, absY, int32(u.scaleAbsY(event.Y))); err != nil {
		return err
	}
	if pressure {
		value := int32(event.Pressure * 1024)
		if value < 0 {
			value = 0
		}
		if value > 1024 {
			value = 1024
		}
		if event.Type == "mouse_down" && value == 0 {
			value = 512
		}
		if event.Type == "mouse_up" {
			value = 0
		}
		if err := device.emit(evAbs, absPressure, value); err != nil {
			return err
		}
	}
	switch event.Type {
	case "mouse_down":
		if err := device.emit(evKey, tool, 1); err != nil {
			return err
		}
		if err := device.emit(evKey, btnTouch, 1); err != nil {
			return err
		}
	case "mouse_up":
		if err := device.emit(evKey, btnTouch, 0); err != nil {
			return err
		}
		if err := device.emit(evKey, tool, 0); err != nil {
			return err
		}
	}
	return device.sync()
}

func (u *uinputDesktopInput) scaleAbsX(value int) int {
	return scaleAbsCoord(value, u.bounds.Min.X, u.bounds.Max.X)
}

func (u *uinputDesktopInput) scaleAbsY(value int) int {
	return scaleAbsCoord(value, u.bounds.Min.Y, u.bounds.Max.Y)
}

func scaleAbsCoord(value, minValue, maxValue int) int {
	if maxValue <= minValue {
		return scaleAbs(value)
	}
	if value < minValue {
		value = minValue
	}
	if value >= maxValue {
		value = maxValue - 1
	}
	return (value - minValue) * 65535 / (maxValue - minValue)
}

func scaleAbs(value int) int {
	if value < 0 {
		return 0
	}
	if value > 65535 {
		return 65535
	}
	return value
}

func wheelStep(delta int) int {
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

func newUInputDevice(name string, keyBits, relBits []int, absAxes []uinputAbsConfig) (*uinputDevice, error) {
	file, err := prepareUInputFile()
	if err != nil {
		return nil, fmt.Errorf("open uinput for %s: %w", name, err)
	}
	device := &uinputDevice{file: file}
	if err := device.setup(name, keyBits, relBits, absAxes); err != nil {
		device.Close()
		return nil, err
	}
	return device, nil
}

func (d *uinputDevice) setup(name string, keyBits, relBits []int, absAxes []uinputAbsConfig) error {
	if len(keyBits) > 0 {
		if err := d.ioctl(uiSetEVBit, evKey); err != nil {
			return fmt.Errorf("uinput set EV_KEY for %s: %w", name, err)
		}
		for _, key := range keyBits {
			if err := d.ioctl(uiSetKeyBit, key); err != nil {
				return fmt.Errorf("uinput set keybit %d for %s: %w", key, name, err)
			}
		}
	}
	if len(relBits) > 0 {
		if err := d.ioctl(uiSetEVBit, evRel); err != nil {
			return fmt.Errorf("uinput set EV_REL for %s: %w", name, err)
		}
		for _, rel := range relBits {
			if err := d.ioctl(uiSetRelBit, rel); err != nil {
				return fmt.Errorf("uinput set relbit %d for %s: %w", rel, name, err)
			}
		}
	}
	if len(absAxes) > 0 {
		if err := d.ioctl(uiSetEVBit, evAbs); err != nil {
			return fmt.Errorf("uinput set EV_ABS for %s: %w", name, err)
		}
		for _, axis := range absAxes {
			if err := d.ioctl(uiSetAbsBit, axis.Code); err != nil {
				return fmt.Errorf("uinput set absbit %d for %s: %w", axis.Code, name, err)
			}
			if err := d.setupAbs(axis.Code, axis.Minimum, axis.Maximum, axis.Resolution); err != nil {
				return err
			}
		}
	}
	setup := uinputSetup{ID: inputID{BusType: busUSB, Vendor: 0x5244, Product: 0x4556, Version: 1}}
	copy(setup.Name[:], []byte(name))
	if err := d.ioctlPtr(uiDevSetup, unsafe.Pointer(&setup)); err != nil {
		return fmt.Errorf("uinput setup device %s: %w", name, err)
	}
	if err := d.ioctl(uiDevCreate, 0); err != nil {
		return fmt.Errorf("uinput create device %s: %w", name, err)
	}
	time.Sleep(uinputCreateDelay)
	return nil
}

func (d *uinputDevice) Close() error {
	if d.file == nil {
		return nil
	}
	_ = d.ioctl(uiDevDestroy, 0)
	err := d.file.Close()
	d.file = nil
	return err
}

func (d *uinputDevice) moveTo(x, y int) error {
	if !d.hasPt {
		d.lastX, d.lastY, d.hasPt = x, y, true
		return nil
	}
	dx := x - d.lastX
	dy := y - d.lastY
	d.lastX, d.lastY = x, y
	if dx == 0 && dy == 0 {
		return nil
	}
	if err := d.emit(evRel, relX, int32(dx)); err != nil {
		return err
	}
	if err := d.emit(evRel, relY, int32(dy)); err != nil {
		return err
	}
	return d.sync()
}

func (d *uinputDevice) key(code int, value int32) error {
	if code == 0 {
		return nil
	}
	if err := d.emit(evKey, code, value); err != nil {
		return err
	}
	return d.sync()
}

func (d *uinputDevice) sync() error { return d.emit(evSyn, synReport, 0) }

func (d *uinputDevice) emit(typ, code int, value int32) error {
	event := inputEvent{Type: uint16(typ), Code: uint16(code), Value: value}
	return binary.Write(d.file, binary.LittleEndian, event)
}

func (d *uinputDevice) setupAbs(code int, minValue, maxValue, resolution int32) error {
	setup := uinputAbsSetup{Code: uint16(code), AbsInfo: inputAbsInfo{Minimum: minValue, Maximum: maxValue, Resolution: resolution}}
	if err := d.ioctlPtr(uiAbsSetup, unsafe.Pointer(&setup)); err != nil {
		return fmt.Errorf("uinput setup abs %d: %w", code, err)
	}
	return nil
}

func (d *uinputDevice) ioctl(req uintptr, value int) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, d.file.Fd(), req, uintptr(value))
	if errno != 0 {
		return errno
	}
	return nil
}

func (d *uinputDevice) ioctlPtr(req uintptr, ptr unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, d.file.Fd(), req, uintptr(ptr))
	if errno != 0 {
		return errno
	}
	return nil
}

func uiIOW(nr uintptr, size uintptr) uintptr {
	return (1 << 30) | (size << 16) | (uinputIOCTLBase << 8) | nr
}

var (
	uiDevSetup  = uiIOW(3, unsafe.Sizeof(uinputSetup{}))
	uiAbsSetup  = uiIOW(4, unsafe.Sizeof(uinputAbsSetup{}))
	uiSetEVBit  = uiIOW(100, unsafe.Sizeof(int32(0)))
	uiSetKeyBit = uiIOW(101, unsafe.Sizeof(int32(0)))
	uiSetRelBit = uiIOW(102, unsafe.Sizeof(int32(0)))
	uiSetAbsBit = uiIOW(103, unsafe.Sizeof(int32(0)))
)

func uinputButton(button int) int {
	switch button {
	case 1:
		return btnMiddle
	case 2:
		return btnRight
	default:
		return btnLeft
	}
}

func uinputKeyboardKeys() []int {
	keys := []int{}
	for _, key := range []int{1, 14, 15, 28, 29, 42, 54, 56, 57, 58, 87, 88, 97, 100, 102, 103, 104, 105, 106, 107, 108, 109, 110, 111, 125, 126} {
		keys = append(keys, key)
	}
	for key := 2; key <= 13; key++ {
		keys = append(keys, key)
	}
	for key := 16; key <= 53; key++ {
		keys = append(keys, key)
	}
	for key := 59; key <= 68; key++ {
		keys = append(keys, key)
	}
	for key := 71; key <= 83; key++ {
		keys = append(keys, key)
	}
	for key := 86; key <= 88; key++ {
		keys = append(keys, key)
	}
	return keys
}

func uinputKeycode(event desktopInputEvent) int {
	code := event.Code
	if strings.HasPrefix(code, "Key") && len(code) == 4 {
		return map[byte]int{
			'A': 30, 'B': 48, 'C': 46, 'D': 32, 'E': 18, 'F': 33, 'G': 34, 'H': 35, 'I': 23, 'J': 36, 'K': 37, 'L': 38, 'M': 50,
			'N': 49, 'O': 24, 'P': 25, 'Q': 16, 'R': 19, 'S': 31, 'T': 20, 'U': 22, 'V': 47, 'W': 17, 'X': 45, 'Y': 21, 'Z': 44,
		}[code[3]]
	}
	if strings.HasPrefix(code, "Digit") && len(code) == 6 {
		return map[byte]int{'1': 2, '2': 3, '3': 4, '4': 5, '5': 6, '6': 7, '7': 8, '8': 9, '9': 10, '0': 11}[code[5]]
	}
	switch code {
	case "Escape":
		return 1
	case "Backspace":
		return 14
	case "Tab":
		return 15
	case "Enter", "NumpadEnter":
		return 28
	case "ControlLeft":
		return 29
	case "ShiftLeft":
		return 42
	case "ShiftRight":
		return 54
	case "AltLeft":
		return 56
	case "Space":
		return 57
	case "CapsLock":
		return 58
	case "F1":
		return 59
	case "F2":
		return 60
	case "F3":
		return 61
	case "F4":
		return 62
	case "F5":
		return 63
	case "F6":
		return 64
	case "F7":
		return 65
	case "F8":
		return 66
	case "F9":
		return 67
	case "F10":
		return 68
	case "F11":
		return 87
	case "F12":
		return 88
	case "Home":
		return 102
	case "ArrowUp":
		return 103
	case "PageUp":
		return 104
	case "ArrowLeft":
		return 105
	case "ArrowRight":
		return 106
	case "End":
		return 107
	case "ArrowDown":
		return 108
	case "PageDown":
		return 109
	case "Insert":
		return 110
	case "Delete":
		return 111
	case "MetaLeft":
		return 125
	case "MetaRight":
		return 126
	case "ControlRight":
		return 97
	case "AltRight":
		return 100
	case "Minus":
		return 12
	case "Equal":
		return 13
	case "BracketLeft":
		return 26
	case "BracketRight":
		return 27
	case "Backslash":
		return 43
	case "Semicolon":
		return 39
	case "Quote":
		return 40
	case "Backquote":
		return 41
	case "Comma":
		return 51
	case "Period":
		return 52
	case "Slash":
		return 53
	case "Numpad0":
		return 82
	case "Numpad1":
		return 79
	case "Numpad2":
		return 80
	case "Numpad3":
		return 81
	case "Numpad4":
		return 75
	case "Numpad5":
		return 76
	case "Numpad6":
		return 77
	case "Numpad7":
		return 71
	case "Numpad8":
		return 72
	case "Numpad9":
		return 73
	case "NumpadAdd":
		return 78
	case "NumpadSubtract":
		return 74
	case "NumpadMultiply":
		return 55
	case "NumpadDivide":
		return 98
	case "NumpadDecimal":
		return 83
	}
	return 0
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

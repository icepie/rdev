//go:build windows

package client

import (
	"fmt"
	"image"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"rdev/internal/protocol"
)

const (
	smCXScreen        = 0
	smCYScreen        = 1
	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79

	desktopReadObjects  = 0x0001
	desktopWriteObjects = 0x0080

	monitorInfoPrimary = 0x00000001

	srcCopy    = 0x00CC0020
	captureBlt = 0x40000000

	biRGB        = 0
	dibRGBColors = 0
)

var (
	user32 = syscall.NewLazyDLL("user32.dll")
	gdi32  = syscall.NewLazyDLL("gdi32.dll")

	procGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	procOpenInputDesktop    = user32.NewProc("OpenInputDesktop")
	procSetThreadDesktop    = user32.NewProc("SetThreadDesktop")
	procCloseDesktop        = user32.NewProc("CloseDesktop")
	procGetDC               = user32.NewProc("GetDC")
	procGetCursorPos        = user32.NewProc("GetCursorPos")
	procReleaseDC           = user32.NewProc("ReleaseDC")
	procEnumDisplayMonitors = user32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfoW     = user32.NewProc("GetMonitorInfoW")
	procEnumWindows         = user32.NewProc("EnumWindows")
	procIsWindowVisible     = user32.NewProc("IsWindowVisible")
	procGetWindowTextLength = user32.NewProc("GetWindowTextLengthW")
	procGetWindowText       = user32.NewProc("GetWindowTextW")
	procGetWindowRect       = user32.NewProc("GetWindowRect")
	procCreateCompatibleDC  = gdi32.NewProc("CreateCompatibleDC")
	procCreateDIBSection    = gdi32.NewProc("CreateDIBSection")
	procSelectObject        = gdi32.NewProc("SelectObject")
	procDeleteObject        = gdi32.NewProc("DeleteObject")
	procDeleteDC            = gdi32.NewProc("DeleteDC")
	procBitBlt              = gdi32.NewProc("BitBlt")
)

type winRect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

type monitorInfoEx struct {
	Size    uint32
	Monitor winRect
	Work    winRect
	Flags   uint32
	Device  [32]uint16
}

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [1]uint32
}

type windowsCaptureSource struct {
	source protocol.DesktopSource
	bounds image.Rectangle
}

type gdiDesktopCapturer struct {
	desktop  uintptr
	screenDC uintptr
	memDC    uintptr
	bitmap   uintptr
	oldObj   uintptr
	bits     unsafe.Pointer
	stride   int
	bounds   image.Rectangle
	source   protocol.DesktopSource
}

func desktopSources() []protocol.DesktopSource {
	virtual := virtualScreenSource()
	sources := []protocol.DesktopSource{
		{ID: "auto", Label: "Auto", Kind: "screen", Backend: "win32-gdi", X: virtual.X, Y: virtual.Y, Width: virtual.Width, Height: virtual.Height, Primary: true},
		virtual,
	}
	for _, monitor := range enumerateWindowsMonitors() {
		sources = append(sources, monitor.source)
	}
	sources = append(sources, enumerateDXGISources()...)
	for _, window := range enumerateWindowsWindows(80) {
		sources = append(sources, window.source)
	}
	return sources
}

func newDesktopCapturer(source string) (desktopCapturer, error) {
	if strings.HasPrefix(source, "dxgi:") {
		return newDXGICapturer(source)
	}

	desktop, _, _ := procOpenInputDesktop.Call(0, 0, desktopReadObjects|desktopWriteObjects)
	if desktop != 0 {
		procSetThreadDesktop.Call(desktop)
	}

	selected, err := resolveWindowsCaptureSource(source)
	if err != nil {
		if desktop != 0 {
			procCloseDesktop.Call(desktop)
		}
		return nil, err
	}
	width := selected.bounds.Dx()
	height := selected.bounds.Dy()
	if width <= 0 || height <= 0 {
		if desktop != 0 {
			procCloseDesktop.Call(desktop)
		}
		return nil, fmt.Errorf("invalid capture source size %dx%d", width, height)
	}

	screenDC, _, err := procGetDC.Call(0)
	if screenDC == 0 {
		if desktop != 0 {
			procCloseDesktop.Call(desktop)
		}
		return nil, fmt.Errorf("GetDC failed: %s", windowsCallError(err))
	}
	capturer := &gdiDesktopCapturer{desktop: desktop, screenDC: screenDC, bounds: selected.bounds, source: selected.source}

	memDC, _, err := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		capturer.Close()
		return nil, fmt.Errorf("CreateCompatibleDC failed: %s", windowsCallError(err))
	}
	capturer.memDC = memDC

	bmi := bitmapInfo{}
	bmi.Header.Size = uint32(unsafe.Sizeof(bmi.Header))
	bmi.Header.Width = int32(width)
	bmi.Header.Height = -int32(height)
	bmi.Header.Planes = 1
	bmi.Header.BitCount = 32
	bmi.Header.Compression = biRGB
	bmi.Header.SizeImage = uint32(width * height * 4)

	var bits unsafe.Pointer
	bitmap, _, err := procCreateDIBSection.Call(
		screenDC,
		uintptr(unsafe.Pointer(&bmi)),
		dibRGBColors,
		uintptr(unsafe.Pointer(&bits)),
		0,
		0,
	)
	if bitmap == 0 || bits == nil {
		capturer.Close()
		return nil, fmt.Errorf("CreateDIBSection failed: %s", windowsCallError(err))
	}
	capturer.bitmap = bitmap
	capturer.bits = bits
	capturer.stride = width * 4

	oldObj, _, err := procSelectObject.Call(memDC, bitmap)
	if oldObj == 0 {
		capturer.Close()
		return nil, fmt.Errorf("SelectObject failed: %s", windowsCallError(err))
	}
	capturer.oldObj = oldObj
	if err := capturer.probe(); err != nil {
		capturer.Close()
		return nil, err
	}
	return capturer, nil
}

func resolveWindowsCaptureSource(source string) (windowsCaptureSource, error) {
	switch {
	case source == "", source == "auto", source == "virtual", source == "screen:virtual", source == "screen:all":
		return sourceFromProtocol(virtualScreenSource()), nil
	case source == "primary", source == "screen:primary":
		return sourceFromProtocol(primaryScreenSource()), nil
	case strings.HasPrefix(source, "monitor:"):
		for _, monitor := range enumerateWindowsMonitors() {
			if monitor.source.ID == source {
				return monitor, nil
			}
		}
		return windowsCaptureSource{}, fmt.Errorf("monitor source %q not found", source)
	case strings.HasPrefix(source, "window:"):
		hwnd, err := parseWindowHandle(source)
		if err != nil {
			return windowsCaptureSource{}, err
		}
		window, err := windowsWindowSource(hwnd)
		if err != nil {
			return windowsCaptureSource{}, err
		}
		return window, nil
	default:
		return windowsCaptureSource{}, fmt.Errorf("unsupported desktop source %q", source)
	}
}

func sourceFromProtocol(source protocol.DesktopSource) windowsCaptureSource {
	return windowsCaptureSource{source: source, bounds: image.Rect(source.X, source.Y, source.X+source.Width, source.Y+source.Height)}
}

func virtualScreenSource() protocol.DesktopSource {
	x := getSystemMetric(smXVirtualScreen)
	y := getSystemMetric(smYVirtualScreen)
	width := getSystemMetric(smCXVirtualScreen)
	height := getSystemMetric(smCYVirtualScreen)
	return protocol.DesktopSource{ID: "screen:all", Label: "All screens", Kind: "screen", Backend: "win32-gdi", X: x, Y: y, Width: width, Height: height, Primary: true}
}

func primaryScreenSource() protocol.DesktopSource {
	return protocol.DesktopSource{ID: "screen:primary", Label: "Primary screen", Kind: "screen", Backend: "win32-gdi", Width: getSystemMetric(smCXScreen), Height: getSystemMetric(smCYScreen), Primary: true}
}

func enumerateWindowsMonitors() []windowsCaptureSource {
	var monitors []windowsCaptureSource
	callback := syscall.NewCallback(func(hMonitor, hdcMonitor, rectPtr, data uintptr) uintptr {
		var info monitorInfoEx
		info.Size = uint32(unsafe.Sizeof(info))
		ok, _, _ := procGetMonitorInfoW.Call(hMonitor, uintptr(unsafe.Pointer(&info)))
		if ok == 0 {
			return 1
		}
		idx := len(monitors) + 1
		device := syscall.UTF16ToString(info.Device[:])
		label := fmt.Sprintf("Monitor %d", idx)
		if device != "" {
			label += " (" + device + ")"
		}
		bounds := image.Rect(int(info.Monitor.Left), int(info.Monitor.Top), int(info.Monitor.Right), int(info.Monitor.Bottom))
		source := protocol.DesktopSource{
			ID:      fmt.Sprintf("monitor:%d", idx),
			Label:   label,
			Kind:    "monitor",
			Backend: "win32-gdi",
			X:       bounds.Min.X,
			Y:       bounds.Min.Y,
			Width:   bounds.Dx(),
			Height:  bounds.Dy(),
			Primary: info.Flags&monitorInfoPrimary != 0,
		}
		monitors = append(monitors, windowsCaptureSource{source: source, bounds: bounds})
		return 1
	})
	procEnumDisplayMonitors.Call(0, 0, callback, 0)
	return monitors
}

func enumerateWindowsWindows(limit int) []windowsCaptureSource {
	var windows []windowsCaptureSource
	callback := syscall.NewCallback(func(hwnd, data uintptr) uintptr {
		if len(windows) >= limit {
			return 0
		}
		visible, _, _ := procIsWindowVisible.Call(hwnd)
		if visible == 0 {
			return 1
		}
		window, err := windowsWindowSource(hwnd)
		if err != nil || window.source.Label == "" || window.bounds.Dx() < 80 || window.bounds.Dy() < 60 {
			return 1
		}
		windows = append(windows, window)
		return 1
	})
	procEnumWindows.Call(callback, 0)
	return windows
}

func windowsWindowSource(hwnd uintptr) (windowsCaptureSource, error) {
	var rect winRect
	ok, _, err := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&rect)))
	if ok == 0 {
		return windowsCaptureSource{}, fmt.Errorf("GetWindowRect failed: %s", windowsCallError(err))
	}
	bounds := image.Rect(int(rect.Left), int(rect.Top), int(rect.Right), int(rect.Bottom))
	length, _, _ := procGetWindowTextLength.Call(hwnd)
	if length == 0 {
		return windowsCaptureSource{}, fmt.Errorf("window has no title")
	}
	titleBuf := make([]uint16, int(length)+1)
	procGetWindowText.Call(hwnd, uintptr(unsafe.Pointer(&titleBuf[0])), uintptr(len(titleBuf)))
	title := strings.TrimSpace(syscall.UTF16ToString(titleBuf))
	if title == "" {
		return windowsCaptureSource{}, fmt.Errorf("window has no title")
	}
	source := protocol.DesktopSource{
		ID:      fmt.Sprintf("window:%x", hwnd),
		Label:   title,
		Kind:    "window",
		Backend: "win32-gdi",
		X:       bounds.Min.X,
		Y:       bounds.Min.Y,
		Width:   bounds.Dx(),
		Height:  bounds.Dy(),
	}
	return windowsCaptureSource{source: source, bounds: bounds}, nil
}

func parseWindowHandle(source string) (uintptr, error) {
	raw := strings.TrimPrefix(source, "window:")
	value, err := strconv.ParseUint(raw, 16, 64)
	if err != nil || value == 0 {
		return 0, fmt.Errorf("invalid window source %q", source)
	}
	return uintptr(value), nil
}

func windowsCallError(err error) string {
	if err == nil || err == syscall.Errno(0) {
		return "unknown error"
	}
	return err.Error()
}

func getSystemMetric(index uintptr) int {
	value, _, _ := procGetSystemMetrics.Call(index)
	return int(int32(value))
}

func (c *gdiDesktopCapturer) Bounds() image.Rectangle { return c.bounds }

func (c *gdiDesktopCapturer) Source() protocol.DesktopSource { return c.source }

func (c *gdiDesktopCapturer) CursorPosition() (image.Point, bool) {
	var point winPoint
	ok, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&point)))
	if ok == 0 {
		return image.Point{}, false
	}
	return image.Pt(int(point.X), int(point.Y)), true
}

func (c *gdiDesktopCapturer) probe() error {
	ok, _, err := procBitBlt.Call(c.memDC, 0, 0, 1, 1, c.screenDC, uintptr(c.bounds.Min.X), uintptr(c.bounds.Min.Y), srcCopy|captureBlt)
	if ok == 0 {
		return fmt.Errorf("desktop capture is not accessible from this Windows session: %s", windowsCallError(err))
	}
	return nil
}

func (c *gdiDesktopCapturer) Close() error {
	if c.memDC != 0 && c.oldObj != 0 {
		procSelectObject.Call(c.memDC, c.oldObj)
		c.oldObj = 0
	}
	if c.bitmap != 0 {
		procDeleteObject.Call(c.bitmap)
		c.bitmap = 0
	}
	if c.memDC != 0 {
		procDeleteDC.Call(c.memDC)
		c.memDC = 0
	}
	if c.screenDC != 0 {
		procReleaseDC.Call(0, c.screenDC)
		c.screenDC = 0
	}
	if c.desktop != 0 {
		procCloseDesktop.Call(c.desktop)
		c.desktop = 0
	}
	return nil
}

func (c *gdiDesktopCapturer) Capture() (image.Image, error) {
	width := c.bounds.Dx()
	height := c.bounds.Dy()
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("invalid capture bounds %v", c.bounds)
	}

	ok, _, err := procBitBlt.Call(
		c.memDC,
		0,
		0,
		uintptr(width),
		uintptr(height),
		c.screenDC,
		uintptr(c.bounds.Min.X),
		uintptr(c.bounds.Min.Y),
		srcCopy|captureBlt,
	)
	if ok == 0 {
		return nil, fmt.Errorf("BitBlt failed: %s", windowsCallError(err))
	}

	pixels := unsafe.Slice((*byte)(c.bits), c.stride*height)
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	parallelDesktopRows(width, height, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			src := pixels[y*c.stride : y*c.stride+width*4]
			dst := img.Pix[y*img.Stride : y*img.Stride+width*4]
			for x := 0; x < width; x++ {
				si := x * 4
				dst[si+0] = src[si+2]
				dst[si+1] = src[si+1]
				dst[si+2] = src[si+0]
				dst[si+3] = 0xff
			}
		}
	})
	return img, nil
}

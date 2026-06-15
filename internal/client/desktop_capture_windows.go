//go:build windows

package client

import (
	"fmt"
	"image"
	"syscall"
	"unsafe"
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

	srcCopy    = 0x00CC0020
	captureBlt = 0x40000000

	biRGB        = 0
	dibRGBColors = 0
)

var (
	user32 = syscall.NewLazyDLL("user32.dll")
	gdi32  = syscall.NewLazyDLL("gdi32.dll")

	procGetSystemMetrics       = user32.NewProc("GetSystemMetrics")
	procOpenInputDesktop       = user32.NewProc("OpenInputDesktop")
	procSetThreadDesktop       = user32.NewProc("SetThreadDesktop")
	procCloseDesktop           = user32.NewProc("CloseDesktop")
	procGetDC                  = user32.NewProc("GetDC")
	procReleaseDC              = user32.NewProc("ReleaseDC")
	procCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject           = gdi32.NewProc("SelectObject")
	procDeleteObject           = gdi32.NewProc("DeleteObject")
	procDeleteDC               = gdi32.NewProc("DeleteDC")
	procBitBlt                 = gdi32.NewProc("BitBlt")
	procGetDIBits              = gdi32.NewProc("GetDIBits")
)

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

type gdiDesktopCapturer struct {
	desktop  uintptr
	screenDC uintptr
	memDC    uintptr
	bitmap   uintptr
	oldObj   uintptr
	bounds   image.Rectangle
}

func newDesktopCapturer(source string) (desktopCapturer, error) {
	desktop, _, _ := procOpenInputDesktop.Call(0, 0, desktopReadObjects|desktopWriteObjects)
	if desktop != 0 {
		procSetThreadDesktop.Call(desktop)
	}

	x := getSystemMetric(smXVirtualScreen)
	y := getSystemMetric(smYVirtualScreen)
	width := getSystemMetric(smCXVirtualScreen)
	height := getSystemMetric(smCYVirtualScreen)
	if source == "primary" {
		x = 0
		y = 0
		width = getSystemMetric(smCXScreen)
		height = getSystemMetric(smCYScreen)
	}
	if width <= 0 || height <= 0 {
		if desktop != 0 {
			procCloseDesktop.Call(desktop)
		}
		return nil, fmt.Errorf("invalid virtual screen size %dx%d", width, height)
	}

	screenDC, _, err := procGetDC.Call(0)
	if screenDC == 0 {
		if desktop != 0 {
			procCloseDesktop.Call(desktop)
		}
		return nil, fmt.Errorf("GetDC failed: %s", windowsCallError(err))
	}
	capturer := &gdiDesktopCapturer{desktop: desktop, screenDC: screenDC, bounds: image.Rect(x, y, x+width, y+height)}

	memDC, _, err := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		capturer.Close()
		return nil, fmt.Errorf("CreateCompatibleDC failed: %s", windowsCallError(err))
	}
	capturer.memDC = memDC

	bitmap, _, err := procCreateCompatibleBitmap.Call(screenDC, uintptr(width), uintptr(height))
	if bitmap == 0 {
		capturer.Close()
		return nil, fmt.Errorf("CreateCompatibleBitmap failed: %s", windowsCallError(err))
	}
	capturer.bitmap = bitmap

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

	stride := width * 4
	pixels := make([]byte, stride*height)
	bmi := bitmapInfo{}
	bmi.Header.Size = uint32(unsafe.Sizeof(bmi.Header))
	bmi.Header.Width = int32(width)
	bmi.Header.Height = -int32(height)
	bmi.Header.Planes = 1
	bmi.Header.BitCount = 32
	bmi.Header.Compression = biRGB
	bmi.Header.SizeImage = uint32(len(pixels))

	ret, _, err := procGetDIBits.Call(
		c.memDC,
		c.bitmap,
		0,
		uintptr(height),
		uintptr(unsafe.Pointer(&pixels[0])),
		uintptr(unsafe.Pointer(&bmi)),
		dibRGBColors,
	)
	if ret == 0 {
		return nil, fmt.Errorf("GetDIBits failed: %s", windowsCallError(err))
	}

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		src := pixels[y*stride : y*stride+stride]
		dst := img.Pix[y*img.Stride : y*img.Stride+width*4]
		for x := 0; x < width; x++ {
			si := x * 4
			dst[si+0] = src[si+2]
			dst[si+1] = src[si+1]
			dst[si+2] = src[si+0]
			dst[si+3] = 0xff
		}
	}
	return img, nil
}

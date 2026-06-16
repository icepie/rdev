//go:build darwin

package client

import (
	"fmt"
	"image"
	"unsafe"

	"github.com/ebitengine/purego"
	"rdev/internal/protocol"
)

type quartzCapturer struct {
	displayID uint32
	bounds    image.Rectangle
}

var (
	cgMainDisplayID        func() uint32
	cgDisplayCreateImage   func(uint32) uintptr
	cgImageGetWidth        func(uintptr) uintptr
	cgImageGetHeight       func(uintptr) uintptr
	cgImageGetBytesPerRow  func(uintptr) uintptr
	cgImageGetBitsPerPixel func(uintptr) uintptr
	cgImageGetBitmapInfo   func(uintptr) uint32
	cgImageGetDataProvider func(uintptr) uintptr
	cgDataProviderCopyData func(uintptr) uintptr
	cfDataGetBytePtr       func(uintptr) uintptr
	cfDataGetLength        func(uintptr) uintptr
	quartzCaptureInitErr   error
	quartzCaptureInitDone  bool
)

func desktopSources() []protocol.DesktopSource {
	return []protocol.DesktopSource{
		{ID: "auto", Label: "Auto", Kind: "screen", Backend: "quartz", Primary: true},
		{ID: "screen:all", Label: "All screens", Kind: "screen", Backend: "quartz", Primary: true},
	}
}

func newDesktopCapturer(source string) (desktopCapturer, error) {
	if source != "" && source != "auto" && source != "screen:all" {
		return nil, fmt.Errorf("unsupported macOS desktop source %q", source)
	}
	if err := initQuartzCapture(); err != nil {
		return nil, err
	}
	displayID := cgMainDisplayID()
	imageRef := cgDisplayCreateImage(displayID)
	if imageRef == 0 {
		return nil, fmt.Errorf("CGDisplayCreateImage failed; grant Screen Recording permission to rdev-client")
	}
	width := int(cgImageGetWidth(imageRef))
	height := int(cgImageGetHeight(imageRef))
	cfRelease(imageRef)
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("CGDisplayCreateImage returned invalid size %dx%d", width, height)
	}
	return &quartzCapturer{displayID: displayID, bounds: image.Rect(0, 0, width, height)}, nil
}

func initQuartzCapture() error {
	if quartzCaptureInitDone {
		return quartzCaptureInitErr
	}
	quartzCaptureInitDone = true
	appServices, err := purego.Dlopen("/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		quartzCaptureInitErr = fmt.Errorf("open ApplicationServices: %w", err)
		return quartzCaptureInitErr
	}
	purego.RegisterLibFunc(&cgMainDisplayID, appServices, "CGMainDisplayID")
	purego.RegisterLibFunc(&cgDisplayCreateImage, appServices, "CGDisplayCreateImage")
	purego.RegisterLibFunc(&cgImageGetWidth, appServices, "CGImageGetWidth")
	purego.RegisterLibFunc(&cgImageGetHeight, appServices, "CGImageGetHeight")
	purego.RegisterLibFunc(&cgImageGetBytesPerRow, appServices, "CGImageGetBytesPerRow")
	purego.RegisterLibFunc(&cgImageGetBitsPerPixel, appServices, "CGImageGetBitsPerPixel")
	purego.RegisterLibFunc(&cgImageGetBitmapInfo, appServices, "CGImageGetBitmapInfo")
	purego.RegisterLibFunc(&cgImageGetDataProvider, appServices, "CGImageGetDataProvider")
	purego.RegisterLibFunc(&cgDataProviderCopyData, appServices, "CGDataProviderCopyData")
	purego.RegisterLibFunc(&cfDataGetBytePtr, appServices, "CFDataGetBytePtr")
	purego.RegisterLibFunc(&cfDataGetLength, appServices, "CFDataGetLength")
	purego.RegisterLibFunc(&cfRelease, appServices, "CFRelease")
	return nil
}

func (q *quartzCapturer) Bounds() image.Rectangle { return q.bounds }
func (q *quartzCapturer) Close() error            { return nil }

func (q *quartzCapturer) Capture() (image.Image, error) {
	imageRef := cgDisplayCreateImage(q.displayID)
	if imageRef == 0 {
		return nil, fmt.Errorf("CGDisplayCreateImage failed; grant Screen Recording permission to rdev-client")
	}
	defer cfRelease(imageRef)

	width := int(cgImageGetWidth(imageRef))
	height := int(cgImageGetHeight(imageRef))
	stride := int(cgImageGetBytesPerRow(imageRef))
	bitsPerPixel := int(cgImageGetBitsPerPixel(imageRef))
	bitmapInfo := cgImageGetBitmapInfo(imageRef)
	if width <= 0 || height <= 0 || stride <= 0 || bitsPerPixel != 32 {
		return nil, fmt.Errorf("unsupported macOS screenshot format: %dx%d stride=%d bpp=%d", width, height, stride, bitsPerPixel)
	}

	provider := cgImageGetDataProvider(imageRef)
	if provider == 0 {
		return nil, fmt.Errorf("CGImageGetDataProvider failed")
	}
	dataRef := cgDataProviderCopyData(provider)
	if dataRef == 0 {
		return nil, fmt.Errorf("CGDataProviderCopyData failed")
	}
	defer cfRelease(dataRef)
	ptr := cfDataGetBytePtr(dataRef)
	length := int(cfDataGetLength(dataRef))
	if ptr == 0 || length < stride*height {
		return nil, fmt.Errorf("invalid macOS screenshot data")
	}

	src := unsafe.Slice((*byte)(unsafe.Pointer(ptr)), length)
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	convertQuartzImage(img, src, stride, bitmapInfo)
	return img, nil
}

func convertQuartzImage(dst *image.RGBA, src []byte, stride int, bitmapInfo uint32) {
	alphaInfo := bitmapInfo & 0x1f
	byteOrder := bitmapInfo & 0x7000
	for y := 0; y < dst.Rect.Dy(); y++ {
		srcRow := src[y*stride:]
		dstRow := dst.Pix[y*dst.Stride:]
		for x := 0; x < dst.Rect.Dx(); x++ {
			s := srcRow[x*4:]
			d := dstRow[x*4:]
			if byteOrder == 0x2000 && (alphaInfo == 2 || alphaInfo == 6) {
				d[0], d[1], d[2] = s[2], s[1], s[0]
			} else if byteOrder == 0x4000 && (alphaInfo == 2 || alphaInfo == 6) {
				d[0], d[1], d[2] = s[1], s[2], s[3]
			} else {
				d[0], d[1], d[2] = s[0], s[1], s[2]
			}
			if alphaInfo == 5 || alphaInfo == 6 {
				d[3] = 0xff
			} else if byteOrder == 0x4000 && alphaInfo == 2 {
				d[3] = s[0]
			} else {
				d[3] = s[3]
			}
		}
	}
}

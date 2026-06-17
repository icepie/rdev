//go:build darwin

package client

import (
	"fmt"
	"image"
	"sort"
	"strconv"
	"strings"
	"unsafe"

	"github.com/ebitengine/purego"
	"rdev/internal/protocol"
)

type cgRect struct {
	X      float64
	Y      float64
	Width  float64
	Height float64
}

type quartzCapturer struct {
	displayID uint32
	windowID  uint32
	kind      string
	bounds    image.Rectangle
	source    protocol.DesktopSource
}

const (
	cgWindowListOptionOnScreenOnly     = 1 << 0
	cgWindowListOptionIncludingWindow  = 1 << 3
	cgWindowListExcludeDesktopElements = 1 << 4
	cgWindowImageDefault               = 0
	cgWindowImageBoundsIgnoreFraming   = 1 << 0
	cgWindowImageBestResolution        = 1 << 3
	cfStringEncodingUTF8               = 0x08000100
	cfNumberSInt64Type                 = 4
	quartzMaxDisplays                  = 32
	quartzMaxWindowSources             = 100
	quartzMinWindowSourceSize          = 32
)

var (
	cgMainDisplayID                        func() uint32
	cgGetActiveDisplayList                 func(uint32, *uint32, *uint32) int32
	cgDisplayBounds                        func(uint32) cgRect
	cgDisplayCreateImage                   func(uint32) uintptr
	cgWindowListCopyWindowInfo             func(uint32, uint32) uintptr
	cgWindowListCreateImage                func(cgRect, uint32, uint32, uint32) uintptr
	cgRectMakeWithDictionaryRepresentation func(uintptr, *cgRect) bool
	cgEventCreate                          func(uintptr) uintptr
	cgEventGetLocation                     func(uintptr) cgPoint
	cgImageGetWidth                        func(uintptr) uintptr
	cgImageGetHeight                       func(uintptr) uintptr
	cgImageGetBytesPerRow                  func(uintptr) uintptr
	cgImageGetBitsPerPixel                 func(uintptr) uintptr
	cgImageGetBitmapInfo                   func(uintptr) uint32
	cgImageGetDataProvider                 func(uintptr) uintptr
	cgDataProviderCopyData                 func(uintptr) uintptr
	cfArrayGetCount                        func(uintptr) int64
	cfArrayGetValueAtIndex                 func(uintptr, int64) uintptr
	cfDictionaryGetValue                   func(uintptr, uintptr) uintptr
	cfNumberGetValue                       func(uintptr, int32, unsafe.Pointer) bool
	cfStringCreateWithCString              func(uintptr, *byte, uint32) uintptr
	cfStringGetCString                     func(uintptr, *byte, int64, uint32) bool
	cfDataGetBytePtr                       func(uintptr) uintptr
	cfDataGetLength                        func(uintptr) uintptr
	quartzCaptureInitErr                   error
	quartzCaptureInitDone                  bool
)

func desktopSources() []protocol.DesktopSource {
	if err := initQuartzCapture(); err != nil {
		return []protocol.DesktopSource{{ID: "auto", Label: "Auto", Kind: "screen", Backend: "quartz", Primary: true}}
	}
	displays := quartzDisplayIDs()
	mainID := cgMainDisplayID()
	mainBounds := quartzDisplayBounds(mainID)
	sources := []protocol.DesktopSource{
		{ID: "auto", Label: "Auto", Kind: "screen", Backend: "quartz", Width: mainBounds.Dx(), Height: mainBounds.Dy(), Primary: true},
		{ID: "screen:all", Label: "Main display", Kind: "screen", Backend: "quartz", Width: mainBounds.Dx(), Height: mainBounds.Dy(), Primary: true},
	}
	for _, displayID := range displays {
		bounds := quartzDisplayBounds(displayID)
		if bounds.Empty() {
			continue
		}
		label := "Display " + strconv.FormatUint(uint64(displayID), 10)
		if displayID == mainID {
			label = "Main display"
		}
		sources = append(sources, protocol.DesktopSource{
			ID:      "display:" + strconv.FormatUint(uint64(displayID), 10),
			Label:   label,
			Kind:    "monitor",
			Backend: "quartz",
			X:       bounds.Min.X,
			Y:       bounds.Min.Y,
			Width:   bounds.Dx(),
			Height:  bounds.Dy(),
			Primary: displayID == mainID,
		})
	}
	sources = append(sources, quartzWindowSources()...)
	return sources
}

func newDesktopCapturer(source string) (desktopCapturer, error) {
	if err := initQuartzCapture(); err != nil {
		return nil, err
	}
	selected, err := resolveQuartzSource(source)
	if err != nil {
		return nil, err
	}
	capturer := &quartzCapturer{displayID: selected.displayID, windowID: selected.windowID, kind: selected.kind, bounds: selected.bounds, source: selected.source}
	if selected.kind == "window" {
		imageRef := capturer.captureWindowImage()
		if imageRef == 0 {
			return nil, fmt.Errorf("CGWindowListCreateImage failed for window %d; grant Screen Recording permission to rdev-client", selected.windowID)
		}
		width := int(cgImageGetWidth(imageRef))
		height := int(cgImageGetHeight(imageRef))
		cfRelease(imageRef)
		if width <= 0 || height <= 0 {
			return nil, fmt.Errorf("CGWindowListCreateImage returned invalid size %dx%d", width, height)
		}
		return capturer, nil
	}
	imageRef := cgDisplayCreateImage(selected.displayID)
	if imageRef == 0 {
		return nil, fmt.Errorf("CGDisplayCreateImage failed; grant Screen Recording permission to rdev-client")
	}
	width := int(cgImageGetWidth(imageRef))
	height := int(cgImageGetHeight(imageRef))
	cfRelease(imageRef)
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("CGDisplayCreateImage returned invalid size %dx%d", width, height)
	}
	if capturer.bounds.Empty() {
		capturer.bounds = image.Rect(0, 0, width, height)
	}
	return capturer, nil
}

type quartzCaptureSource struct {
	source    protocol.DesktopSource
	displayID uint32
	windowID  uint32
	kind      string
	bounds    image.Rectangle
}

func resolveQuartzSource(source string) (quartzCaptureSource, error) {
	mainID := cgMainDisplayID()
	if source == "" || source == "auto" || source == "screen:all" || source == "virtual" {
		bounds := quartzDisplayBounds(mainID)
		return quartzCaptureSource{displayID: mainID, kind: "display", bounds: bounds, source: protocol.DesktopSource{ID: "screen:all", Label: "Main display", Kind: "screen", Backend: "quartz", Width: bounds.Dx(), Height: bounds.Dy(), Primary: true}}, nil
	}
	if strings.HasPrefix(source, "display:") {
		id, err := strconv.ParseUint(strings.TrimPrefix(source, "display:"), 10, 32)
		if err != nil || id == 0 {
			return quartzCaptureSource{}, fmt.Errorf("invalid macOS display source %q", source)
		}
		displayID := uint32(id)
		bounds := quartzDisplayBounds(displayID)
		if bounds.Empty() || !quartzDisplayExists(displayID) {
			return quartzCaptureSource{}, fmt.Errorf("macOS display %d not found", displayID)
		}
		return quartzCaptureSource{displayID: displayID, kind: "display", bounds: bounds, source: protocol.DesktopSource{ID: source, Label: "Display " + strconv.FormatUint(id, 10), Kind: "monitor", Backend: "quartz", X: bounds.Min.X, Y: bounds.Min.Y, Width: bounds.Dx(), Height: bounds.Dy(), Primary: displayID == mainID}}, nil
	}
	if strings.HasPrefix(source, "window:") {
		id, err := strconv.ParseUint(strings.TrimPrefix(source, "window:"), 10, 32)
		if err != nil || id == 0 {
			return quartzCaptureSource{}, fmt.Errorf("invalid macOS window source %q", source)
		}
		for _, candidate := range quartzWindowCaptureSources() {
			if candidate.windowID == uint32(id) {
				return candidate, nil
			}
		}
		return quartzCaptureSource{}, fmt.Errorf("macOS window %d not found or not capturable", id)
	}
	return quartzCaptureSource{}, fmt.Errorf("unsupported macOS desktop source %q", source)
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
	purego.RegisterLibFunc(&cgGetActiveDisplayList, appServices, "CGGetActiveDisplayList")
	purego.RegisterLibFunc(&cgDisplayBounds, appServices, "CGDisplayBounds")
	purego.RegisterLibFunc(&cgDisplayCreateImage, appServices, "CGDisplayCreateImage")
	purego.RegisterLibFunc(&cgWindowListCopyWindowInfo, appServices, "CGWindowListCopyWindowInfo")
	purego.RegisterLibFunc(&cgWindowListCreateImage, appServices, "CGWindowListCreateImage")
	purego.RegisterLibFunc(&cgRectMakeWithDictionaryRepresentation, appServices, "CGRectMakeWithDictionaryRepresentation")
	purego.RegisterLibFunc(&cgEventCreate, appServices, "CGEventCreate")
	purego.RegisterLibFunc(&cgEventGetLocation, appServices, "CGEventGetLocation")
	purego.RegisterLibFunc(&cgImageGetWidth, appServices, "CGImageGetWidth")
	purego.RegisterLibFunc(&cgImageGetHeight, appServices, "CGImageGetHeight")
	purego.RegisterLibFunc(&cgImageGetBytesPerRow, appServices, "CGImageGetBytesPerRow")
	purego.RegisterLibFunc(&cgImageGetBitsPerPixel, appServices, "CGImageGetBitsPerPixel")
	purego.RegisterLibFunc(&cgImageGetBitmapInfo, appServices, "CGImageGetBitmapInfo")
	purego.RegisterLibFunc(&cgImageGetDataProvider, appServices, "CGImageGetDataProvider")
	purego.RegisterLibFunc(&cgDataProviderCopyData, appServices, "CGDataProviderCopyData")
	purego.RegisterLibFunc(&cfArrayGetCount, appServices, "CFArrayGetCount")
	purego.RegisterLibFunc(&cfArrayGetValueAtIndex, appServices, "CFArrayGetValueAtIndex")
	purego.RegisterLibFunc(&cfDictionaryGetValue, appServices, "CFDictionaryGetValue")
	purego.RegisterLibFunc(&cfNumberGetValue, appServices, "CFNumberGetValue")
	purego.RegisterLibFunc(&cfStringCreateWithCString, appServices, "CFStringCreateWithCString")
	purego.RegisterLibFunc(&cfStringGetCString, appServices, "CFStringGetCString")
	purego.RegisterLibFunc(&cfDataGetBytePtr, appServices, "CFDataGetBytePtr")
	purego.RegisterLibFunc(&cfDataGetLength, appServices, "CFDataGetLength")
	purego.RegisterLibFunc(&cfRelease, appServices, "CFRelease")
	return nil
}

func quartzDisplayIDs() []uint32 {
	if cgGetActiveDisplayList == nil {
		return []uint32{cgMainDisplayID()}
	}
	displays := make([]uint32, quartzMaxDisplays)
	var count uint32
	if errCode := cgGetActiveDisplayList(uint32(len(displays)), &displays[0], &count); errCode != 0 || count == 0 {
		return []uint32{cgMainDisplayID()}
	}
	return displays[:count]
}

func quartzDisplayExists(displayID uint32) bool {
	for _, candidate := range quartzDisplayIDs() {
		if candidate == displayID {
			return true
		}
	}
	return false
}

func quartzDisplayBounds(displayID uint32) image.Rectangle {
	if cgDisplayBounds == nil {
		return image.Rectangle{}
	}
	bounds := cgDisplayBounds(displayID)
	return quartzRectToImage(bounds)
}

func quartzRectToImage(bounds cgRect) image.Rectangle {
	minX := int(bounds.X)
	minY := int(bounds.Y)
	width := int(bounds.Width + 0.5)
	height := int(bounds.Height + 0.5)
	if width <= 0 || height <= 0 {
		return image.Rectangle{}
	}
	return image.Rect(minX, minY, minX+width, minY+height)
}

func quartzWindowSources() []protocol.DesktopSource {
	captures := quartzWindowCaptureSources()
	sources := make([]protocol.DesktopSource, 0, len(captures))
	for _, capture := range captures {
		sources = append(sources, capture.source)
	}
	return sources
}

func quartzWindowCaptureSources() []quartzCaptureSource {
	if cgWindowListCopyWindowInfo == nil {
		return nil
	}
	array := cgWindowListCopyWindowInfo(cgWindowListOptionOnScreenOnly|cgWindowListExcludeDesktopElements, 0)
	if array == 0 {
		return nil
	}
	defer cfRelease(array)
	count := cfArrayGetCount(array)
	windows := make([]quartzCaptureSource, 0, min(int(count), quartzMaxWindowSources))
	seen := map[uint32]bool{}
	for i := int64(0); i < count && len(windows) < quartzMaxWindowSources; i++ {
		dict := cfArrayGetValueAtIndex(array, i)
		if dict == 0 {
			continue
		}
		windowID, ok := quartzDictInt(dict, "kCGWindowNumber")
		if !ok || windowID <= 0 || seen[uint32(windowID)] {
			continue
		}
		layer, ok := quartzDictInt(dict, "kCGWindowLayer")
		if ok && layer != 0 {
			continue
		}
		boundsValue := quartzDictValue(dict, "kCGWindowBounds")
		if boundsValue == 0 || cgRectMakeWithDictionaryRepresentation == nil {
			continue
		}
		var rect cgRect
		if !cgRectMakeWithDictionaryRepresentation(boundsValue, &rect) {
			continue
		}
		bounds := quartzRectToImage(rect)
		if bounds.Dx() < quartzMinWindowSourceSize || bounds.Dy() < quartzMinWindowSourceSize {
			continue
		}
		owner := quartzDictString(dict, "kCGWindowOwnerName")
		title := quartzDictString(dict, "kCGWindowName")
		label := quartzWindowLabel(owner, title, uint32(windowID))
		seen[uint32(windowID)] = true
		windows = append(windows, quartzCaptureSource{
			windowID: uint32(windowID),
			kind:     "window",
			bounds:   bounds,
			source: protocol.DesktopSource{
				ID:      "window:" + strconv.FormatInt(windowID, 10),
				Label:   label,
				Kind:    "window",
				Backend: "quartz",
				X:       bounds.Min.X,
				Y:       bounds.Min.Y,
				Width:   bounds.Dx(),
				Height:  bounds.Dy(),
			},
		})
	}
	sort.SliceStable(windows, func(i, j int) bool { return windows[i].source.Label < windows[j].source.Label })
	return windows
}

func quartzWindowLabel(owner, title string, windowID uint32) string {
	owner = strings.TrimSpace(owner)
	title = strings.TrimSpace(title)
	switch {
	case owner != "" && title != "":
		return owner + " - " + title
	case owner != "":
		return owner
	case title != "":
		return title
	default:
		return "Window " + strconv.FormatUint(uint64(windowID), 10)
	}
}

func quartzDictValue(dict uintptr, key string) uintptr {
	if cfDictionaryGetValue == nil || cfStringCreateWithCString == nil {
		return 0
	}
	keyBytes := append([]byte(key), 0)
	keyRef := cfStringCreateWithCString(0, &keyBytes[0], cfStringEncodingUTF8)
	if keyRef == 0 {
		return 0
	}
	defer cfRelease(keyRef)
	return cfDictionaryGetValue(dict, keyRef)
}

func quartzDictInt(dict uintptr, key string) (int64, bool) {
	value := quartzDictValue(dict, key)
	if value == 0 || cfNumberGetValue == nil {
		return 0, false
	}
	var out int64
	if !cfNumberGetValue(value, cfNumberSInt64Type, unsafe.Pointer(&out)) {
		return 0, false
	}
	return out, true
}

func quartzDictString(dict uintptr, key string) string {
	value := quartzDictValue(dict, key)
	if value == 0 || cfStringGetCString == nil {
		return ""
	}
	buf := make([]byte, 1024)
	if !cfStringGetCString(value, &buf[0], int64(len(buf)), cfStringEncodingUTF8) {
		return ""
	}
	if idx := strings.IndexByte(string(buf), 0); idx >= 0 {
		return string(buf[:idx])
	}
	return string(buf)
}

func (q *quartzCapturer) Bounds() image.Rectangle { return q.bounds }
func (q *quartzCapturer) Close() error            { return nil }
func (q *quartzCapturer) Source() protocol.DesktopSource {
	return q.source
}

func (q *quartzCapturer) CursorPosition() (image.Point, bool) {
	if cgEventCreate == nil || cgEventGetLocation == nil {
		return image.Point{}, false
	}
	event := cgEventCreate(0)
	if event == 0 {
		return image.Point{}, false
	}
	defer cfRelease(event)
	point := cgEventGetLocation(event)
	return image.Pt(int(point.X+0.5), int(point.Y+0.5)), true
}

func (q *quartzCapturer) Capture() (image.Image, error) {
	var imageRef uintptr
	if q.kind == "window" {
		imageRef = q.captureWindowImage()
		if imageRef == 0 {
			return nil, fmt.Errorf("CGWindowListCreateImage failed for window %d; grant Screen Recording permission to rdev-client", q.windowID)
		}
	} else {
		imageRef = cgDisplayCreateImage(q.displayID)
		if imageRef == 0 {
			return nil, fmt.Errorf("CGDisplayCreateImage failed; grant Screen Recording permission to rdev-client")
		}
	}
	defer cfRelease(imageRef)
	return quartzImageToRGBA(imageRef)
}

func (q *quartzCapturer) captureWindowImage() uintptr {
	if cgWindowListCreateImage == nil || q.windowID == 0 {
		return 0
	}
	rect := cgRect{X: float64(q.bounds.Min.X), Y: float64(q.bounds.Min.Y), Width: float64(q.bounds.Dx()), Height: float64(q.bounds.Dy())}
	return cgWindowListCreateImage(rect, cgWindowListOptionIncludingWindow, q.windowID, cgWindowImageDefault|cgWindowImageBoundsIgnoreFraming|cgWindowImageBestResolution)
}

func quartzImageToRGBA(imageRef uintptr) (image.Image, error) {
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
	width := dst.Rect.Dx()
	height := dst.Rect.Dy()
	parallelDesktopRows(width, height, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			srcRow := src[y*stride:]
			dstRow := dst.Pix[y*dst.Stride:]
			for x := 0; x < width; x++ {
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
	})
}

//go:build windows

package client

import (
	"errors"
	"fmt"
	"image"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"rdev/internal/protocol"
)

const (
	dxgiErrorNotFound     = 0x887A0002
	dxgiErrorWaitTimeout  = 0x887A0027
	dxgiFormatR8G8B8A8    = 28
	dxgiFormatB8G8R8A8    = 87
	dxgiOutputAttached    = 1
	d3dDriverTypeUnknown  = 0
	d3d11CreateDeviceBGRA = 0x20
	d3d11SDKVersion       = 7
	d3d11UsageStaging     = 3
	d3d11CPUAccessRead    = 0x20000
	d3d11MapRead          = 1
	dxgiAcquireTimeoutMS  = 500
)

var (
	dxgiDLL  = syscall.NewLazyDLL("dxgi.dll")
	d3d11DLL = syscall.NewLazyDLL("d3d11.dll")
	ntdllDLL = syscall.NewLazyDLL("ntdll.dll")

	procCreateDXGIFactory1 = dxgiDLL.NewProc("CreateDXGIFactory1")
	procD3D11CreateDevice  = d3d11DLL.NewProc("D3D11CreateDevice")
	procRtlGetVersion      = ntdllDLL.NewProc("RtlGetVersion")

	errDXGIFrameTimeout = errors.New("DXGI frame timeout")
)

type winOSVersionInfo struct {
	Size         uint32
	MajorVersion uint32
	MinorVersion uint32
	BuildNumber  uint32
	PlatformID   uint32
	CSDVersion   [128]uint16
}

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

var (
	iidIDXGIFactory1   = guid{0x770aae78, 0xf26f, 0x4dba, [8]byte{0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87}}
	iidIDXGIOutput1    = guid{0x00cddea8, 0x939b, 0x4b83, [8]byte{0xa3, 0x40, 0xa6, 0x85, 0x22, 0x66, 0x66, 0xcc}}
	iidID3D11Texture2D = guid{0x6f15aaf2, 0xd208, 0x4e89, [8]byte{0x9a, 0xb4, 0x48, 0x95, 0x35, 0xd3, 0x4f, 0x9c}}
)

type dxgiOutputDesc struct {
	DeviceName         [32]uint16
	DesktopCoordinates winRect
	AttachedToDesktop  int32
	Rotation           uint32
	Monitor            uintptr
}

type dxgiSampleDesc struct {
	Count   uint32
	Quality uint32
}

type d3d11Texture2DDesc struct {
	Width          uint32
	Height         uint32
	MipLevels      uint32
	ArraySize      uint32
	Format         uint32
	SampleDesc     dxgiSampleDesc
	Usage          uint32
	BindFlags      uint32
	CPUAccessFlags uint32
	MiscFlags      uint32
}

type d3d11MappedSubresource struct {
	Data       uintptr
	RowPitch   uint32
	DepthPitch uint32
}

type dxgiOutduplPointerPosition struct {
	Position winPoint
	Visible  int32
}

type winPoint struct {
	X int32
	Y int32
}

type dxgiOutduplFrameInfo struct {
	LastPresentTime           int64
	LastMouseUpdateTime       int64
	AccumulatedFrames         uint32
	RectsCoalesced            int32
	ProtectedContentMaskedOut int32
	PointerPosition           dxgiOutduplPointerPosition
	TotalMetadataBufferSize   uint32
	PointerShapeBufferSize    uint32
}

type dxgiCaptureSource struct {
	source     protocol.DesktopSource
	adapterIdx uint32
	outputIdx  uint32
	bounds     image.Rectangle
}

type dxgiDesktopCapturer struct {
	adapter       uintptr
	output        uintptr
	output1       uintptr
	device        uintptr
	context       uintptr
	duplication   uintptr
	staging       uintptr
	bounds        image.Rectangle
	source        protocol.DesktopSource
	textureWidth  uint32
	textureHeight uint32
	textureFormat uint32
	lastFrame     *image.RGBA
}

func enumerateDXGISources() []protocol.DesktopSource {
	captures := enumerateDXGICaptureSources()
	sources := make([]protocol.DesktopSource, 0, len(captures))
	for _, capture := range captures {
		sources = append(sources, capture.source)
	}
	return sources
}

func enumerateDXGICaptureSources() []dxgiCaptureSource {
	if !dxgiSupportedOS() {
		return nil
	}
	factory, err := createDXGIFactory1()
	if err != nil {
		return nil
	}
	defer comRelease(factory)

	var sources []dxgiCaptureSource
	for adapterIdx := uint32(0); ; adapterIdx++ {
		adapter, hr := dxgiFactoryEnumAdapters1(factory, adapterIdx)
		if hr == dxgiErrorNotFound {
			break
		}
		if failedHR(hr) || adapter == 0 {
			break
		}
		for outputIdx := uint32(0); ; outputIdx++ {
			output, hr := dxgiAdapterEnumOutputs(adapter, outputIdx)
			if hr == dxgiErrorNotFound {
				break
			}
			if failedHR(hr) || output == 0 {
				break
			}
			desc, hr := dxgiOutputGetDesc(output)
			comRelease(output)
			if failedHR(hr) || desc.AttachedToDesktop != dxgiOutputAttached {
				continue
			}
			bounds := image.Rect(int(desc.DesktopCoordinates.Left), int(desc.DesktopCoordinates.Top), int(desc.DesktopCoordinates.Right), int(desc.DesktopCoordinates.Bottom))
			if bounds.Dx() <= 0 || bounds.Dy() <= 0 {
				continue
			}
			label := syscall.UTF16ToString(desc.DeviceName[:])
			if label == "" {
				label = fmt.Sprintf("DXGI Monitor %d", len(sources)+1)
			}
			sources = append(sources, dxgiCaptureSource{
				adapterIdx: adapterIdx,
				outputIdx:  outputIdx,
				bounds:     bounds,
				source: protocol.DesktopSource{
					ID:      fmt.Sprintf("dxgi:monitor:%d:%d", adapterIdx, outputIdx),
					Label:   "DXGI " + label,
					Kind:    "monitor",
					Backend: "dxgi",
					X:       bounds.Min.X,
					Y:       bounds.Min.Y,
					Width:   bounds.Dx(),
					Height:  bounds.Dy(),
					Primary: len(sources) == 0,
				},
			})
		}
		comRelease(adapter)
	}
	return sources
}

func resolveDXGICaptureSource(source string) (dxgiCaptureSource, error) {
	for _, candidate := range enumerateDXGICaptureSources() {
		if candidate.source.ID == source {
			return candidate, nil
		}
	}
	if strings.HasPrefix(source, "dxgi:monitor:") {
		parts := strings.Split(source, ":")
		if len(parts) == 4 {
			adapterIdx, adapterErr := strconv.ParseUint(parts[2], 10, 32)
			outputIdx, outputErr := strconv.ParseUint(parts[3], 10, 32)
			if adapterErr == nil && outputErr == nil {
				return dxgiCaptureSource{adapterIdx: uint32(adapterIdx), outputIdx: uint32(outputIdx), source: protocol.DesktopSource{ID: source, Label: source, Kind: "monitor", Backend: "dxgi"}}, nil
			}
		}
	}
	return dxgiCaptureSource{}, fmt.Errorf("DXGI source %q not found", source)
}

func newDXGICapturer(source string) (desktopCapturer, error) {
	if !dxgiSupportedOS() {
		return nil, fmt.Errorf("DXGI Desktop Duplication requires Windows 8 or newer")
	}
	selected, err := resolveDXGICaptureSource(source)
	if err != nil {
		return nil, err
	}
	factory, err := createDXGIFactory1()
	if err != nil {
		return nil, err
	}
	defer comRelease(factory)

	adapter, hr := dxgiFactoryEnumAdapters1(factory, selected.adapterIdx)
	if failedHR(hr) || adapter == 0 {
		return nil, fmt.Errorf("DXGI EnumAdapters1(%d) failed: %s", selected.adapterIdx, formatHRESULT(hr))
	}
	capturer := &dxgiDesktopCapturer{adapter: adapter, bounds: selected.bounds, source: selected.source}

	output, hr := dxgiAdapterEnumOutputs(adapter, selected.outputIdx)
	if failedHR(hr) || output == 0 {
		capturer.Close()
		return nil, fmt.Errorf("DXGI EnumOutputs(%d) failed: %s", selected.outputIdx, formatHRESULT(hr))
	}
	capturer.output = output
	desc, hr := dxgiOutputGetDesc(output)
	if failedHR(hr) {
		capturer.Close()
		return nil, fmt.Errorf("DXGI GetDesc failed: %s", formatHRESULT(hr))
	}
	bounds := image.Rect(int(desc.DesktopCoordinates.Left), int(desc.DesktopCoordinates.Top), int(desc.DesktopCoordinates.Right), int(desc.DesktopCoordinates.Bottom))
	if bounds.Dx() > 0 && bounds.Dy() > 0 {
		capturer.bounds = bounds
		capturer.source.X = bounds.Min.X
		capturer.source.Y = bounds.Min.Y
		capturer.source.Width = bounds.Dx()
		capturer.source.Height = bounds.Dy()
	}

	output1, hr := comQueryInterface(output, &iidIDXGIOutput1)
	if failedHR(hr) || output1 == 0 {
		capturer.Close()
		return nil, fmt.Errorf("DXGI Output1 is unavailable: %s", formatHRESULT(hr))
	}
	capturer.output1 = output1

	device, context, hr := d3d11CreateDevice(adapter)
	if failedHR(hr) || device == 0 || context == 0 {
		capturer.Close()
		return nil, fmt.Errorf("D3D11CreateDevice failed: %s", formatHRESULT(hr))
	}
	capturer.device = device
	capturer.context = context

	duplication, hr := dxgiOutput1DuplicateOutput(output1, device)
	if failedHR(hr) || duplication == 0 {
		capturer.Close()
		return nil, fmt.Errorf("DXGI DuplicateOutput failed: %s", formatHRESULT(hr))
	}
	capturer.duplication = duplication
	return capturer, nil
}

func (c *dxgiDesktopCapturer) Bounds() image.Rectangle { return c.bounds }

func (c *dxgiDesktopCapturer) Source() protocol.DesktopSource { return c.source }

func (c *dxgiDesktopCapturer) Close() error {
	if c.staging != 0 {
		comRelease(c.staging)
		c.staging = 0
	}
	if c.duplication != 0 {
		comRelease(c.duplication)
		c.duplication = 0
	}
	if c.context != 0 {
		comRelease(c.context)
		c.context = 0
	}
	if c.device != 0 {
		comRelease(c.device)
		c.device = 0
	}
	if c.output1 != 0 {
		comRelease(c.output1)
		c.output1 = 0
	}
	if c.output != 0 {
		comRelease(c.output)
		c.output = 0
	}
	if c.adapter != 0 {
		comRelease(c.adapter)
		c.adapter = 0
	}
	return nil
}

func (c *dxgiDesktopCapturer) Capture() (image.Image, error) {
	deadline := time.Now().Add(2 * time.Second)
	var resource uintptr
	for {
		var err error
		resource, err = c.acquireFrame()
		if err == nil {
			break
		}
		if !errors.Is(err, errDXGIFrameTimeout) {
			return nil, err
		}
		if c.lastFrame != nil {
			return c.lastFrame, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("DXGI AcquireNextFrame timed out before the first frame")
		}
	}
	defer comRelease(resource)
	defer dxgiOutputDuplicationReleaseFrame(c.duplication)

	texture, hr := comQueryInterface(resource, &iidID3D11Texture2D)
	if failedHR(hr) || texture == 0 {
		return nil, fmt.Errorf("DXGI frame QueryInterface(ID3D11Texture2D) failed: %s", formatHRESULT(hr))
	}
	defer comRelease(texture)

	desc := d3d11Texture2DGetDesc(texture)
	if desc.Width == 0 || desc.Height == 0 {
		if c.lastFrame != nil {
			return c.lastFrame, nil
		}
		return nil, fmt.Errorf("DXGI frame has invalid size")
	}
	if desc.Format != dxgiFormatB8G8R8A8 && desc.Format != dxgiFormatR8G8B8A8 {
		return nil, fmt.Errorf("unsupported DXGI frame format %d", desc.Format)
	}
	if err := c.ensureStaging(desc); err != nil {
		return nil, err
	}
	d3d11ContextCopyResource(c.context, c.staging, texture)
	mapped, hr := d3d11ContextMap(c.context, c.staging, d3d11MapRead)
	if failedHR(hr) || mapped.Data == 0 {
		return nil, fmt.Errorf("D3D11 Map staging texture failed: %s", formatHRESULT(hr))
	}
	defer d3d11ContextUnmap(c.context, c.staging)

	width := int(desc.Width)
	height := int(desc.Height)
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		src := unsafe.Slice((*byte)(unsafe.Pointer(mapped.Data+uintptr(y)*uintptr(mapped.RowPitch))), width*4)
		dst := img.Pix[y*img.Stride : y*img.Stride+width*4]
		if desc.Format == dxgiFormatB8G8R8A8 {
			for x := 0; x < width; x++ {
				off := x * 4
				dst[off] = src[off+2]
				dst[off+1] = src[off+1]
				dst[off+2] = src[off]
				dst[off+3] = 255
			}
			continue
		}
		for x := 0; x < width; x++ {
			off := x * 4
			dst[off] = src[off]
			dst[off+1] = src[off+1]
			dst[off+2] = src[off+2]
			dst[off+3] = 255
		}
	}
	c.lastFrame = img
	return img, nil
}

func (c *dxgiDesktopCapturer) acquireFrame() (uintptr, error) {
	var frameInfo dxgiOutduplFrameInfo
	var resource uintptr
	hr := dxgiOutputDuplicationAcquireNextFrame(c.duplication, dxgiAcquireTimeoutMS, &frameInfo, &resource)
	if hr == 0 && resource != 0 {
		return resource, nil
	}
	if hr == dxgiErrorWaitTimeout {
		return 0, errDXGIFrameTimeout
	}
	return 0, fmt.Errorf("DXGI AcquireNextFrame failed: %s", formatHRESULT(hr))
}

func (c *dxgiDesktopCapturer) ensureStaging(desc d3d11Texture2DDesc) error {
	if c.staging != 0 && c.textureWidth == desc.Width && c.textureHeight == desc.Height && c.textureFormat == desc.Format {
		return nil
	}
	if c.staging != 0 {
		comRelease(c.staging)
		c.staging = 0
	}
	stagingDesc := desc
	stagingDesc.MipLevels = 1
	stagingDesc.ArraySize = 1
	stagingDesc.SampleDesc.Count = 1
	stagingDesc.SampleDesc.Quality = 0
	stagingDesc.Usage = d3d11UsageStaging
	stagingDesc.BindFlags = 0
	stagingDesc.CPUAccessFlags = d3d11CPUAccessRead
	stagingDesc.MiscFlags = 0
	texture, hr := d3d11DeviceCreateTexture2D(c.device, &stagingDesc)
	if failedHR(hr) || texture == 0 {
		return fmt.Errorf("D3D11 CreateTexture2D staging failed: %s", formatHRESULT(hr))
	}
	c.staging = texture
	c.textureWidth = desc.Width
	c.textureHeight = desc.Height
	c.textureFormat = desc.Format
	return nil
}

func dxgiSupportedOS() bool {
	var info winOSVersionInfo
	info.Size = uint32(unsafe.Sizeof(info))
	hr, _, _ := procRtlGetVersion.Call(uintptr(unsafe.Pointer(&info)))
	if hr != 0 {
		return false
	}
	return info.MajorVersion > 6 || (info.MajorVersion == 6 && info.MinorVersion >= 2)
}

func createDXGIFactory1() (uintptr, error) {
	if err := dxgiDLL.Load(); err != nil {
		return 0, fmt.Errorf("load dxgi.dll: %w", err)
	}
	var factory uintptr
	hr, _, _ := procCreateDXGIFactory1.Call(uintptr(unsafe.Pointer(&iidIDXGIFactory1)), uintptr(unsafe.Pointer(&factory)))
	if failedHR(uint32(hr)) || factory == 0 {
		return 0, fmt.Errorf("CreateDXGIFactory1 failed: %s", formatHRESULT(uint32(hr)))
	}
	return factory, nil
}

func d3d11CreateDevice(adapter uintptr) (uintptr, uintptr, uint32) {
	var device uintptr
	var context uintptr
	var featureLevel uint32
	hr, _, _ := procD3D11CreateDevice.Call(
		adapter,
		d3dDriverTypeUnknown,
		0,
		d3d11CreateDeviceBGRA,
		0,
		0,
		d3d11SDKVersion,
		uintptr(unsafe.Pointer(&device)),
		uintptr(unsafe.Pointer(&featureLevel)),
		uintptr(unsafe.Pointer(&context)),
	)
	return device, context, uint32(hr)
}

func dxgiFactoryEnumAdapters1(factory uintptr, index uint32) (uintptr, uint32) {
	var adapter uintptr
	hr := comCall(factory, 12, uintptr(index), uintptr(unsafe.Pointer(&adapter)))
	return adapter, hr
}

func dxgiAdapterEnumOutputs(adapter uintptr, index uint32) (uintptr, uint32) {
	var output uintptr
	hr := comCall(adapter, 7, uintptr(index), uintptr(unsafe.Pointer(&output)))
	return output, hr
}

func dxgiOutputGetDesc(output uintptr) (dxgiOutputDesc, uint32) {
	var desc dxgiOutputDesc
	hr := comCall(output, 7, uintptr(unsafe.Pointer(&desc)))
	return desc, hr
}

func dxgiOutput1DuplicateOutput(output1 uintptr, device uintptr) (uintptr, uint32) {
	var duplication uintptr
	hr := comCall(output1, 22, device, uintptr(unsafe.Pointer(&duplication)))
	return duplication, hr
}

func dxgiOutputDuplicationAcquireNextFrame(duplication uintptr, timeoutMS uint32, frameInfo *dxgiOutduplFrameInfo, resource *uintptr) uint32 {
	return comCall(duplication, 8, uintptr(timeoutMS), uintptr(unsafe.Pointer(frameInfo)), uintptr(unsafe.Pointer(resource)))
}

func dxgiOutputDuplicationReleaseFrame(duplication uintptr) uint32 {
	return comCall(duplication, 14)
}

func d3d11Texture2DGetDesc(texture uintptr) d3d11Texture2DDesc {
	var desc d3d11Texture2DDesc
	comCall(texture, 10, uintptr(unsafe.Pointer(&desc)))
	return desc
}

func d3d11DeviceCreateTexture2D(device uintptr, desc *d3d11Texture2DDesc) (uintptr, uint32) {
	var texture uintptr
	hr := comCall(device, 5, uintptr(unsafe.Pointer(desc)), 0, uintptr(unsafe.Pointer(&texture)))
	return texture, hr
}

func d3d11ContextCopyResource(context uintptr, dst uintptr, src uintptr) {
	comCall(context, 47, dst, src)
}

func d3d11ContextMap(context uintptr, resource uintptr, mapType uint32) (d3d11MappedSubresource, uint32) {
	var mapped d3d11MappedSubresource
	hr := comCall(context, 14, resource, 0, uintptr(mapType), 0, uintptr(unsafe.Pointer(&mapped)))
	return mapped, hr
}

func d3d11ContextUnmap(context uintptr, resource uintptr) {
	comCall(context, 15, resource, 0)
}

func comQueryInterface(obj uintptr, iid *guid) (uintptr, uint32) {
	var out uintptr
	hr := comCall(obj, 0, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out)))
	return out, hr
}

func comRelease(obj uintptr) {
	if obj != 0 {
		comCall(obj, 2)
	}
}

func comCall(obj uintptr, method uintptr, args ...uintptr) uint32 {
	vtable := *(*uintptr)(unsafe.Pointer(obj))
	fn := *(*uintptr)(unsafe.Pointer(vtable + method*unsafe.Sizeof(uintptr(0))))
	callArgs := make([]uintptr, 0, len(args)+1)
	callArgs = append(callArgs, obj)
	callArgs = append(callArgs, args...)
	hr, _, _ := syscall.SyscallN(fn, callArgs...)
	return uint32(hr)
}

func failedHR(hr uint32) bool {
	return int32(hr) < 0
}

func formatHRESULT(hr uint32) string {
	if hr == 0 {
		return "S_OK"
	}
	return fmt.Sprintf("HRESULT 0x%08x", hr)
}

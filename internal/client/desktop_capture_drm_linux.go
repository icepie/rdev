//go:build linux

package client

import (
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"rdev/internal/protocol"
)

const (
	drmConnectorConnected   = 1
	drmModeFBModifiers      = 1 << 1
	drmFormatModLinear      = 0
	drmPrimeHandleToFDFlags = 0
	drmFormatModVendorIntel = 0x01
	i915FormatModXTiled     = uint64(drmFormatModVendorIntel)<<56 | 1
)

const (
	drmFormatXRGB8888 = uint32('X') | uint32('R')<<8 | uint32('2')<<16 | uint32('4')<<24
	drmFormatXBGR8888 = uint32('X') | uint32('B')<<8 | uint32('2')<<16 | uint32('4')<<24
	drmFormatRGBX8888 = uint32('R') | uint32('X')<<8 | uint32('2')<<16 | uint32('4')<<24
	drmFormatBGRX8888 = uint32('B') | uint32('X')<<8 | uint32('2')<<16 | uint32('4')<<24
	drmFormatARGB8888 = uint32('A') | uint32('R')<<8 | uint32('2')<<16 | uint32('4')<<24
	drmFormatABGR8888 = uint32('A') | uint32('B')<<8 | uint32('2')<<16 | uint32('4')<<24
	drmFormatRGBA8888 = uint32('R') | uint32('A')<<8 | uint32('2')<<16 | uint32('4')<<24
	drmFormatBGRA8888 = uint32('B') | uint32('A')<<8 | uint32('2')<<16 | uint32('4')<<24
	drmFormatRGB565   = uint32('R') | uint32('G')<<8 | uint32('1')<<16 | uint32('6')<<24
)

type drmModeCardRes struct {
	FBIDPtr         uint64
	CRTCIDPtr       uint64
	ConnectorIDPtr  uint64
	EncoderIDPtr    uint64
	CountFBs        uint32
	CountCRTCs      uint32
	CountConnectors uint32
	CountEncoders   uint32
	MinWidth        uint32
	MaxWidth        uint32
	MinHeight       uint32
	MaxHeight       uint32
}

type drmModeModeInfo struct {
	Clock      uint32
	HDisplay   uint16
	HSyncStart uint16
	HSyncEnd   uint16
	HTotal     uint16
	HSkew      uint16
	VDisplay   uint16
	VSyncStart uint16
	VSyncEnd   uint16
	VTotal     uint16
	VScan      uint16
	VRefresh   uint32
	Flags      uint32
	Type       uint32
	Name       [32]byte
}

type drmModeCRTC struct {
	SetConnectorsPtr uint64
	CountConnectors  uint32
	CRTCID           uint32
	FBID             uint32
	X                uint32
	Y                uint32
	GammaSize        uint32
	ModeValid        uint32
	Mode             drmModeModeInfo
}

type drmModeGetEncoder struct {
	EncoderID      uint32
	EncoderType    uint32
	CRTCID         uint32
	PossibleCRTCs  uint32
	PossibleClones uint32
}

type drmModeGetConnector struct {
	EncodersPtr     uint64
	ModesPtr        uint64
	PropsPtr        uint64
	PropValuesPtr   uint64
	CountModes      uint32
	CountProps      uint32
	CountEncoders   uint32
	EncoderID       uint32
	ConnectorID     uint32
	ConnectorType   uint32
	ConnectorTypeID uint32
	Connection      uint32
	MMWidth         uint32
	MMHeight        uint32
	SubPixel        uint32
	Pad             uint32
}

type drmModeFBCmd2 struct {
	FBID        uint32
	Width       uint32
	Height      uint32
	PixelFormat uint32
	Flags       uint32
	Handles     [4]uint32
	Pitches     [4]uint32
	Offsets     [4]uint32
	Modifiers   [4]uint64
}

type drmModeFBCmd struct {
	FBID   uint32
	Width  uint32
	Height uint32
	Pitch  uint32
	BPP    uint32
	Depth  uint32
	Handle uint32
}

type drmModeMapDumb struct {
	Handle uint32
	Pad    uint32
	Offset uint64
}

type drmGemFlink struct {
	Handle uint32
	Name   uint32
}

type drmGemOpen struct {
	Name   uint32
	Handle uint32
	Size   uint64
}

type drmAMDGPUGemMmap struct {
	AddrPtr uint64
}

type drmPrimeHandle struct {
	Handle uint32
	Flags  uint32
	FD     int32
}

type drmGemClose struct {
	Handle uint32
	Pad    uint32
}

type drmCaptureSource struct {
	source      protocol.DesktopSource
	cardPath    string
	connectorID uint32
	crtcID      uint32
	fbID        uint32
	captureX    int
	captureY    int
	fullScreen  bool
}

type drmKMSCapturer struct {
	file              *os.File
	dmabufFD          int
	data              []byte
	mappedFBID        uint32
	mappedHandle      uint32
	mappedCloseHandle uint32
	mappedPitch       int
	mappedFormat      uint32
	mappedOffset      int
	mappedModifier    uint64
	fb                drmModeFBCmd2
	selected          drmCaptureSource
	bounds            image.Rectangle
}

func enumerateDRMSources() []protocol.DesktopSource {
	captures := enumerateDRMCaptureSources()
	sources := make([]protocol.DesktopSource, 0, len(captures))
	for _, capture := range captures {
		sources = append(sources, capture.source)
	}
	return sources
}

func drmSourceAvailable(source string) bool {
	if strings.HasPrefix(source, "drm:") {
		return true
	}
	if source == "" || source == "auto" || source == "screen:all" || source == "virtual" {
		return len(enumerateDRMCaptureSources()) > 0
	}
	return false
}

func enumerateDRMCaptureSources() []drmCaptureSource {
	paths, _ := filepath.Glob("/dev/dri/card*")
	var sources []drmCaptureSource
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		sources = append(sources, drmCaptureSourcesForCard(file, path)...)
		file.Close()
	}
	return sources
}

func drmCaptureSourcesForCard(file *os.File, path string) []drmCaptureSource {
	resources, connectorIDs, err := drmResources(file)
	if err != nil || resources.CountConnectors == 0 {
		return nil
	}
	card := filepath.Base(path)
	seenScreen := map[uint32]bool{}
	var sources []drmCaptureSource
	for _, connectorID := range connectorIDs {
		connector, encoders, err := drmConnector(file, connectorID)
		if err != nil || connector.Connection != drmConnectorConnected {
			continue
		}
		encoderID := connector.EncoderID
		if encoderID == 0 && len(encoders) > 0 {
			encoderID = encoders[0]
		}
		if encoderID == 0 {
			continue
		}
		encoder, err := drmEncoder(file, encoderID)
		if err != nil || encoder.CRTCID == 0 {
			continue
		}
		crtc, err := drmCRTC(file, encoder.CRTCID)
		if err != nil || crtc.FBID == 0 || crtc.ModeValid == 0 {
			continue
		}
		fb, err := drmFB2(file, crtc.FBID)
		fbWidth := fb.Width
		fbHeight := fb.Height
		fbPitch := fb.Pitches[0]
		if err != nil {
			legacy, legacyErr := drmFB(file, crtc.FBID)
			if legacyErr != nil {
				continue
			}
			_ = drmGemCloseHandle(file, legacy.Handle)
			fbWidth = legacy.Width
			fbHeight = legacy.Height
			fbPitch = legacy.Pitch
		}
		if fbWidth == 0 || fbHeight == 0 || fbPitch == 0 {
			continue
		}
		if !seenScreen[crtc.FBID] {
			seenScreen[crtc.FBID] = true
			sources = append(sources, drmCaptureSource{
				cardPath: path, crtcID: encoder.CRTCID, fbID: crtc.FBID, fullScreen: true,
				source: protocol.DesktopSource{
					ID:      fmt.Sprintf("drm:%s:screen:all", card),
					Label:   "DRM/KMS all screens",
					Kind:    "screen",
					Backend: "drm-kms",
					Width:   int(fbWidth),
					Height:  int(fbHeight),
					Primary: len(sources) == 0,
				},
			})
		}
		width := int(crtc.Mode.HDisplay)
		height := int(crtc.Mode.VDisplay)
		if width <= 0 || height <= 0 {
			width = int(fbWidth)
			height = int(fbHeight)
		}
		label := drmConnectorName(connector.ConnectorType, connector.ConnectorTypeID)
		sources = append(sources, drmCaptureSource{
			cardPath: path, connectorID: connectorID, crtcID: encoder.CRTCID, fbID: crtc.FBID, captureX: int(crtc.X), captureY: int(crtc.Y),
			source: protocol.DesktopSource{
				ID:      fmt.Sprintf("drm:%s:connector:%d", card, connectorID),
				Label:   "DRM/KMS " + label,
				Kind:    "monitor",
				Backend: "drm-kms",
				X:       int(crtc.X),
				Y:       int(crtc.Y),
				Width:   width,
				Height:  height,
				Primary: len(sources) == 0,
			},
		})
	}
	return sources
}

func newDRMCapturer(source string) (desktopCapturer, error) {
	selected, err := resolveDRMCaptureSource(source)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(selected.cardPath, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open DRM card %s: %w", selected.cardPath, err)
	}
	capturer := &drmKMSCapturer{file: file, dmabufFD: -1, selected: selected}
	if err := capturer.refresh(); err != nil {
		capturer.Close()
		return nil, err
	}
	return capturer, nil
}

func resolveDRMCaptureSource(source string) (drmCaptureSource, error) {
	sources := enumerateDRMCaptureSources()
	if len(sources) == 0 {
		return drmCaptureSource{}, fmt.Errorf("no readable DRM/KMS card with active scanout found")
	}
	if source == "" || source == "auto" || source == "screen:all" || source == "virtual" {
		return sources[0], nil
	}
	for _, candidate := range sources {
		if candidate.source.ID == source {
			return candidate, nil
		}
	}
	return drmCaptureSource{}, fmt.Errorf("DRM/KMS source %q not found", source)
}

func (c *drmKMSCapturer) Bounds() image.Rectangle { return c.bounds }

func (c *drmKMSCapturer) Source() protocol.DesktopSource { return c.selected.source }

func (c *drmKMSCapturer) Close() error {
	c.unmap()
	if c.file != nil {
		return c.file.Close()
	}
	return nil
}

func (c *drmKMSCapturer) Capture() (image.Image, error) {
	if err := c.refresh(); err != nil {
		return nil, err
	}
	width := c.bounds.Dx()
	height := c.bounds.Dy()
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("invalid DRM/KMS capture bounds")
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	bytesPerPixel := drmBytesPerPixel(c.mappedFormat)
	if bytesPerPixel <= 0 {
		return nil, fmt.Errorf("unsupported DRM/KMS pixel format %s", drmFourCC(c.mappedFormat))
	}
	parallelDesktopRows(width, height, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			dst := img.Pix[y*img.Stride:]
			for x := 0; x < width; x++ {
				off := c.pixelOffset(c.selected.captureX+x, c.selected.captureY+y, bytesPerPixel)
				if off+bytesPerPixel > len(c.data) {
					break
				}
				pixel := drmPixel(c.mappedFormat, c.data[off:off+bytesPerPixel])
				d := x * 4
				dst[d+0] = pixel.R
				dst[d+1] = pixel.G
				dst[d+2] = pixel.B
				dst[d+3] = pixel.A
			}
		}
	})
	return img, nil
}

func (c *drmKMSCapturer) pixelOffset(x, y, bytesPerPixel int) int {
	if c.mappedModifier == i915FormatModXTiled {
		byteX := x * bytesPerPixel
		tileX := byteX / 512
		innerX := byteX % 512
		tileY := y / 8
		innerY := y % 8
		return c.mappedOffset + tileY*c.mappedPitch*8 + tileX*4096 + innerY*512 + innerX
	}
	return c.mappedOffset + y*c.mappedPitch + x*bytesPerPixel
}

func (c *drmKMSCapturer) refresh() error {
	crtc, err := drmCRTC(c.file, c.selected.crtcID)
	if err != nil {
		return fmt.Errorf("read DRM/KMS CRTC %d: %w", c.selected.crtcID, err)
	}
	if crtc.FBID == 0 {
		return fmt.Errorf("DRM/KMS CRTC %d has no active framebuffer", c.selected.crtcID)
	}
	fb, err := drmFB2(c.file, crtc.FBID)
	fbWidth := fb.Width
	fbHeight := fb.Height
	fbFormat := fb.PixelFormat
	legacyFB := false
	if err != nil {
		legacy, legacyErr := drmFB(c.file, crtc.FBID)
		if legacyErr != nil {
			return fmt.Errorf("read DRM/KMS framebuffer %d: GETFB2 failed: %w; GETFB failed: %v", crtc.FBID, err, legacyErr)
		}
		format, ok := drmLegacyFormat(legacy.BPP, legacy.Depth)
		if !ok {
			_ = drmGemCloseHandle(c.file, legacy.Handle)
			return fmt.Errorf("unsupported DRM/KMS legacy framebuffer %d format bpp=%d depth=%d", crtc.FBID, legacy.BPP, legacy.Depth)
		}
		fb = drmModeFBCmd2{FBID: legacy.FBID, Width: legacy.Width, Height: legacy.Height, PixelFormat: format, Handles: [4]uint32{legacy.Handle}, Pitches: [4]uint32{legacy.Pitch}}
		fbWidth = legacy.Width
		fbHeight = legacy.Height
		fbFormat = format
		legacyFB = true
	}
	if fbWidth == 0 || fbHeight == 0 || fb.Pitches[0] == 0 {
		_ = drmGemCloseHandle(c.file, fb.Handles[0])
		return fmt.Errorf("invalid DRM/KMS framebuffer %d layout", crtc.FBID)
	}
	if drmBytesPerPixel(fbFormat) <= 0 {
		_ = drmGemCloseHandle(c.file, fb.Handles[0])
		return fmt.Errorf("unsupported DRM/KMS pixel format %s", drmFourCC(fbFormat))
	}
	if c.mappedFBID != crtc.FBID {
		c.unmap()
		if legacyFB {
			if err := c.mapFBLegacy(crtc.FBID, fmt.Errorf("DRM/KMS GETFB2 unavailable")); err != nil {
				_ = drmGemCloseHandle(c.file, fb.Handles[0])
				return err
			}
		} else if err := c.mapFB(fb); err != nil {
			return err
		}
		c.mappedFBID = crtc.FBID
	} else {
		_ = drmGemCloseHandle(c.file, fb.Handles[0])
	}
	c.fb = fb
	if c.selected.fullScreen {
		c.selected.captureX = 0
		c.selected.captureY = 0
		c.bounds = image.Rect(0, 0, int(fbWidth), int(fbHeight))
		c.selected.source.Width = int(fbWidth)
		c.selected.source.Height = int(fbHeight)
	} else {
		width := int(crtc.Mode.HDisplay)
		height := int(crtc.Mode.VDisplay)
		if width <= 0 || height <= 0 {
			width = int(fbWidth)
			height = int(fbHeight)
		}
		c.selected.captureX = int(crtc.X)
		c.selected.captureY = int(crtc.Y)
		c.bounds = image.Rect(int(crtc.X), int(crtc.Y), int(crtc.X)+width, int(crtc.Y)+height)
		c.selected.source.X = int(crtc.X)
		c.selected.source.Y = int(crtc.Y)
		c.selected.source.Width = width
		c.selected.source.Height = height
	}
	return nil
}

func (c *drmKMSCapturer) mapFB(fb drmModeFBCmd2) error {
	modifier := uint64(drmFormatModLinear)
	if (fb.Flags & drmModeFBModifiers) != 0 {
		modifier = fb.Modifiers[0]
	}
	if modifier != drmFormatModLinear && !drmModifierMappable(modifier) {
		_ = drmGemCloseHandle(c.file, fb.Handles[0])
		return c.mapFBLegacy(fb.FBID, fmt.Errorf("DRM/KMS framebuffer %d uses unsupported modifier 0x%x", fb.FBID, modifier))
	}
	if fb.Handles[0] == 0 {
		return c.mapFBLegacy(fb.FBID, fmt.Errorf("DRM/KMS framebuffer %d did not expose a GEM handle", fb.FBID))
	}
	mapLen := drmMapLen(int(fb.Offsets[0]), int(fb.Pitches[0]), int(fb.Height), modifier)
	if err := c.mapDumbHandle(fb.Handles[0], mapLen); err == nil {
		c.mappedHandle = fb.Handles[0]
		c.mappedPitch = int(fb.Pitches[0])
		c.mappedFormat = fb.PixelFormat
		c.mappedOffset = int(fb.Offsets[0])
		c.mappedModifier = modifier
		return nil
	}
	if err := c.mapAMDGPUHandle(fb.Handles[0], mapLen); err == nil {
		c.mappedHandle = fb.Handles[0]
		c.mappedPitch = int(fb.Pitches[0])
		c.mappedFormat = fb.PixelFormat
		c.mappedOffset = int(fb.Offsets[0])
		c.mappedModifier = modifier
		return nil
	}
	prime := drmPrimeHandle{Handle: fb.Handles[0], Flags: drmPrimeHandleToFDFlags, FD: -1}
	if err := drmIoctl(c.file, drmIOWR(0x2d, unsafe.Sizeof(prime)), unsafe.Pointer(&prime)); err != nil {
		_ = drmGemCloseHandle(c.file, fb.Handles[0])
		return fmt.Errorf("export DRM/KMS framebuffer %d as dma-buf: %w", fb.FBID, err)
	}
	_ = drmGemCloseHandle(c.file, fb.Handles[0])
	data, err := syscall.Mmap(int(prime.FD), 0, mapLen, syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		_ = syscall.Close(int(prime.FD))
		return fmt.Errorf("mmap DRM/KMS dma-buf for framebuffer %d: %w", fb.FBID, err)
	}
	c.dmabufFD = int(prime.FD)
	c.data = data
	c.mappedHandle = fb.Handles[0]
	c.mappedPitch = int(fb.Pitches[0])
	c.mappedFormat = fb.PixelFormat
	c.mappedOffset = int(fb.Offsets[0])
	c.mappedModifier = modifier
	return nil
}

func (c *drmKMSCapturer) mapFBLegacy(fbID uint32, cause error) error {
	fb, err := drmFB(c.file, fbID)
	if err != nil {
		return fmt.Errorf("%v; fallback GETFB failed: %w", cause, err)
	}
	if fb.Handle == 0 || fb.Pitch == 0 || fb.Width == 0 || fb.Height == 0 {
		return fmt.Errorf("%v; fallback GETFB returned invalid layout", cause)
	}
	format, ok := drmLegacyFormat(fb.BPP, fb.Depth)
	if !ok {
		_ = drmGemCloseHandle(c.file, fb.Handle)
		return fmt.Errorf("%v; unsupported fallback GETFB format bpp=%d depth=%d", cause, fb.BPP, fb.Depth)
	}
	mapLen := int(fb.Pitch) * int(fb.Height)
	if err := c.mapDumbHandle(fb.Handle, mapLen); err == nil {
		c.mappedHandle = fb.Handle
		c.mappedPitch = int(fb.Pitch)
		c.mappedFormat = format
		c.mappedOffset = 0
		c.mappedModifier = drmFormatModLinear
		return nil
	}
	if err := c.mapAMDGPUHandle(fb.Handle, mapLen); err == nil {
		c.mappedHandle = fb.Handle
		c.mappedPitch = int(fb.Pitch)
		c.mappedFormat = format
		c.mappedOffset = 0
		c.mappedModifier = drmFormatModLinear
		return nil
	}
	prime := drmPrimeHandle{Handle: fb.Handle, Flags: drmPrimeHandleToFDFlags, FD: -1}
	if err := drmIoctl(c.file, drmIOWR(0x2d, unsafe.Sizeof(prime)), unsafe.Pointer(&prime)); err != nil {
		_ = drmGemCloseHandle(c.file, fb.Handle)
		return fmt.Errorf("%v; fallback PRIME export failed: %w", cause, err)
	}
	_ = drmGemCloseHandle(c.file, fb.Handle)
	data, err := syscall.Mmap(int(prime.FD), 0, mapLen, syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		_ = syscall.Close(int(prime.FD))
		return fmt.Errorf("%v; fallback PRIME mmap failed: %w", cause, err)
	}
	c.dmabufFD = int(prime.FD)
	c.data = data
	c.mappedHandle = fb.Handle
	c.mappedPitch = int(fb.Pitch)
	c.mappedFormat = format
	c.mappedOffset = 0
	c.mappedModifier = drmFormatModLinear
	return nil
}

func drmModifierMappable(modifier uint64) bool {
	return modifier == drmFormatModLinear || modifier == i915FormatModXTiled
}

func drmMapLen(offset, pitch, height int, modifier uint64) int {
	if modifier == i915FormatModXTiled {
		return offset + ((height+7)/8)*pitch*8
	}
	return offset + pitch*height
}

func (c *drmKMSCapturer) mapDumbHandle(handle uint32, mapLen int) error {
	if handle == 0 || mapLen <= 0 {
		return fmt.Errorf("invalid GEM handle or map length")
	}
	opened, err := drmReopenGEMHandle(c.file, handle)
	if err != nil {
		return err
	}
	mapping := drmModeMapDumb{Handle: opened.Handle}
	if err := drmIoctl(c.file, drmIOWR(0xB3, unsafe.Sizeof(mapping)), unsafe.Pointer(&mapping)); err != nil {
		_ = drmGemCloseHandle(c.file, opened.Handle)
		return fmt.Errorf("DRM_IOCTL_MODE_MAP_DUMB: %w", err)
	}
	if opened.Size > 0 && int(opened.Size) > mapLen {
		mapLen = int(opened.Size)
	}
	data, err := syscall.Mmap(int(c.file.Fd()), int64(mapping.Offset), mapLen, syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		_ = drmGemCloseHandle(c.file, opened.Handle)
		return fmt.Errorf("mmap DRM/KMS dumb buffer: %w", err)
	}
	c.data = data
	c.dmabufFD = -1
	c.mappedCloseHandle = opened.Handle
	if opened.Handle != handle {
		_ = drmGemCloseHandle(c.file, handle)
	}
	return nil
}

func (c *drmKMSCapturer) mapAMDGPUHandle(handle uint32, mapLen int) error {
	if handle == 0 || mapLen <= 0 {
		return fmt.Errorf("invalid AMDGPU GEM handle or map length")
	}
	req := drmAMDGPUGemMmap{AddrPtr: uint64(handle)}
	if err := drmIoctl(c.file, drmIOWR(0x41, unsafe.Sizeof(req)), unsafe.Pointer(&req)); err != nil {
		return fmt.Errorf("DRM_IOCTL_AMDGPU_GEM_MMAP: %w", err)
	}
	data, err := syscall.Mmap(int(c.file.Fd()), int64(req.AddrPtr), mapLen, syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return fmt.Errorf("mmap AMDGPU framebuffer: %w", err)
	}
	c.data = data
	c.dmabufFD = -1
	c.mappedCloseHandle = handle
	return nil
}

func (c *drmKMSCapturer) unmap() {
	if c.data != nil {
		_ = syscall.Munmap(c.data)
		c.data = nil
	}
	if c.dmabufFD >= 0 {
		_ = syscall.Close(c.dmabufFD)
		c.dmabufFD = -1
	}
	if c.mappedCloseHandle != 0 {
		_ = drmGemCloseHandle(c.file, c.mappedCloseHandle)
		c.mappedCloseHandle = 0
	}
	c.mappedFBID = 0
	c.mappedHandle = 0
	c.mappedPitch = 0
	c.mappedFormat = 0
}

func drmResources(file *os.File) (drmModeCardRes, []uint32, error) {
	var res drmModeCardRes
	if err := drmIoctl(file, drmIOWR(0xA0, unsafe.Sizeof(res)), unsafe.Pointer(&res)); err != nil {
		return drmModeCardRes{}, nil, err
	}
	connectors := make([]uint32, res.CountConnectors)
	crtcs := make([]uint32, res.CountCRTCs)
	encoders := make([]uint32, res.CountEncoders)
	fbs := make([]uint32, res.CountFBs)
	if len(connectors) > 0 {
		res.ConnectorIDPtr = uint64(uintptr(unsafe.Pointer(&connectors[0])))
	}
	if len(crtcs) > 0 {
		res.CRTCIDPtr = uint64(uintptr(unsafe.Pointer(&crtcs[0])))
	}
	if len(encoders) > 0 {
		res.EncoderIDPtr = uint64(uintptr(unsafe.Pointer(&encoders[0])))
	}
	if len(fbs) > 0 {
		res.FBIDPtr = uint64(uintptr(unsafe.Pointer(&fbs[0])))
	}
	if err := drmIoctl(file, drmIOWR(0xA0, unsafe.Sizeof(res)), unsafe.Pointer(&res)); err != nil {
		return drmModeCardRes{}, nil, err
	}
	return res, connectors, nil
}

func drmConnector(file *os.File, connectorID uint32) (drmModeGetConnector, []uint32, error) {
	conn := drmModeGetConnector{ConnectorID: connectorID}
	if err := drmIoctl(file, drmIOWR(0xA7, unsafe.Sizeof(conn)), unsafe.Pointer(&conn)); err != nil {
		return drmModeGetConnector{}, nil, err
	}
	encoders := make([]uint32, conn.CountEncoders)
	modes := make([]drmModeModeInfo, conn.CountModes)
	props := make([]uint32, conn.CountProps)
	propValues := make([]uint64, conn.CountProps)
	if len(encoders) > 0 {
		conn.EncodersPtr = uint64(uintptr(unsafe.Pointer(&encoders[0])))
	}
	if len(modes) > 0 {
		conn.ModesPtr = uint64(uintptr(unsafe.Pointer(&modes[0])))
	}
	if len(props) > 0 {
		conn.PropsPtr = uint64(uintptr(unsafe.Pointer(&props[0])))
	}
	if len(propValues) > 0 {
		conn.PropValuesPtr = uint64(uintptr(unsafe.Pointer(&propValues[0])))
	}
	if err := drmIoctl(file, drmIOWR(0xA7, unsafe.Sizeof(conn)), unsafe.Pointer(&conn)); err != nil {
		return drmModeGetConnector{}, nil, err
	}
	return conn, encoders, nil
}

func drmEncoder(file *os.File, encoderID uint32) (drmModeGetEncoder, error) {
	encoder := drmModeGetEncoder{EncoderID: encoderID}
	if err := drmIoctl(file, drmIOWR(0xA6, unsafe.Sizeof(encoder)), unsafe.Pointer(&encoder)); err != nil {
		return drmModeGetEncoder{}, err
	}
	return encoder, nil
}

func drmCRTC(file *os.File, crtcID uint32) (drmModeCRTC, error) {
	crtc := drmModeCRTC{CRTCID: crtcID}
	if err := drmIoctl(file, drmIOWR(0xA1, unsafe.Sizeof(crtc)), unsafe.Pointer(&crtc)); err != nil {
		return drmModeCRTC{}, err
	}
	return crtc, nil
}

func drmFB2(file *os.File, fbID uint32) (drmModeFBCmd2, error) {
	fb := drmModeFBCmd2{FBID: fbID}
	if err := drmIoctl(file, drmIOWR(0xCE, unsafe.Sizeof(fb)), unsafe.Pointer(&fb)); err != nil {
		return drmModeFBCmd2{}, err
	}
	return fb, nil
}

func drmReopenGEMHandle(file *os.File, handle uint32) (drmGemOpen, error) {
	flink := drmGemFlink{Handle: handle}
	if err := drmIoctl(file, drmIOWR(0x0a, unsafe.Sizeof(flink)), unsafe.Pointer(&flink)); err != nil {
		return drmGemOpen{}, fmt.Errorf("DRM_IOCTL_GEM_FLINK: %w", err)
	}
	opened := drmGemOpen{Name: flink.Name}
	if err := drmIoctl(file, drmIOWR(0x0b, unsafe.Sizeof(opened)), unsafe.Pointer(&opened)); err != nil {
		return drmGemOpen{}, fmt.Errorf("DRM_IOCTL_GEM_OPEN: %w", err)
	}
	return opened, nil
}

func drmFB(file *os.File, fbID uint32) (drmModeFBCmd, error) {
	fb := drmModeFBCmd{FBID: fbID}
	if err := drmIoctl(file, drmIOWR(0xAD, unsafe.Sizeof(fb)), unsafe.Pointer(&fb)); err != nil {
		return drmModeFBCmd{}, err
	}
	return fb, nil
}

func drmGemCloseHandle(file *os.File, handle uint32) error {
	if handle == 0 {
		return nil
	}
	closeReq := drmGemClose{Handle: handle}
	return drmIoctl(file, drmIOW(0x09, unsafe.Sizeof(closeReq)), unsafe.Pointer(&closeReq))
}

func drmIoctl(file *os.File, req uintptr, ptr unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), req, uintptr(ptr))
	if errno != 0 {
		return errno
	}
	return nil
}

func drmIOW(nr uintptr, size uintptr) uintptr {
	return ioctl(1, uintptr('d'), nr, size)
}

func drmIOWR(nr uintptr, size uintptr) uintptr {
	return ioctl(3, uintptr('d'), nr, size)
}

func ioctl(dir, typ, nr, size uintptr) uintptr {
	return (dir << 30) | (typ << 8) | nr | (size << 16)
}

func drmLegacyFormat(bpp, depth uint32) (uint32, bool) {
	switch {
	case bpp == 32 && depth == 24:
		return drmFormatXRGB8888, true
	case bpp == 32 && depth == 32:
		return drmFormatARGB8888, true
	case bpp == 16 && depth == 16:
		return drmFormatRGB565, true
	default:
		return 0, false
	}
}

func drmBytesPerPixel(format uint32) int {
	switch format {
	case drmFormatXRGB8888, drmFormatXBGR8888, drmFormatRGBX8888, drmFormatBGRX8888, drmFormatARGB8888, drmFormatABGR8888, drmFormatRGBA8888, drmFormatBGRA8888:
		return 4
	case drmFormatRGB565:
		return 2
	default:
		return 0
	}
}

func drmPixel(format uint32, data []byte) color.RGBA {
	switch format {
	case drmFormatXRGB8888, drmFormatARGB8888:
		return color.RGBA{R: data[2], G: data[1], B: data[0], A: 255}
	case drmFormatXBGR8888, drmFormatABGR8888, drmFormatRGBX8888, drmFormatRGBA8888:
		return color.RGBA{R: data[0], G: data[1], B: data[2], A: 255}
	case drmFormatBGRX8888, drmFormatBGRA8888:
		return color.RGBA{R: data[2], G: data[1], B: data[0], A: 255}
	case drmFormatRGB565:
		value := binary.LittleEndian.Uint16(data)
		return color.RGBA{R: uint8(((uint32(value) >> 11) & 0x1f) * 255 / 31), G: uint8(((uint32(value) >> 5) & 0x3f) * 255 / 63), B: uint8((uint32(value) & 0x1f) * 255 / 31), A: 255}
	default:
		return color.RGBA{A: 255}
	}
}

func drmFourCC(format uint32) string {
	b := []byte{byte(format), byte(format >> 8), byte(format >> 16), byte(format >> 24)}
	for i, ch := range b {
		if ch < 32 || ch > 126 {
			b[i] = '?'
		}
	}
	return string(b)
}

func drmConnectorName(connectorType, connectorTypeID uint32) string {
	names := map[uint32]string{
		1:  "VGA",
		2:  "DVI-I",
		3:  "DVI-D",
		4:  "DVI-A",
		5:  "Composite",
		6:  "SVIDEO",
		7:  "LVDS",
		8:  "Component",
		9:  "DIN",
		10: "DP",
		11: "HDMI-A",
		12: "HDMI-B",
		13: "TV",
		14: "eDP",
		15: "Virtual",
		16: "DSI",
		17: "DPI",
		18: "Writeback",
		19: "SPI",
		20: "USB",
	}
	name := names[connectorType]
	if name == "" {
		name = "Connector"
	}
	return name + "-" + strconv.FormatUint(uint64(connectorTypeID), 10)
}

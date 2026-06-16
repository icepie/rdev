//go:build linux

package client

import (
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"math/bits"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"github.com/BurntSushi/xgb"
	"github.com/BurntSushi/xgb/randr"
	"github.com/BurntSushi/xgb/xproto"
	"rdev/internal/protocol"
)

type x11CaptureSource struct {
	source   protocol.DesktopSource
	drawable xproto.Drawable
	bounds   image.Rectangle
	captureX int16
	captureY int16
}

type x11DesktopCapturer struct {
	conn        *xgb.Conn
	screen      *xproto.ScreenInfo
	visual      xproto.VisualInfo
	format      xproto.Format
	source      protocol.DesktopSource
	drawable    xproto.Drawable
	bounds      image.Rectangle
	captureX    int16
	captureY    int16
	byteOrder   binary.ByteOrder
	redShift    uint
	greenShift  uint
	blueShift   uint
	redMax      uint32
	greenMax    uint32
	blueMax     uint32
	bytesPerPix int
}

func desktopSources() []protocol.DesktopSource {
	fbSources := enumerateFBDevSources()
	drmSources := enumerateDRMSources()
	conn, env, err := linuxX11ConnectAny()
	if err != nil {
		if len(drmSources) > 0 {
			return append([]protocol.DesktopSource{{ID: "auto", Label: "Auto", Kind: "screen", Backend: "drm-kms", Width: drmSources[0].Width, Height: drmSources[0].Height, Primary: true}}, append(drmSources, fbSources...)...)
		}
		if len(fbSources) > 0 {
			return append([]protocol.DesktopSource{{ID: "auto", Label: "Auto", Kind: "screen", Backend: "fbdev", Width: fbSources[0].Width, Height: fbSources[0].Height, Primary: true}}, fbSources...)
		}
		return []protocol.DesktopSource{{ID: "auto", Label: "Auto", Kind: "screen", Backend: "x11", Primary: true}}
	}
	defer conn.Close()
	setup := xproto.Setup(conn)
	screen := setup.DefaultScreen(conn)
	label := "All screens"
	if env.Display != "" {
		label += " (" + env.Display + ")"
	}
	sources := []protocol.DesktopSource{
		{ID: "auto", Label: "Auto", Kind: "screen", Backend: "x11", Width: int(screen.WidthInPixels), Height: int(screen.HeightInPixels), Primary: true},
		{ID: "screen:all", Label: label, Kind: "screen", Backend: "x11", Width: int(screen.WidthInPixels), Height: int(screen.HeightInPixels), Primary: true},
	}
	for _, monitor := range enumerateX11Monitors(conn, screen) {
		sources = append(sources, monitor.source)
	}
	for _, window := range enumerateX11Windows(conn, screen, 80) {
		sources = append(sources, window.source)
	}
	sources = append(sources, drmSources...)
	sources = append(sources, fbSources...)
	return sources
}

func newDesktopCapturer(source string) (desktopCapturer, error) {
	if strings.HasPrefix(source, "drm:") || (os.Getenv("DISPLAY") == "" && drmSourceAvailable(source)) {
		return newDRMCapturer(source)
	}
	if strings.HasPrefix(source, "fbdev:") || (os.Getenv("DISPLAY") == "" && fbdevPath(source) != "") {
		return newFBDevCapturer(source)
	}
	conn, _, err := linuxX11ConnectAny()
	if err != nil {
		if source == "" || source == "auto" || source == "screen:all" || source == "virtual" {
			if drmSourceAvailable(source) {
				return newDRMCapturer(source)
			}
			if fbdevPath(source) != "" {
				return newFBDevCapturer(source)
			}
		}
		return nil, err
	}
	setup := xproto.Setup(conn)
	screen := setup.DefaultScreen(conn)
	visual, ok := findVisual(setup, screen.RootVisual)
	if !ok {
		conn.Close()
		return nil, fmt.Errorf("X11 root visual not found")
	}
	format, ok := findPixmapFormat(setup, screen.RootDepth)
	if !ok {
		conn.Close()
		return nil, fmt.Errorf("X11 pixmap format for depth %d not found", screen.RootDepth)
	}
	if format.BitsPerPixel != 16 && format.BitsPerPixel != 24 && format.BitsPerPixel != 32 {
		conn.Close()
		return nil, fmt.Errorf("unsupported X11 bits-per-pixel %d", format.BitsPerPixel)
	}
	selected, err := resolveX11CaptureSource(conn, screen, source)
	if err != nil {
		conn.Close()
		return nil, err
	}
	var byteOrder binary.ByteOrder = binary.LittleEndian
	if setup.ImageByteOrder == xproto.ImageOrderMSBFirst {
		byteOrder = binary.BigEndian
	}
	return &x11DesktopCapturer{
		conn:        conn,
		screen:      screen,
		visual:      visual,
		format:      format,
		source:      selected.source,
		drawable:    selected.drawable,
		bounds:      selected.bounds,
		captureX:    selected.captureX,
		captureY:    selected.captureY,
		byteOrder:   byteOrder,
		redShift:    uint(bits.TrailingZeros32(visual.RedMask)),
		greenShift:  uint(bits.TrailingZeros32(visual.GreenMask)),
		blueShift:   uint(bits.TrailingZeros32(visual.BlueMask)),
		redMax:      visual.RedMask >> uint(bits.TrailingZeros32(visual.RedMask)),
		greenMax:    visual.GreenMask >> uint(bits.TrailingZeros32(visual.GreenMask)),
		blueMax:     visual.BlueMask >> uint(bits.TrailingZeros32(visual.BlueMask)),
		bytesPerPix: int(format.BitsPerPixel / 8),
	}, nil
}

func resolveX11CaptureSource(conn *xgb.Conn, screen *xproto.ScreenInfo, source string) (x11CaptureSource, error) {
	root := xproto.Drawable(screen.Root)
	rootSource := protocol.DesktopSource{ID: "screen:all", Label: "All screens", Kind: "screen", Backend: "x11", Width: int(screen.WidthInPixels), Height: int(screen.HeightInPixels), Primary: true}
	if source == "" || source == "auto" || source == "virtual" || source == "screen:root" || source == "screen:all" {
		return x11CaptureSource{source: rootSource, drawable: root, bounds: image.Rect(0, 0, int(screen.WidthInPixels), int(screen.HeightInPixels))}, nil
	}
	if strings.HasPrefix(source, "monitor:") {
		for _, monitor := range enumerateX11Monitors(conn, screen) {
			if monitor.source.ID == source {
				return monitor, nil
			}
		}
		return x11CaptureSource{}, fmt.Errorf("monitor source %q not found", source)
	}
	if strings.HasPrefix(source, "window:") {
		id, err := strconv.ParseUint(strings.TrimPrefix(source, "window:"), 10, 32)
		if err != nil || id == 0 {
			return x11CaptureSource{}, fmt.Errorf("invalid window source %q", source)
		}
		window, err := x11WindowSource(conn, screen, xproto.Window(id))
		if err != nil {
			return x11CaptureSource{}, err
		}
		return window, nil
	}
	return x11CaptureSource{}, fmt.Errorf("unsupported desktop source %q", source)
}

func enumerateX11Monitors(conn *xgb.Conn, screen *xproto.ScreenInfo) []x11CaptureSource {
	if err := randr.Init(conn); err != nil {
		return nil
	}
	resources, err := randr.GetScreenResourcesCurrent(conn, screen.Root).Reply()
	if err != nil || resources == nil {
		return nil
	}
	primary := randr.Output(0)
	if reply, err := randr.GetOutputPrimary(conn, screen.Root).Reply(); err == nil && reply != nil {
		primary = reply.Output
	}
	var monitors []x11CaptureSource
	for _, output := range resources.Outputs {
		info, err := randr.GetOutputInfo(conn, output, resources.ConfigTimestamp).Reply()
		if err != nil || info == nil || info.Connection != randr.ConnectionConnected || info.Crtc == 0 {
			continue
		}
		crtc, err := randr.GetCrtcInfo(conn, info.Crtc, resources.ConfigTimestamp).Reply()
		if err != nil || crtc == nil || crtc.Width == 0 || crtc.Height == 0 {
			continue
		}
		name := strings.TrimSpace(string(info.Name))
		if name == "" {
			name = fmt.Sprintf("Output %d", output)
		}
		bounds := image.Rect(int(crtc.X), int(crtc.Y), int(crtc.X)+int(crtc.Width), int(crtc.Y)+int(crtc.Height))
		source := protocol.DesktopSource{
			ID:      fmt.Sprintf("monitor:%d", output),
			Label:   name,
			Kind:    "monitor",
			Backend: "x11-randr",
			X:       bounds.Min.X,
			Y:       bounds.Min.Y,
			Width:   bounds.Dx(),
			Height:  bounds.Dy(),
			Primary: output == primary,
		}
		monitors = append(monitors, x11CaptureSource{source: source, drawable: xproto.Drawable(screen.Root), bounds: bounds, captureX: int16(bounds.Min.X), captureY: int16(bounds.Min.Y)})
	}
	return monitors
}

func enumerateX11Windows(conn *xgb.Conn, screen *xproto.ScreenInfo, limit int) []x11CaptureSource {
	listAtom, err := internAtom(conn, "_NET_CLIENT_LIST")
	if err != nil {
		return nil
	}
	reply, err := xproto.GetProperty(conn, false, screen.Root, listAtom, xproto.AtomWindow, 0, uint32(limit)).Reply()
	if err != nil || reply == nil || reply.Format != 32 {
		return nil
	}
	var windows []x11CaptureSource
	for offset := 0; offset+4 <= len(reply.Value) && len(windows) < limit; offset += 4 {
		win := xproto.Window(xgb.Get32(reply.Value[offset:]))
		window, err := x11WindowSource(conn, screen, win)
		if err != nil || window.source.Label == "" || window.bounds.Dx() < 80 || window.bounds.Dy() < 60 {
			continue
		}
		windows = append(windows, window)
	}
	return windows
}

func x11WindowSource(conn *xgb.Conn, screen *xproto.ScreenInfo, window xproto.Window) (x11CaptureSource, error) {
	attrs, err := xproto.GetWindowAttributes(conn, window).Reply()
	if err != nil || attrs == nil || attrs.MapState != xproto.MapStateViewable {
		return x11CaptureSource{}, fmt.Errorf("window is not viewable")
	}
	geom, err := xproto.GetGeometry(conn, xproto.Drawable(window)).Reply()
	if err != nil || geom == nil || geom.Width == 0 || geom.Height == 0 {
		return x11CaptureSource{}, fmt.Errorf("window geometry unavailable")
	}
	translated, err := xproto.TranslateCoordinates(conn, window, screen.Root, 0, 0).Reply()
	if err != nil || translated == nil {
		return x11CaptureSource{}, fmt.Errorf("window coordinates unavailable")
	}
	title := x11WindowTitle(conn, window)
	if title == "" {
		return x11CaptureSource{}, fmt.Errorf("window title unavailable")
	}
	bounds := image.Rect(int(translated.DstX), int(translated.DstY), int(translated.DstX)+int(geom.Width), int(translated.DstY)+int(geom.Height))
	source := protocol.DesktopSource{
		ID:      fmt.Sprintf("window:%d", uint32(window)),
		Label:   title,
		Kind:    "window",
		Backend: "x11",
		X:       bounds.Min.X,
		Y:       bounds.Min.Y,
		Width:   bounds.Dx(),
		Height:  bounds.Dy(),
	}
	return x11CaptureSource{source: source, drawable: xproto.Drawable(window), bounds: bounds}, nil
}

func x11WindowTitle(conn *xgb.Conn, window xproto.Window) string {
	if atom, err := internAtom(conn, "_NET_WM_NAME"); err == nil {
		if title := x11StringProperty(conn, window, atom); title != "" {
			return title
		}
	}
	return x11StringProperty(conn, window, xproto.AtomWmName)
}

func x11StringProperty(conn *xgb.Conn, window xproto.Window, atom xproto.Atom) string {
	reply, err := xproto.GetProperty(conn, false, window, atom, xproto.AtomAny, 0, 1024).Reply()
	if err != nil || reply == nil || reply.Format != 8 || len(reply.Value) == 0 {
		return ""
	}
	return strings.TrimSpace(string(reply.Value))
}

func internAtom(conn *xgb.Conn, name string) (xproto.Atom, error) {
	reply, err := xproto.InternAtom(conn, false, uint16(len(name)), name).Reply()
	if err != nil {
		return 0, err
	}
	return reply.Atom, nil
}

func (c *x11DesktopCapturer) Bounds() image.Rectangle { return c.bounds }

func (c *x11DesktopCapturer) Source() protocol.DesktopSource { return c.source }

func (c *x11DesktopCapturer) CursorPosition() (image.Point, bool) {
	reply, err := xproto.QueryPointer(c.conn, c.screen.Root).Reply()
	if err != nil || reply == nil {
		return image.Point{}, false
	}
	return image.Pt(int(reply.RootX), int(reply.RootY)), true
}

func (c *x11DesktopCapturer) Close() error {
	c.conn.Close()
	return nil
}

func (c *x11DesktopCapturer) Capture() (image.Image, error) {
	width := c.bounds.Dx()
	height := c.bounds.Dy()
	reply, err := xproto.GetImage(c.conn, xproto.ImageFormatZPixmap, c.drawable, c.captureX, c.captureY, uint16(width), uint16(height), ^uint32(0)).Reply()
	if err != nil {
		return nil, fmt.Errorf("X11 get image: %w", err)
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	stride := scanlineStride(width, int(c.format.BitsPerPixel), int(c.format.ScanlinePad))
	for y := 0; y < height; y++ {
		row := y * stride
		for x := 0; x < width; x++ {
			off := row + x*c.bytesPerPix
			if off+c.bytesPerPix > len(reply.Data) {
				break
			}
			pixel := c.pixel(reply.Data[off : off+c.bytesPerPix])
			r := scaleColor((pixel&c.visual.RedMask)>>c.redShift, c.redMax)
			g := scaleColor((pixel&c.visual.GreenMask)>>c.greenShift, c.greenMax)
			b := scaleColor((pixel&c.visual.BlueMask)>>c.blueShift, c.blueMax)
			img.SetRGBA(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
		}
	}
	return img, nil
}

func (c *x11DesktopCapturer) pixel(data []byte) uint32 {
	switch c.bytesPerPix {
	case 4:
		return c.byteOrder.Uint32(data)
	case 3:
		if c.byteOrder == binary.LittleEndian {
			return uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16
		}
		return uint32(data[2]) | uint32(data[1])<<8 | uint32(data[0])<<16
	case 2:
		return uint32(c.byteOrder.Uint16(data))
	default:
		return 0
	}
}

func scanlineStride(width, bitsPerPixel, scanlinePad int) int {
	bitsPerLine := width * bitsPerPixel
	paddedBits := ((bitsPerLine + scanlinePad - 1) / scanlinePad) * scanlinePad
	return paddedBits / 8
}

func scaleColor(value, max uint32) uint8 {
	if max == 0 {
		return 0
	}
	return uint8((value * 255) / max)
}

func findPixmapFormat(setup *xproto.SetupInfo, depth byte) (xproto.Format, bool) {
	for _, format := range setup.PixmapFormats {
		if format.Depth == depth {
			return format, true
		}
	}
	return xproto.Format{}, false
}

func findVisual(setup *xproto.SetupInfo, visualID xproto.Visualid) (xproto.VisualInfo, bool) {
	for _, screen := range setup.Roots {
		for _, depth := range screen.AllowedDepths {
			for _, visual := range depth.Visuals {
				if visual.VisualId == visualID {
					return visual, true
				}
			}
		}
	}
	return xproto.VisualInfo{}, false
}

const (
	fbioGetVScreenInfo = 0x4600
	fbioGetFScreenInfo = 0x4602
)

type fbBitfield struct {
	Offset   uint32
	Length   uint32
	MsbRight uint32
}

type fbVarScreenInfo struct {
	Xres         uint32
	Yres         uint32
	XresVirtual  uint32
	YresVirtual  uint32
	Xoffset      uint32
	Yoffset      uint32
	BitsPerPixel uint32
	Grayscale    uint32
	Red          fbBitfield
	Green        fbBitfield
	Blue         fbBitfield
	Transp       fbBitfield
	Nonstd       uint32
	Activate     uint32
	Height       uint32
	Width        uint32
	AccelFlags   uint32
	Pixclock     uint32
	LeftMargin   uint32
	RightMargin  uint32
	UpperMargin  uint32
	LowerMargin  uint32
	HsyncLen     uint32
	VsyncLen     uint32
	Sync         uint32
	Vmode        uint32
	Rotate       uint32
	Colorspace   uint32
	Reserved     [4]uint32
}

type fbFixScreenInfo struct {
	ID         [16]byte
	SmemStart  uintptr
	SmemLen    uint32
	Type       uint32
	TypeAux    uint32
	Visual     uint32
	XpanStep   uint16
	YpanStep   uint16
	YwrapStep  uint16
	LineLength uint32
	MmioStart  uintptr
	MmioLen    uint32
	Accel      uint32
	Caps       uint16
	Reserved   [2]uint16
}

type fbdevCapturer struct {
	file   *os.File
	data   []byte
	path   string
	bounds image.Rectangle
	source protocol.DesktopSource
	vinfo  fbVarScreenInfo
	finfo  fbFixScreenInfo
	stride int
}

func enumerateFBDevSources() []protocol.DesktopSource {
	paths := []string{"/dev/fb0", "/dev/fb/0"}
	var sources []protocol.DesktopSource
	for _, path := range paths {
		vinfo, finfo, err := probeFBDev(path)
		if err != nil {
			continue
		}
		label := strings.TrimRight(string(finfo.ID[:]), "\x00 ")
		if label == "" {
			label = path
		}
		sources = append(sources, protocol.DesktopSource{
			ID:      "fbdev:" + path,
			Label:   "Framebuffer " + label,
			Kind:    "screen",
			Backend: "fbdev",
			Width:   int(vinfo.Xres),
			Height:  int(vinfo.Yres),
			Primary: len(sources) == 0,
		})
	}
	return sources
}

func fbdevPath(source string) string {
	if strings.HasPrefix(source, "fbdev:") {
		return strings.TrimPrefix(source, "fbdev:")
	}
	if source == "" || source == "auto" || source == "screen:all" {
		for _, candidate := range []string{"/dev/fb0", "/dev/fb/0"} {
			if _, _, err := probeFBDev(candidate); err == nil {
				return candidate
			}
		}
	}
	return ""
}

func fbdevSysfsName(path string) string {
	name := strings.TrimPrefix(path, "/dev/")
	name = strings.TrimPrefix(name, "fb/")
	if name == "0" {
		return "fb0"
	}
	return name
}

func fbdevBlanked(path string) bool {
	data, err := os.ReadFile("/sys/class/graphics/" + fbdevSysfsName(path) + "/blank")
	if err != nil {
		return false
	}
	blank, err := strconv.Atoi(strings.TrimSpace(string(data)))
	return err == nil && blank != 0
}

func probeFBDev(path string) (fbVarScreenInfo, fbFixScreenInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return fbVarScreenInfo{}, fbFixScreenInfo{}, err
	}
	defer file.Close()
	var vinfo fbVarScreenInfo
	var finfo fbFixScreenInfo
	if err := fbdevIoctl(file, fbioGetVScreenInfo, unsafe.Pointer(&vinfo)); err != nil {
		return fbVarScreenInfo{}, fbFixScreenInfo{}, err
	}
	if err := fbdevIoctl(file, fbioGetFScreenInfo, unsafe.Pointer(&finfo)); err != nil {
		return fbVarScreenInfo{}, fbFixScreenInfo{}, err
	}
	if vinfo.Xres == 0 || vinfo.Yres == 0 || vinfo.BitsPerPixel == 0 {
		return fbVarScreenInfo{}, fbFixScreenInfo{}, fmt.Errorf("invalid framebuffer geometry")
	}
	if fbdevBlanked(path) {
		return fbVarScreenInfo{}, fbFixScreenInfo{}, fmt.Errorf("framebuffer is blanked")
	}
	if vinfo.BitsPerPixel != 16 && vinfo.BitsPerPixel != 24 && vinfo.BitsPerPixel != 32 {
		return fbVarScreenInfo{}, fbFixScreenInfo{}, fmt.Errorf("unsupported framebuffer bpp %d", vinfo.BitsPerPixel)
	}
	return vinfo, finfo, nil
}

func newFBDevCapturer(source string) (desktopCapturer, error) {
	path := fbdevPath(source)
	if path == "" {
		return nil, fmt.Errorf("fbdev source %q not found", source)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open framebuffer %s: %w", path, err)
	}
	var vinfo fbVarScreenInfo
	var finfo fbFixScreenInfo
	if err := fbdevIoctl(file, fbioGetVScreenInfo, unsafe.Pointer(&vinfo)); err != nil {
		file.Close()
		return nil, fmt.Errorf("read framebuffer variable info: %w", err)
	}
	if err := fbdevIoctl(file, fbioGetFScreenInfo, unsafe.Pointer(&finfo)); err != nil {
		file.Close()
		return nil, fmt.Errorf("read framebuffer fixed info: %w", err)
	}
	if vinfo.Xres == 0 || vinfo.Yres == 0 || vinfo.BitsPerPixel == 0 {
		file.Close()
		return nil, fmt.Errorf("invalid framebuffer geometry")
	}
	if fbdevBlanked(path) {
		file.Close()
		return nil, fmt.Errorf("framebuffer %s is blanked", path)
	}
	stride := int(finfo.LineLength)
	if stride <= 0 {
		stride = int(vinfo.Xres * vinfo.BitsPerPixel / 8)
	}
	mapLen := int(finfo.SmemLen)
	minLen := stride * int(vinfo.Yres+vinfo.Yoffset)
	if mapLen < minLen {
		mapLen = minLen
	}
	data, err := syscall.Mmap(int(file.Fd()), 0, mapLen, syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("mmap framebuffer: %w", err)
	}
	label := strings.TrimRight(string(finfo.ID[:]), "\x00 ")
	if label == "" {
		label = path
	}
	bounds := image.Rect(0, 0, int(vinfo.Xres), int(vinfo.Yres))
	return &fbdevCapturer{
		file: file, data: data, path: path, bounds: bounds, vinfo: vinfo, finfo: finfo, stride: stride,
		source: protocol.DesktopSource{ID: "fbdev:" + path, Label: "Framebuffer " + label, Kind: "screen", Backend: "fbdev", Width: bounds.Dx(), Height: bounds.Dy(), Primary: true},
	}, nil
}

func fbdevIoctl(file *os.File, req uintptr, ptr unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), req, uintptr(ptr))
	if errno != 0 {
		return errno
	}
	return nil
}

func (c *fbdevCapturer) Bounds() image.Rectangle { return c.bounds }

func (c *fbdevCapturer) Source() protocol.DesktopSource { return c.source }

func (c *fbdevCapturer) Close() error {
	if c.data != nil {
		_ = syscall.Munmap(c.data)
		c.data = nil
	}
	if c.file != nil {
		return c.file.Close()
	}
	return nil
}

func (c *fbdevCapturer) Capture() (image.Image, error) {
	width := int(c.vinfo.Xres)
	height := int(c.vinfo.Yres)
	bpp := int(c.vinfo.BitsPerPixel / 8)
	if bpp <= 0 || c.stride <= 0 {
		return nil, fmt.Errorf("invalid framebuffer layout")
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	base := int(c.vinfo.Yoffset)*c.stride + int(c.vinfo.Xoffset)*bpp
	for y := 0; y < height; y++ {
		row := base + y*c.stride
		for x := 0; x < width; x++ {
			off := row + x*bpp
			if off+bpp > len(c.data) {
				break
			}
			pixel := c.fbPixel(c.data[off : off+bpp])
			r := scaleColor((pixel&maskFor(c.vinfo.Red))>>c.vinfo.Red.Offset, bitfieldMax(c.vinfo.Red))
			g := scaleColor((pixel&maskFor(c.vinfo.Green))>>c.vinfo.Green.Offset, bitfieldMax(c.vinfo.Green))
			b := scaleColor((pixel&maskFor(c.vinfo.Blue))>>c.vinfo.Blue.Offset, bitfieldMax(c.vinfo.Blue))
			img.SetRGBA(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
		}
	}
	return img, nil
}

func (c *fbdevCapturer) fbPixel(data []byte) uint32 {
	switch c.vinfo.BitsPerPixel {
	case 32:
		return binary.LittleEndian.Uint32(data)
	case 24:
		return uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16
	case 16:
		return uint32(binary.LittleEndian.Uint16(data))
	default:
		return 0
	}
}

func maskFor(field fbBitfield) uint32 {
	if field.Length == 0 || field.Length >= 32 {
		return ^uint32(0)
	}
	return ((uint32(1) << field.Length) - 1) << field.Offset
}

func bitfieldMax(field fbBitfield) uint32 {
	if field.Length == 0 {
		return 0
	}
	if field.Length >= 32 {
		return ^uint32(0)
	}
	return (uint32(1) << field.Length) - 1
}

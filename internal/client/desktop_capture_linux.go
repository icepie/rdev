//go:build linux

package client

import (
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"math/bits"

	"github.com/BurntSushi/xgb"
	"github.com/BurntSushi/xgb/xproto"
)

type x11DesktopCapturer struct {
	conn        *xgb.Conn
	screen      *xproto.ScreenInfo
	visual      xproto.VisualInfo
	format      xproto.Format
	bounds      image.Rectangle
	byteOrder   binary.ByteOrder
	redShift    uint
	greenShift  uint
	blueShift   uint
	redMax      uint32
	greenMax    uint32
	blueMax     uint32
	bytesPerPix int
}

func newDesktopCapturer() (desktopCapturer, error) {
	conn, err := xgb.NewConn()
	if err != nil {
		return nil, fmt.Errorf("connect X11: %w", err)
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
	geom, err := xproto.GetGeometry(conn, xproto.Drawable(screen.Root)).Reply()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("X11 root geometry: %w", err)
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
		bounds:      image.Rect(0, 0, int(geom.Width), int(geom.Height)),
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

func (c *x11DesktopCapturer) Bounds() image.Rectangle { return c.bounds }

func (c *x11DesktopCapturer) Close() error {
	c.conn.Close()
	return nil
}

func (c *x11DesktopCapturer) Capture() (image.Image, error) {
	width := c.bounds.Dx()
	height := c.bounds.Dy()
	reply, err := xproto.GetImage(c.conn, xproto.ImageFormatZPixmap, xproto.Drawable(c.screen.Root), 0, 0, uint16(width), uint16(height), ^uint32(0)).Reply()
	if err != nil {
		return nil, fmt.Errorf("X11 get image: %w", err)
	}
	img := image.NewRGBA(c.bounds)
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

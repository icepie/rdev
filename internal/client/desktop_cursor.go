package client

import (
	"image"
	"image/color"
)

type desktopCursorProvider interface {
	CursorPosition() (image.Point, bool)
}

var desktopCursorBitmap = []string{
	"X...........",
	"XX..........",
	"XWX.........",
	"XWWX........",
	"XWWWX.......",
	"XWWWWX......",
	"XWWWWWX.....",
	"XWWWWWWX....",
	"XWWWWWWWX...",
	"XWWWWXXXXX..",
	"XWWX........",
	"XWXX........",
	"XX.XX.......",
	"X..XX.......",
	"...XX.......",
	"...XX.......",
}

func desktopCursorPosition(session *desktopSession, capturer desktopCapturer) (image.Point, bool) {
	bounds := capturer.Bounds()
	if provider, ok := capturer.(desktopCursorProvider); ok {
		if point, ok := provider.CursorPosition(); ok && point.In(bounds) {
			return point, true
		}
	}
	return session.cursorPosition()
}

func overlayDesktopCursor(frame *image.RGBA, bounds image.Rectangle, cursor image.Point) {
	if frame == nil || bounds.Dx() <= 0 || bounds.Dy() <= 0 || frame.Rect.Dx() <= 0 || frame.Rect.Dy() <= 0 {
		return
	}
	if cursor.X < bounds.Min.X || cursor.Y < bounds.Min.Y || cursor.X >= bounds.Max.X || cursor.Y >= bounds.Max.Y {
		return
	}
	x := (cursor.X - bounds.Min.X) * frame.Rect.Dx() / bounds.Dx()
	y := (cursor.Y - bounds.Min.Y) * frame.Rect.Dy() / bounds.Dy()
	black := color.RGBA{A: 255}
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	for row, mask := range desktopCursorBitmap {
		for col, pixel := range mask {
			if pixel == '.' {
				continue
			}
			px := frame.Rect.Min.X + x + col
			py := frame.Rect.Min.Y + y + row
			if px < frame.Rect.Min.X || py < frame.Rect.Min.Y || px >= frame.Rect.Max.X || py >= frame.Rect.Max.Y {
				continue
			}
			if pixel == 'W' {
				frame.SetRGBA(px, py, white)
			} else {
				frame.SetRGBA(px, py, black)
			}
		}
	}
}

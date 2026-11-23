package server

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"image"
	"io"
	"time"
	"unicode"

	"rdev/internal/protocol"
)

const (
	rfbSecurityVeNCrypt = 19
	rfbVeNCryptPlain    = 256

	rfbClientSetPixelFormat           = 0
	rfbClientSetEncodings             = 2
	rfbClientFramebufferUpdateRequest = 3
	rfbClientKeyEvent                 = 4
	rfbClientPointerEvent             = 5
	rfbClientCutText                  = 6

	rfbEncodingRaw  = 0
	rfbEncodingZRLE = 16
)

func (v *vncConn) writeServerInit(width, height int) error {
	if err := v.writeU16(width); err != nil {
		return err
	}
	if err := v.writeU16(height); err != nil {
		return err
	}
	pf := []byte{32, 24, 0, 1, 0, 255, 0, 255, 0, 255, 16, 8, 0, 0, 0, 0}
	if _, err := v.conn.Write(pf); err != nil {
		return err
	}
	name := []byte("RDev " + v.deviceID)
	if err := v.writeU32(uint32(len(name))); err != nil {
		return err
	}
	_, err := v.conn.Write(name)
	return err
}

func (v *vncConn) handleSetEncodings() error {
	if err := v.discard(1); err != nil {
		return err
	}
	count, err := v.readU16()
	if err != nil {
		return err
	}
	v.supportsZRLE = false
	for i := 0; i < int(count); i++ {
		encoding, err := v.readS32()
		if err != nil {
			return err
		}
		if encoding == rfbEncodingZRLE {
			v.supportsZRLE = true
		}
	}
	return nil
}

func (v *vncConn) handleFramebufferUpdateRequest() error {
	incremental, err := v.reader.ReadByte()
	if err != nil {
		return err
	}
	x, err := v.readU16()
	if err != nil {
		return err
	}
	y, err := v.readU16()
	if err != nil {
		return err
	}
	width, err := v.readU16()
	if err != nil {
		return err
	}
	height, err := v.readU16()
	if err != nil {
		return err
	}
	return v.sendFramebuffer(incremental != 0, int(x), int(y), int(width), int(height))
}

type vncRawRect struct {
	x      int
	y      int
	width  int
	height int
	data   []byte
}

func (v *vncConn) makeRawRect(frame []byte, frameWidth, x, y, width, height int) vncRawRect {
	return vncRawRect{x: x, y: y, width: width, height: height, data: vncRawSubrect(frame, frameWidth, x, y, width, height)}
}

func (v *vncConn) writeFramebufferUpdate(rects []vncRawRect) error {
	if v.supportsZRLE {
		return v.writeZRLEFramebufferUpdate(rects)
	}
	return v.writeRawFramebufferUpdate(rects)
}

func (v *vncConn) writeRawFramebufferUpdate(rects []vncRawRect) error {
	if err := v.writeU8(0); err != nil { // FramebufferUpdate
		return err
	}
	if err := v.writeU8(0); err != nil {
		return err
	}
	if err := v.writeU16(len(rects)); err != nil {
		return err
	}
	for _, rect := range rects {
		if err := v.writeU16(rect.x); err != nil {
			return err
		}
		if err := v.writeU16(rect.y); err != nil {
			return err
		}
		if err := v.writeU16(rect.width); err != nil {
			return err
		}
		if err := v.writeU16(rect.height); err != nil {
			return err
		}
		if err := v.writeS32(rfbEncodingRaw); err != nil {
			return err
		}
		if _, err := v.conn.Write(rect.data); err != nil {
			return err
		}
	}
	return nil
}

func (v *vncConn) writeZRLEFramebufferUpdate(rects []vncRawRect) error {
	encoded := make([][]byte, len(rects))
	for i, rect := range rects {
		encoded[i] = vncEncodeZRLE(rect.data, rect.width, rect.height)
	}
	if err := v.writeU8(0); err != nil { // FramebufferUpdate
		return err
	}
	if err := v.writeU8(0); err != nil {
		return err
	}
	if err := v.writeU16(len(rects)); err != nil {
		return err
	}
	for i, rect := range rects {
		if err := v.writeU16(rect.x); err != nil {
			return err
		}
		if err := v.writeU16(rect.y); err != nil {
			return err
		}
		if err := v.writeU16(rect.width); err != nil {
			return err
		}
		if err := v.writeU16(rect.height); err != nil {
			return err
		}
		if err := v.writeS32(rfbEncodingZRLE); err != nil {
			return err
		}
		if err := v.writeU32(uint32(len(encoded[i]))); err != nil {
			return err
		}
		if _, err := v.conn.Write(encoded[i]); err != nil {
			return err
		}
	}
	return nil
}

func (v *vncConn) handleKeyEvent() error {
	down, err := v.reader.ReadByte()
	if err != nil {
		return err
	}
	if err := v.discard(2); err != nil {
		return err
	}
	keysym, err := v.readU32()
	if err != nil {
		return err
	}
	key, code := rfbKey(keysym)
	if code == "" {
		return nil
	}
	inputType := "key_up"
	if down != 0 {
		inputType = "key_down"
	}
	v.sendInput(&protocol.Message{InputType: inputType, Key: key, Code: code})
	return nil
}

func rfbKey(keysym uint32) (string, string) {
	if keysym >= 'a' && keysym <= 'z' {
		upper := unicode.ToUpper(rune(keysym))
		return string(rune(keysym)), "Key" + string(upper)
	}
	if keysym >= 'A' && keysym <= 'Z' {
		return string(rune(keysym)), "Key" + string(rune(keysym))
	}
	if keysym >= '0' && keysym <= '9' {
		return string(rune(keysym)), "Digit" + string(rune(keysym))
	}
	switch keysym {
	case 0xff1b:
		return "Escape", "Escape"
	case 0xff08:
		return "Backspace", "Backspace"
	case 0xff09:
		return "Tab", "Tab"
	case 0xff0d:
		return "Enter", "Enter"
	case 0xffe3, 0xffe4:
		return "Control", "ControlLeft"
	case 0xffe1:
		return "Shift", "ShiftLeft"
	case 0xffe2:
		return "Shift", "ShiftRight"
	case 0xffe9, 0xffea:
		return "Alt", "AltLeft"
	case 0x20:
		return " ", "Space"
	case 0xff50:
		return "Home", "Home"
	case 0xff51:
		return "ArrowLeft", "ArrowLeft"
	case 0xff52:
		return "ArrowUp", "ArrowUp"
	case 0xff53:
		return "ArrowRight", "ArrowRight"
	case 0xff54:
		return "ArrowDown", "ArrowDown"
	case 0xff55:
		return "PageUp", "PageUp"
	case 0xff56:
		return "PageDown", "PageDown"
	case 0xff57:
		return "End", "End"
	case 0xff63:
		return "Insert", "Insert"
	case 0xffff:
		return "Delete", "Delete"
	}
	if keysym >= 0xffbe && keysym <= 0xffc9 {
		n := keysym - 0xffbd
		return fmt.Sprintf("F%d", n), fmt.Sprintf("F%d", n)
	}
	return "", ""
}

func (v *vncConn) handlePointerEvent() error {
	buttons, err := v.reader.ReadByte()
	if err != nil {
		return err
	}
	x, err := v.readU16()
	if err != nil {
		return err
	}
	y, err := v.readU16()
	if err != nil {
		return err
	}
	now := time.Now()
	if now.Sub(v.lastMouse) >= 16*time.Millisecond {
		v.sendInput(&protocol.Message{InputType: "mouse_move", X: int(x), Y: int(y), PointerType: "mouse"})
		v.lastMouse = now
	}
	for i := 0; i < 3; i++ {
		mask := uint8(1 << i)
		was := v.buttons&mask != 0
		is := buttons&mask != 0
		if was == is {
			continue
		}
		button := []int{0, 1, 2}[i]
		inputType := "mouse_up"
		if is {
			inputType = "mouse_down"
		}
		v.sendInput(&protocol.Message{InputType: inputType, X: int(x), Y: int(y), Button: button, PointerType: "mouse"})
	}
	if buttons&(1<<3) != 0 {
		v.sendInput(&protocol.Message{InputType: "wheel", X: int(x), Y: int(y), DeltaY: -120, PointerType: "mouse"})
	}
	if buttons&(1<<4) != 0 {
		v.sendInput(&protocol.Message{InputType: "wheel", X: int(x), Y: int(y), DeltaY: 120, PointerType: "mouse"})
	}
	if buttons&(1<<5) != 0 {
		v.sendInput(&protocol.Message{InputType: "wheel", X: int(x), Y: int(y), DeltaX: -120, PointerType: "mouse"})
	}
	if buttons&(1<<6) != 0 {
		v.sendInput(&protocol.Message{InputType: "wheel", X: int(x), Y: int(y), DeltaX: 120, PointerType: "mouse"})
	}
	v.buttons = buttons & 0x07
	return nil
}

func (v *vncConn) handleClientCutText() error {
	if err := v.discard(3); err != nil {
		return err
	}
	length, err := v.readU32()
	if err != nil {
		return err
	}
	if length > 16*1024*1024 {
		return fmt.Errorf("client cut text too large")
	}
	return v.discard(int(length))
}

func vncRawFrame(img image.Image, width, height int) []byte {
	if width <= 0 || height <= 0 {
		return nil
	}
	bounds := img.Bounds()
	frame := make([]byte, width*height*4)
	for y := 0; y < height; y++ {
		sy := bounds.Min.Y + y*bounds.Dy()/height
		for x := 0; x < width; x++ {
			sx := bounds.Min.X + x*bounds.Dx()/width
			r, g, b, _ := img.At(sx, sy).RGBA()
			off := (y*width + x) * 4
			frame[off+0] = byte(b >> 8)
			frame[off+1] = byte(g >> 8)
			frame[off+2] = byte(r >> 8)
			frame[off+3] = 0
		}
	}
	return frame
}

func vncClampRect(x, y, width, height, maxWidth, maxHeight int) (int, int, int, int, bool) {
	if maxWidth <= 0 || maxHeight <= 0 || width <= 0 || height <= 0 {
		return 0, 0, 0, 0, false
	}
	if x < 0 {
		width += x
		x = 0
	}
	if y < 0 {
		height += y
		y = 0
	}
	if x >= maxWidth || y >= maxHeight || width <= 0 || height <= 0 {
		return 0, 0, 0, 0, false
	}
	if x+width > maxWidth {
		width = maxWidth - x
	}
	if y+height > maxHeight {
		height = maxHeight - y
	}
	return x, y, width, height, width > 0 && height > 0
}

func vncIntersectRect(ax, ay, aw, ah, bx, by, bw, bh int) (int, int, int, int, bool) {
	x1 := maxInt(ax, bx)
	y1 := maxInt(ay, by)
	x2 := minInt(ax+aw, bx+bw)
	y2 := minInt(ay+ah, by+bh)
	w, h := x2-x1, y2-y1
	return x1, y1, w, h, w > 0 && h > 0
}

func vncChangedRect(prev, next []byte, width, height int) (int, int, int, int, bool) {
	if width <= 0 || height <= 0 || len(prev) != len(next) || len(next) < width*height*4 {
		return 0, 0, width, height, true
	}
	minX, minY := width, height
	maxX, maxY := -1, -1
	stride := width * 4
	for y := 0; y < height; y++ {
		rowStart := y * stride
		rowEnd := rowStart + stride
		if bytes.Equal(prev[rowStart:rowEnd], next[rowStart:rowEnd]) {
			continue
		}
		left, right := -1, -1
		for x := 0; x < width; x++ {
			off := rowStart + x*4
			if !bytes.Equal(prev[off:off+4], next[off:off+4]) {
				left = x
				break
			}
		}
		for x := width - 1; x >= 0; x-- {
			off := rowStart + x*4
			if !bytes.Equal(prev[off:off+4], next[off:off+4]) {
				right = x
				break
			}
		}
		if left >= 0 {
			minX = minInt(minX, left)
			maxX = maxInt(maxX, right)
			minY = minInt(minY, y)
			maxY = maxInt(maxY, y)
		}
	}
	if maxX < minX || maxY < minY {
		return 0, 0, 0, 0, false
	}
	return minX, minY, maxX - minX + 1, maxY - minY + 1, true
}

func vncRawSubrect(frame []byte, frameWidth, x, y, width, height int) []byte {
	if width <= 0 || height <= 0 || frameWidth <= 0 {
		return nil
	}
	stride := frameWidth * 4
	rectStride := width * 4
	out := make([]byte, rectStride*height)
	for row := 0; row < height; row++ {
		src := (y+row)*stride + x*4
		copy(out[row*rectStride:(row+1)*rectStride], frame[src:src+rectStride])
	}
	return out
}

func vncEncodeZRLE(raw []byte, width, height int) []byte {
	var plain bytes.Buffer
	for y := 0; y < height; y += 64 {
		tileH := minInt(64, height-y)
		for x := 0; x < width; x += 64 {
			tileW := minInt(64, width-x)
			plain.Write(vncEncodeZRLETile(raw, width, x, y, tileW, tileH))
		}
	}
	var compressed bytes.Buffer
	zw, err := zlib.NewWriterLevel(&compressed, zlib.BestSpeed)
	if err != nil {
		zw = zlib.NewWriter(&compressed)
	}
	_, _ = zw.Write(plain.Bytes())
	_ = zw.Close()
	return compressed.Bytes()
}

func vncEncodeZRLETile(raw []byte, stridePixels, x, y, width, height int) []byte {
	palette := make([][3]byte, 0, 16)
	paletteIndex := make(map[[3]byte]byte, 16)
	indices := make([]byte, 0, width*height)
	paletteOverflow := false
	for ty := 0; ty < height; ty++ {
		row := ((y+ty)*stridePixels + x) * 4
		for tx := 0; tx < width; tx++ {
			px := row + tx*4
			color := [3]byte{raw[px], raw[px+1], raw[px+2]}
			idx, ok := paletteIndex[color]
			if !ok {
				if len(palette) >= 16 {
					paletteOverflow = true
					break
				}
				idx = byte(len(palette))
				paletteIndex[color] = idx
				palette = append(palette, color)
			}
			indices = append(indices, idx)
		}
		if paletteOverflow {
			break
		}
	}
	if len(palette) == 1 && !paletteOverflow {
		return []byte{1, palette[0][0], palette[0][1], palette[0][2]}
	}
	if len(palette) >= 2 && !paletteOverflow {
		return vncEncodeZRLEPackedPaletteTile(palette, indices, width, height)
	}
	var out bytes.Buffer
	out.WriteByte(0) // raw truecolor tile
	for ty := 0; ty < height; ty++ {
		row := ((y+ty)*stridePixels + x) * 4
		for tx := 0; tx < width; tx++ {
			px := row + tx*4
			out.Write(raw[px : px+3]) // 32bpp depth-24 little-endian CPIXEL omits X byte.
		}
	}
	return out.Bytes()
}

func vncEncodeZRLEPackedPaletteTile(palette [][3]byte, indices []byte, width, height int) []byte {
	bitsPerPixel := 1
	if len(palette) > 4 {
		bitsPerPixel = 4
	} else if len(palette) > 2 {
		bitsPerPixel = 2
	}
	var out bytes.Buffer
	out.WriteByte(byte(len(palette)))
	for _, color := range palette {
		out.Write(color[:])
	}
	mask := byte((1 << bitsPerPixel) - 1)
	for y := 0; y < height; y++ {
		bitPos := 8
		var packed byte
		row := y * width
		for x := 0; x < width; x++ {
			bitPos -= bitsPerPixel
			packed |= (indices[row+x] & mask) << bitPos
			if bitPos == 0 {
				out.WriteByte(packed)
				packed = 0
				bitPos = 8
			}
		}
		if bitPos != 8 {
			out.WriteByte(packed)
		}
	}
	return out.Bytes()
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (v *vncConn) writeSecurityFailure(reason string) error {
	if err := v.writeU32(1); err != nil {
		return err
	}
	if err := v.writeU32(uint32(len(reason))); err != nil {
		return err
	}
	_, err := io.WriteString(v.conn, reason)
	return err
}

func (v *vncConn) discard(n int) error {
	_, err := io.CopyN(io.Discard, v.reader, int64(n))
	return err
}

func (v *vncConn) readU16() (uint16, error) {
	var value uint16
	err := binary.Read(v.reader, binary.BigEndian, &value)
	return value, err
}

func (v *vncConn) readU32() (uint32, error) {
	var value uint32
	err := binary.Read(v.reader, binary.BigEndian, &value)
	return value, err
}

func (v *vncConn) readS32() (int32, error) {
	var value int32
	err := binary.Read(v.reader, binary.BigEndian, &value)
	return value, err
}

func (v *vncConn) writeU8(value byte) error {
	_, err := v.conn.Write([]byte{value})
	return err
}

func (v *vncConn) writeU16(value int) error {
	return binary.Write(v.conn, binary.BigEndian, uint16(value))
}

func (v *vncConn) writeU32(value uint32) error {
	return binary.Write(v.conn, binary.BigEndian, value)
}

func (v *vncConn) writeS32(value int32) error {
	return binary.Write(v.conn, binary.BigEndian, value)
}

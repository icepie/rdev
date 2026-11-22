package client

import (
	"encoding/binary"
	"math"
	"sync"
)

// DXGI R16G16B16A16_FLOAT desktop frames use linear scRGB. Map SDR
// reference white to 255 and clip brighter highlights for the 8-bit stream.
// The half-float input domain is small enough to precompute the transfer
// function once, only when a floating-point frame is first captured.
var desktopScRGBTable = sync.OnceValue(func() *[1 << 16]byte {
	var table [1 << 16]byte
	for bits := 0; bits < 0x3c00; bits++ {
		exponent, mantissa := bits>>10, bits&1023
		linear := math.Ldexp(float64(mantissa), -24)
		if exponent != 0 {
			linear = math.Ldexp(float64(1024+mantissa), exponent-25)
		}
		value := 12.92 * linear
		if linear > 0.0031308 {
			value = 1.055*math.Pow(linear, 1/2.4) - 0.055
		}
		table[bits] = byte(math.Round(value * 255))
	}
	for bits := 0x3c00; bits <= 0x7c00; bits++ { // 1.0 through positive infinity
		table[bits] = 255
	}
	return &table
})

func desktopScRGBRow(dst, src []byte) {
	table := desktopScRGBTable()
	for len(dst) >= 4 {
		dst[0] = table[binary.LittleEndian.Uint16(src)]
		dst[1] = table[binary.LittleEndian.Uint16(src[2:])]
		dst[2] = table[binary.LittleEndian.Uint16(src[4:])]
		dst[3] = 255
		dst, src = dst[4:], src[8:]
	}
}

package client

import (
	"bytes"
	"testing"
)

func TestDesktopScRGBRow(t *testing.T) {
	// Two little-endian RGBA16F pixels: (0.5, 1, 0), (-1, 2, NaN).
	// Alpha is discarded and row padding must never become image data.
	src := []byte{0, 0x38, 0, 0x3c, 0, 0, 0, 0, 0, 0xbc, 0, 0x40, 1, 0x7c, 0, 0, 42, 42}
	got := make([]byte, 8)
	desktopScRGBRow(got, src)
	want := []byte{188, 255, 0, 255, 0, 255, 0, 255}
	if !bytes.Equal(got, want) {
		t.Fatalf("scRGB to SDR pixels = %v, want %v", got, want)
	}
}

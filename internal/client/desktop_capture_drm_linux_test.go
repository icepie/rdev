//go:build linux

package client

import "testing"

func TestDRMFourCCHelpers(t *testing.T) {
	if got := drmFourCC(drmFormatXRGB8888); got != "XR24" {
		t.Fatalf("drmFourCC(XRGB8888) = %q", got)
	}
	if got := drmBytesPerPixel(drmFormatXRGB8888); got != 4 {
		t.Fatalf("drmBytesPerPixel(XRGB8888) = %d, want 4", got)
	}
	if got := drmBytesPerPixel(drmFormatRGB565); got != 2 {
		t.Fatalf("drmBytesPerPixel(RGB565) = %d, want 2", got)
	}
}

func TestDRMPixelConversion(t *testing.T) {
	pixel := drmPixel(drmFormatXRGB8888, []byte{0x30, 0x20, 0x10, 0x00})
	if pixel.R != 0x10 || pixel.G != 0x20 || pixel.B != 0x30 || pixel.A != 0xff {
		t.Fatalf("XRGB8888 pixel = %#v", pixel)
	}
	pixel = drmPixel(drmFormatRGB565, []byte{0x00, 0xf8})
	if pixel.R != 0xff || pixel.G != 0x00 || pixel.B != 0x00 || pixel.A != 0xff {
		t.Fatalf("RGB565 pixel = %#v", pixel)
	}
}

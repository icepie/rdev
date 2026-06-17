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

func TestDRMModifierHelpers(t *testing.T) {
	if !drmModifierMappable(drmFormatModLinear) {
		t.Fatal("linear modifier should be mappable")
	}
	if !drmModifierMappable(i915FormatModXTiled) {
		t.Fatal("Intel X tiled modifier should be mappable")
	}
	if drmModifierMappable(uint64(drmFormatModVendorIntel)<<56 | 2) {
		t.Fatal("unsupported Intel Y tiled modifier should not be mappable")
	}
	if got := drmMapLen(0, 7680, 1080, i915FormatModXTiled); got != 8294400 {
		t.Fatalf("drmMapLen(X tiled) = %d, want 8294400", got)
	}
}

func TestDRMPixelOffsetLinearAndIntelXTiled(t *testing.T) {
	c := &drmKMSCapturer{mappedPitch: 7680, mappedOffset: 0, mappedModifier: drmFormatModLinear}
	if got := c.pixelOffset(10, 3, 4); got != 3*7680+40 {
		t.Fatalf("linear pixelOffset = %d", got)
	}
	c.mappedModifier = i915FormatModXTiled
	if got := c.pixelOffset(127, 7, 4); got != 7*512+508 {
		t.Fatalf("X tiled first tile offset = %d", got)
	}
	if got := c.pixelOffset(128, 0, 4); got != 4096 {
		t.Fatalf("X tiled second tile offset = %d", got)
	}
	if got := c.pixelOffset(0, 8, 4); got != 7680*8 {
		t.Fatalf("X tiled second tile row offset = %d", got)
	}
}

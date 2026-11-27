package client

import (
	"errors"
	"strings"
	"testing"
)

// A driver that reports S_OK while handing back no pixels must be reported as
// an empty mapping, never as "failed: S_OK".
func TestCheckDXGIMappingSeparatesEmptyFromFailed(t *testing.T) {
	ok := d3d11MappedSubresource{Data: 0x1000, RowPitch: 256}
	if err := checkDXGIMapping(ok, 0); err != nil {
		t.Fatalf("valid mapping rejected: %v", err)
	}

	empty := checkDXGIMapping(d3d11MappedSubresource{}, 0)
	if !errors.Is(empty, errDXGIMappingEmpty) {
		t.Fatalf("null data error = %v, want errDXGIMappingEmpty", empty)
	}
	if !strings.Contains(empty.Error(), "S_OK") || !strings.Contains(empty.Error(), "null data pointer") {
		t.Fatalf("null data message = %q", empty.Error())
	}
	if errors.Is(empty, errDXGIDeviceLost) {
		t.Fatal("null data was reported as a lost device")
	}

	noPitch := checkDXGIMapping(d3d11MappedSubresource{Data: 0x1000}, 0)
	if !errors.Is(noPitch, errDXGIMappingEmpty) || !strings.Contains(noPitch.Error(), "row pitch 0") {
		t.Fatalf("zero pitch error = %v", noPitch)
	}

	failed := checkDXGIMapping(d3d11MappedSubresource{}, 0x80070057)
	if failed == nil || errors.Is(failed, errDXGIMappingEmpty) || errors.Is(failed, errDXGIDeviceLost) {
		t.Fatalf("E_INVALIDARG classification = %v", failed)
	}
	if !strings.Contains(failed.Error(), "D3D11 Map staging texture failed") {
		t.Fatalf("failure message = %q", failed.Error())
	}
}

func TestCheckDXGIMappingDetectsLostDevice(t *testing.T) {
	for _, hr := range []uint32{dxgiErrorDeviceRemoved, dxgiErrorDeviceReset, dxgiErrorAccessLost} {
		err := checkDXGIMapping(d3d11MappedSubresource{}, hr)
		if !errors.Is(err, errDXGIDeviceLost) {
			t.Fatalf("hr %#x classified as %v, want errDXGIDeviceLost", hr, err)
		}
	}
	for _, hr := range []uint32{0, dxgiErrorWaitTimeout, 0x80070057} {
		if dxgiDeviceLost(hr) {
			t.Fatalf("hr %#x treated as device lost", hr)
		}
	}
}

// Dropping the staging texture must also clear the cached description, or the
// next frame would reuse a released handle.
func TestReleaseStagingClearsCachedDescription(t *testing.T) {
	c := &dxgiDesktopCapturer{textureWidth: 1920, textureHeight: 1080, textureFormat: dxgiFormatB8G8R8A8}
	c.releaseStaging()
	if c.staging != 0 || c.textureWidth != 0 || c.textureHeight != 0 || c.textureFormat != 0 {
		t.Fatalf("staging state survived release: %+v", c)
	}
}

// Only the DXGI backend has a GDI counterpart; other capturers must not be
// swapped behind the session's back.
func TestDesktopFallbackCapturerSkipsNonDXGI(t *testing.T) {
	if _, ok := newDesktopFallbackCapturer(&gdiDesktopCapturer{}); ok {
		t.Fatal("GDI capturer was offered a fallback")
	}
}

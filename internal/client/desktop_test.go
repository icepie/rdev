package client

import (
	"runtime"
	"testing"
)

func TestDesktopCapabilitiesReportsCurrentPlatform(t *testing.T) {
	caps := desktopCapabilities()
	if caps == nil {
		t.Fatal("desktopCapabilities() returned nil")
	}
	if caps.Platform != runtime.GOOS {
		t.Fatalf("Platform = %q, want %q", caps.Platform, runtime.GOOS)
	}
	if caps.DisplayServer == "x11" {
		if !caps.Supported || caps.ViewOnly || !caps.Input {
			t.Fatalf("X11 capability = %#v, want supported interactive desktop", caps)
		}
		return
	}
	if caps.DisplayServer == "windows" {
		if !caps.Supported || caps.ViewOnly || !caps.Input {
			t.Fatalf("Windows capability = %#v, want supported interactive desktop", caps)
		}
		return
	}
	if caps.DisplayServer == "drm-kms" || caps.DisplayServer == "fbdev" {
		if !caps.Supported {
			t.Fatalf("Linux fallback capability = %#v, want supported fallback", caps)
		}
		if caps.Input && caps.ViewOnly {
			t.Fatalf("Linux fallback with input should not be view-only: %#v", caps)
		}
		if !caps.Input && !caps.ViewOnly {
			t.Fatalf("Linux fallback without input should be view-only: %#v", caps)
		}
		return
	}
	if caps.Supported {
		t.Fatalf("unsupported desktop capability should be unavailable, got %#v", caps)
	}
	if caps.Reason == "" {
		t.Fatal("expected an unavailable reason")
	}
}

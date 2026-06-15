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
		if !caps.Supported || !caps.ViewOnly {
			t.Fatalf("X11 capability = %#v, want supported view-only", caps)
		}
		return
	}
	if caps.Supported {
		t.Fatalf("non-X11 Phase 1 capability should be unavailable/limited, got %#v", caps)
	}
	if caps.Reason == "" {
		t.Fatal("expected an unavailable/limited reason")
	}
}

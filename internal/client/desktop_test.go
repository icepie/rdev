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
	if caps.Supported {
		t.Fatal("Phase 1 should report capability discovery only, not an implemented capture backend")
	}
	if caps.Reason == "" {
		t.Fatal("expected an unavailable/limited reason")
	}
}

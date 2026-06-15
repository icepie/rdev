package client

import (
	"os"
	"runtime"

	"rdev/internal/protocol"
)

func desktopCapabilities() *protocol.DesktopCapabilities {
	caps := &protocol.DesktopCapabilities{
		Platform:  runtime.GOOS,
		Supported: false,
		ViewOnly:  false,
		Input:     false,
		Clipboard: false,
	}

	switch runtime.GOOS {
	case "windows":
		caps.Backends = []string{"win32-gdi"}
		caps.Reason = "desktop capture backend is planned but not implemented"
	case "linux":
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			caps.DisplayServer = "wayland"
			caps.Backends = []string{"wayland-portal"}
			caps.Reason = "Wayland desktop capture requires a portal backend"
		} else if os.Getenv("DISPLAY") != "" {
			caps.DisplayServer = "x11"
			caps.Supported = true
			caps.ViewOnly = true
			caps.Backends = []string{"x11"}
		} else {
			caps.Reason = "no desktop display detected"
		}
	case "darwin":
		caps.Backends = []string{"coregraphics"}
		caps.Reason = "macOS desktop capture backend is planned but not implemented"
	default:
		caps.Reason = "unsupported platform"
	}

	return caps
}

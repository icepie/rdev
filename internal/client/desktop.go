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
		caps.DisplayServer = "gdi"
		caps.Supported = true
		caps.ViewOnly = true
		caps.Backends = []string{"win32-gdi"}
		caps.Sources = []protocol.DesktopSource{
			{ID: "auto", Label: "Auto", Kind: "screen", Primary: true},
			{ID: "virtual", Label: "Virtual screen", Kind: "screen"},
			{ID: "primary", Label: "Primary screen", Kind: "screen", Primary: true},
		}
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
			caps.Sources = []protocol.DesktopSource{{ID: "auto", Label: "Auto", Kind: "screen", Primary: true}, {ID: "virtual", Label: "X11 root window", Kind: "screen", Primary: true}}
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

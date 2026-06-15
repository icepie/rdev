package client

import (
	"os"
	"runtime"

	"rdev/internal/protocol"
)

func desktopSourcesByBackend(backend string) []protocol.DesktopSource {
	var filtered []protocol.DesktopSource
	for _, source := range desktopSources() {
		if source.Backend == backend {
			filtered = append(filtered, source)
		}
	}
	return filtered
}

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
		caps.DisplayServer = "windows"
		caps.Supported = true
		caps.ViewOnly = false
		caps.Input = true
		caps.Backends = []string{"win32-gdi"}
		caps.Sources = desktopSources()
	case "linux":
		fbSources := desktopSourcesByBackend("fbdev")
		if os.Getenv("DISPLAY") != "" {
			caps.DisplayServer = "x11"
			caps.Supported = true
			caps.ViewOnly = false
			caps.Input = true
			caps.Backends = []string{"x11"}
			caps.Sources = desktopSources()
		} else if os.Getenv("WAYLAND_DISPLAY") != "" {
			caps.DisplayServer = "wayland"
			caps.Backends = []string{"wayland-portal"}
			caps.Reason = "Wayland capture requires native xdg-desktop-portal/PipeWire support; external tools are not used"
			if len(fbSources) > 0 {
				caps.Supported = true
				caps.ViewOnly = true
				caps.Backends = append(caps.Backends, "fbdev")
				caps.Sources = append([]protocol.DesktopSource{{ID: "auto", Label: "Auto", Kind: "screen", Backend: "fbdev", Width: fbSources[0].Width, Height: fbSources[0].Height, Primary: true}}, fbSources...)
				caps.Reason = "Wayland portal backend is not implemented; using root framebuffer fallback"
			}
		} else if len(fbSources) > 0 {
			caps.DisplayServer = "fbdev"
			caps.Supported = true
			caps.ViewOnly = true
			caps.Input = false
			caps.Backends = []string{"fbdev"}
			caps.Sources = append([]protocol.DesktopSource{{ID: "auto", Label: "Auto", Kind: "screen", Backend: "fbdev", Width: fbSources[0].Width, Height: fbSources[0].Height, Primary: true}}, fbSources...)
		} else {
			caps.Reason = "no desktop display or readable framebuffer detected"
		}
	case "darwin":
		caps.DisplayServer = "quartz"
		caps.Backends = []string{"quartz"}
		caps.Sources = desktopSources()
		caps.Reason = "macOS Quartz/CoreGraphics capture backend is planned; default no-cgo build reports capability only"
	default:
		caps.Reason = "unsupported platform"
	}

	return caps
}

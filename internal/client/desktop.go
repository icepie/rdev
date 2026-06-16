package client

import (
	"os"
	"runtime"

	"rdev/internal/protocol"
)

func desktopSourcesByBackend(backend string) []protocol.DesktopSource {
	return desktopSourcesByBackendFrom(desktopSources(), backend)
}

func desktopSourcesByBackendFrom(sources []protocol.DesktopSource, backend string) []protocol.DesktopSource {
	var filtered []protocol.DesktopSource
	for _, source := range sources {
		if source.ID == "auto" {
			continue
		}
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
		sources := desktopSources()
		inputOptions := desktopInputOptions()
		inputBackends := desktopInputBackendIDs(inputOptions)
		caps.DisplayServer = "windows"
		caps.Supported = true
		caps.ViewOnly = false
		caps.Input = len(inputBackends) > 0
		caps.InputBackends = inputBackends
		caps.InputOptions = inputOptions
		caps.Backends = []string{"win32-gdi"}
		if len(desktopSourcesByBackendFrom(sources, "dxgi")) > 0 {
			caps.Backends = append(caps.Backends, "dxgi")
		}
		caps.Sources = sources
	case "linux":
		sources := desktopSources()
		inputOptions := desktopInputOptions()
		inputBackends := desktopInputBackendIDs(inputOptions)
		fbSources := desktopSourcesByBackendFrom(sources, "fbdev")
		drmSources := desktopSourcesByBackendFrom(sources, "drm-kms")
		x11Sources := append(desktopSourcesByBackendFrom(sources, "x11"), desktopSourcesByBackendFrom(sources, "x11-randr")...)
		if len(x11Sources) > 0 {
			caps.DisplayServer = "x11"
			caps.Supported = true
			caps.ViewOnly = false
			caps.Input = len(inputBackends) > 0
			caps.InputBackends = inputBackends
			caps.InputOptions = inputOptions
			caps.Backends = []string{"x11"}
			if len(drmSources) > 0 {
				caps.Backends = append(caps.Backends, "drm-kms")
			}
			caps.Sources = sources
		} else if os.Getenv("WAYLAND_DISPLAY") != "" {
			caps.DisplayServer = "wayland"
			caps.Input = len(inputBackends) > 0
			caps.InputBackends = inputBackends
			caps.InputOptions = inputOptions
			caps.Backends = []string{"wayland-portal"}
			caps.Reason = "Wayland capture requires native xdg-desktop-portal/PipeWire support; external tools are not used"
			if len(drmSources) > 0 {
				caps.Supported = true
				caps.ViewOnly = !caps.Input
				caps.Backends = append(caps.Backends, "drm-kms")
				caps.Sources = append([]protocol.DesktopSource{{ID: "auto", Label: "Auto", Kind: "screen", Backend: "drm-kms", Width: drmSources[0].Width, Height: drmSources[0].Height, Primary: true}}, append(drmSources, fbSources...)...)
				caps.Reason = "Wayland portal backend is not implemented; using DRM/KMS scanout fallback"
			} else if len(fbSources) > 0 {
				caps.Supported = true
				caps.ViewOnly = !caps.Input
				caps.Backends = append(caps.Backends, "fbdev")
				caps.Sources = append([]protocol.DesktopSource{{ID: "auto", Label: "Auto", Kind: "screen", Backend: "fbdev", Width: fbSources[0].Width, Height: fbSources[0].Height, Primary: true}}, fbSources...)
				caps.Reason = "Wayland portal backend is not implemented; using root framebuffer fallback"
			}
		} else if len(drmSources) > 0 {
			caps.DisplayServer = "drm-kms"
			caps.Supported = true
			caps.Input = len(inputBackends) > 0
			caps.ViewOnly = !caps.Input
			caps.InputBackends = inputBackends
			caps.InputOptions = inputOptions
			caps.Backends = []string{"drm-kms"}
			if len(fbSources) > 0 {
				caps.Backends = append(caps.Backends, "fbdev")
			}
			caps.Sources = append([]protocol.DesktopSource{{ID: "auto", Label: "Auto", Kind: "screen", Backend: "drm-kms", Width: drmSources[0].Width, Height: drmSources[0].Height, Primary: true}}, append(drmSources, fbSources...)...)
		} else if len(fbSources) > 0 {
			caps.DisplayServer = "fbdev"
			caps.Supported = true
			caps.Input = len(inputBackends) > 0
			caps.ViewOnly = !caps.Input
			caps.InputBackends = inputBackends
			caps.InputOptions = inputOptions
			caps.Backends = []string{"fbdev"}
			caps.Sources = append([]protocol.DesktopSource{{ID: "auto", Label: "Auto", Kind: "screen", Backend: "fbdev", Width: fbSources[0].Width, Height: fbSources[0].Height, Primary: true}}, fbSources...)
		} else {
			caps.Reason = "no desktop display or readable framebuffer detected"
		}
	case "darwin":
		inputOptions := desktopInputOptions()
		inputBackends := desktopInputBackendIDs(inputOptions)
		caps.DisplayServer = "quartz"
		caps.Supported = true
		caps.Input = len(inputBackends) > 0
		caps.ViewOnly = !caps.Input
		caps.InputBackends = inputBackends
		caps.InputOptions = inputOptions
		caps.Backends = []string{"quartz"}
		caps.Sources = desktopSources()
		caps.Reason = "macOS Quartz capture requires Screen Recording permission; input requires Accessibility permission"
	default:
		caps.Reason = "unsupported platform"
	}

	return caps
}

//go:build darwin

package client

import (
	"fmt"

	"rdev/internal/protocol"
)

func desktopSources() []protocol.DesktopSource {
	return []protocol.DesktopSource{
		{ID: "auto", Label: "Auto", Kind: "screen", Backend: "quartz", Primary: true},
		{ID: "screen:all", Label: "All screens", Kind: "screen", Backend: "quartz", Primary: true},
	}
}

func newDesktopCapturer(source string) (desktopCapturer, error) {
	return nil, fmt.Errorf("macOS desktop capture requires the native Quartz/CoreGraphics backend, which is not implemented in the default no-cgo build yet")
}

//go:build !linux && !windows && !darwin

package client

import (
	"fmt"

	"rdev/internal/protocol"
)

func desktopSources() []protocol.DesktopSource { return nil }

func newDesktopCapturer(source string) (desktopCapturer, error) {
	return nil, fmt.Errorf("desktop capture is not implemented on this platform")
}

// newDesktopFallbackCapturer reports whether another native backend can cover
// the same area; only Windows ships an alternative backend for monitors.
func newDesktopFallbackCapturer(desktopCapturer) (desktopCapturer, bool) {
	return nil, false
}

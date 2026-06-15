//go:build !linux && !windows

package client

import (
	"fmt"

	"rdev/internal/protocol"
)

func desktopSources() []protocol.DesktopSource { return nil }

func newDesktopCapturer(source string) (desktopCapturer, error) {
	return nil, fmt.Errorf("desktop capture is not implemented on this platform")
}

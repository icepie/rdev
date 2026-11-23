//go:build !windows && !linux && !darwin

package client

import (
	"fmt"

	"rdev/internal/protocol"
)

func platformDesktopInputOptions() []protocol.DesktopInputBackend { return nil }

func newDesktopInput(backend string) (desktopInput, error) {
	return nil, fmt.Errorf("desktop input is not supported on this platform")
}

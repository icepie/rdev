//go:build !windows && !linux && !darwin

package client

import (
	"fmt"

	"rdev/internal/protocol"
)

type unsupportedDesktopInput struct{}

func platformDesktopInputOptions() []protocol.DesktopInputBackend { return nil }

func newDesktopInput(backend string) (desktopInput, error) {
	return nil, fmt.Errorf("desktop input is not supported on this platform")
}

func (unsupportedDesktopInput) Backend() string                     { return "" }
func (unsupportedDesktopInput) Apply(event desktopInputEvent) error { return nil }
func (unsupportedDesktopInput) Close() error                        { return nil }

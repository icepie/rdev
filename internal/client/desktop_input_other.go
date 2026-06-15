//go:build !windows && !linux

package client

import "fmt"

type unsupportedDesktopInput struct{}

func newDesktopInput() (desktopInput, error) {
	return nil, fmt.Errorf("desktop input is not supported on this platform")
}

func (unsupportedDesktopInput) Apply(event desktopInputEvent) error { return nil }
func (unsupportedDesktopInput) Close() error                        { return nil }

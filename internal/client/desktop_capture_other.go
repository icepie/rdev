//go:build !linux && !windows

package client

import "fmt"

func newDesktopCapturer(source string) (desktopCapturer, error) {
	return nil, fmt.Errorf("desktop capture is not implemented on this platform")
}

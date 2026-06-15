//go:build !linux

package client

import "fmt"

func newDesktopCapturer() (desktopCapturer, error) {
	return nil, fmt.Errorf("desktop capture is not implemented on this platform")
}

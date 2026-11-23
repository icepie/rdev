//go:build linux

package client

import (
	"fmt"

	"rdev/internal/protocol"
)

func platformDesktopInputOptions() []protocol.DesktopInputBackend {
	var options []protocol.DesktopInputBackend
	if x11InputAvailable() {
		options = append(options, protocol.DesktopInputBackend{ID: "x11-xtest", Label: "X11 XTEST", Kinds: []string{"mouse", "keyboard"}})
	}
	if uinputAvailable() {
		options = append(options, protocol.DesktopInputBackend{ID: "uinput", Label: "Linux uinput", Kinds: []string{"mouse", "keyboard", "touch", "pen"}})
	}
	return options
}

func newDesktopInput(backend string) (desktopInput, error) {
	switch backend {
	case "", "auto", "x11-xtest":
		return newX11DesktopInput()
	case "uinput":
		return newUInputDesktopInput()
	default:
		return nil, fmt.Errorf("unsupported desktop input backend %q", backend)
	}
}

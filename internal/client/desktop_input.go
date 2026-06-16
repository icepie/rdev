package client

import (
	"image"

	"rdev/internal/protocol"
)

type desktopInputEvent struct {
	Type        string
	X           int
	Y           int
	Button      int
	DeltaX      int
	DeltaY      int
	Key         string
	Code        string
	CtrlKey     bool
	AltKey      bool
	ShiftKey    bool
	MetaKey     bool
	PointerType string
	PointerID   int
	Pressure    float64
}

type desktopInput interface {
	Backend() string
	Apply(event desktopInputEvent) error
	Close() error
}

type desktopInputBounds interface {
	SetBounds(bounds image.Rectangle)
}

func desktopInputBackends() []string { return desktopInputBackendIDs(desktopInputOptions()) }

func desktopInputBackendIDs(options []protocol.DesktopInputBackend) []string {
	backends := make([]string, 0, len(options))
	for _, option := range options {
		backends = append(backends, option.ID)
	}
	return backends
}

func desktopInputOptions() []protocol.DesktopInputBackend { return platformDesktopInputOptions() }

func chooseDesktopInputBackend(requested string, available []string) string {
	if requested != "" && requested != "auto" {
		for _, backend := range available {
			if backend == requested {
				return backend
			}
		}
	}
	if len(available) > 0 {
		return available[0]
	}
	return ""
}

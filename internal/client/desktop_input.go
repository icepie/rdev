package client

import (
	"image"
	"log"

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

/* openDesktopInput tries the preferred backend first, then falls back
 * through the remaining advertised ones: an optional backend failing at
 * runtime (e.g. Windows touch injection on a non-interactive session)
 * must not disable mouse/keyboard control entirely. Returns the input
 * and the id of the backend actually in use ("" when none worked). */
func openDesktopInput(preferred string, available []string, newInput func(string) (desktopInput, error)) (desktopInput, string) {
	tried := make(map[string]bool, len(available)+1)
	for _, candidate := range append([]string{preferred}, available...) {
		if candidate == "" || tried[candidate] {
			continue
		}
		tried[candidate] = true
		input, err := newInput(candidate)
		if err != nil {
			log.Printf("desktop input backend %s unavailable: %v", candidate, err)
			continue
		}
		return input, input.Backend()
	}
	return nil, ""
}

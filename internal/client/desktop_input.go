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

// The physical code stays the same when Shift or the layout changes while held.
func (e desktopInputEvent) keyIdentity() string {
	if e.Code != "" {
		return e.Code
	}
	return e.Key
}

type desktopHeldPointer struct {
	pointerType string
	pointerID   int
	button      int
}

type statefulDesktopInput struct {
	input        desktopInput
	heldKeys     map[string]desktopInputEvent
	heldPointers map[desktopHeldPointer]desktopInputEvent
}

func newStatefulDesktopInput(input desktopInput) desktopInput {
	return &statefulDesktopInput{input: input}
}

func (s *statefulDesktopInput) Backend() string { return s.input.Backend() }

func (s *statefulDesktopInput) SetBounds(bounds image.Rectangle) {
	if input, ok := s.input.(desktopInputBounds); ok {
		input.SetBounds(bounds)
	}
}

func (s *statefulDesktopInput) Apply(event desktopInputEvent) error {
	if err := s.input.Apply(event); err != nil {
		return err
	}
	switch event.Type {
	case "key_down":
		if s.heldKeys == nil {
			s.heldKeys = make(map[string]desktopInputEvent)
		}
		s.heldKeys[event.keyIdentity()] = event
	case "key_up":
		delete(s.heldKeys, event.keyIdentity())
	case "mouse_down":
		if s.heldPointers == nil {
			s.heldPointers = make(map[desktopHeldPointer]desktopInputEvent)
		}
		s.heldPointers[desktopHeldPointer{pointerType: event.PointerType, pointerID: event.PointerID, button: event.Button}] = event
	case "mouse_move":
		for key, held := range s.heldPointers {
			if key.pointerType == event.PointerType && key.pointerID == event.PointerID {
				held.X, held.Y, held.Pressure = event.X, event.Y, event.Pressure
				s.heldPointers[key] = held
			}
		}
	case "mouse_up":
		delete(s.heldPointers, desktopHeldPointer{pointerType: event.PointerType, pointerID: event.PointerID, button: event.Button})
	}
	return nil
}

func (s *statefulDesktopInput) Close() error {
	if s.input == nil {
		return nil
	}
	var first error
	for _, held := range s.heldKeys {
		held.Type = "key_up"
		held.CtrlKey, held.AltKey, held.ShiftKey, held.MetaKey = false, false, false, false
		if err := s.input.Apply(held); err != nil && first == nil {
			first = err
		}
	}
	for _, held := range s.heldPointers {
		held.Type = "mouse_up"
		held.Pressure = 0
		if err := s.input.Apply(held); err != nil && first == nil {
			first = err
		}
	}
	s.heldKeys = nil
	s.heldPointers = nil
	if err := s.input.Close(); err != nil && first == nil {
		first = err
	}
	s.input = nil
	return first
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
		return newStatefulDesktopInput(input), input.Backend()
	}
	return nil, ""
}

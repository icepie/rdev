package client

type desktopInputEvent struct {
	Type     string
	X        int
	Y        int
	Button   int
	DeltaX   int
	DeltaY   int
	Key      string
	Code     string
	CtrlKey  bool
	AltKey   bool
	ShiftKey bool
	MetaKey  bool
}

type desktopInput interface {
	Apply(event desktopInputEvent) error
	Close() error
}

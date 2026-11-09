package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"time"

	"rdev/internal/protocol"
)

type automationScreen struct {
	client        *ClientConn
	source        string
	width, height int
	at            time.Time
}

type computerTool struct{ automationTool }

func (*computerTool) Name() string                       { return "computer" }
func (*computerTool) Summary(raw json.RawMessage) string { return automationSummary(raw) }
func (*computerTool) Description() string {
	return "View and operate the selected client's REAL desktop. status lists screens and input capabilities. screenshot returns the selected screen; use its pixel coordinates for click/drag/scroll. key sends a key with optional modifiers. text pastes Unicode through the device clipboard (replaces its contents). Screen control is exclusive to this conversation until release, user takeover, or 2 minutes idle. Always inspect a fresh screenshot before acting. Browser work should normally use the browser tool instead."
}
func (*computerTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"device":{"type":"string"},"action":{"type":"string","enum":["status","screenshot","click","drag","scroll","key","text","release"]},"source":{"type":"string"},"x":{"type":"integer","minimum":0},"y":{"type":"integer","minimum":0},"toX":{"type":"integer","minimum":0},"toY":{"type":"integer","minimum":0},"deltaX":{"type":"integer","minimum":-2000,"maximum":2000},"deltaY":{"type":"integer","minimum":-2000,"maximum":2000},"button":{"type":"integer","minimum":0,"maximum":2},"key":{"type":"string","maxLength":64},"code":{"type":"string","maxLength":64},"ctrl":{"type":"boolean"},"alt":{"type":"boolean"},"shift":{"type":"boolean"},"meta":{"type":"boolean"},"text":{"type":"string","maxLength":20000}},"required":["action"],"additionalProperties":false}`)
}

type computerAction struct {
	Device, Action, Source, Key, Code, Text string
	X, Y, ToX, ToY, DeltaX, DeltaY, Button  int
	Ctrl, Alt, Shift, Meta                  bool
}
type desktopAutomationLease struct {
	owner *agentAutomation
	tool  automationTool
	until time.Time
}

func (t *computerTool) claim(device string) error {
	t.srv.automationMu.Lock()
	defer t.srv.automationMu.Unlock()
	if t.srv.desktopOwners == nil {
		t.srv.desktopOwners = make(map[string]desktopAutomationLease)
	}
	lease := t.srv.desktopOwners[device]
	if lease.owner != nil && lease.owner != t.state && time.Now().Before(lease.until) {
		return fmt.Errorf("desktop is controlled by another conversation; release it or take over in the desktop panel")
	}
	t.srv.desktopOwners[device] = desktopAutomationLease{owner: t.state, tool: t.automationTool, until: time.Now().Add(2 * time.Minute)}
	return nil
}
func (s *Server) takeDesktopControl(device string) {
	s.automationMu.Lock()
	lease := s.desktopOwners[device]
	s.automationMu.Unlock()
	if lease.owner == nil {
		return
	}
	lease.owner.mu.Lock()
	lease.owner.paused = true
	if lease.owner.cancel != nil {
		lease.owner.cancel()
	}
	view := lease.owner.view
	lease.owner.mu.Unlock()
	lease.owner.opMu.Lock()
	lease.owner.screens = nil
	closeGPUAutomation(lease.owner)
	lease.owner.opMu.Unlock()
	s.releaseAutomationDesktop(lease.owner)
	lease.tool.publish(view)
}

// An agent capture is a regular client desktop session with a bounded mailbox,
// rather than a loopback UI websocket or a second platform implementation.
type automationDesktop struct {
	messages chan *protocol.Message
	frames   chan []byte
	done     chan struct{}
}

func (d *automationDesktop) message(m *protocol.Message) {
	select {
	case d.messages <- m:
	default:
	}
}
func (d *automationDesktop) frame(frame []byte) {
	select {
	case d.frames <- bytes.Clone(frame):
	default:
	}
}
func (t *computerTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a computerAction
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", err
	}
	client, err := t.client(a.Device)
	if err != nil {
		return "", err
	}
	if a.Action == "status" && t.srv.clientGPUDesktopAvailable(client) {
		return t.executeGPU(ctx, client, a, raw)
	}
	if a.Action == "status" {
		return marshalJobResult(map[string]any{"device": client.ID, "desktop": client.Desktop})
	}
	if a.Action == "release" {
		if !t.state.opMu.TryLock() {
			return "", fmt.Errorf("wait for the current desktop operation before releasing")
		}
		defer t.state.opMu.Unlock()
		if d := t.state.gpu[client.ID]; d != nil {
			d.close()
			delete(t.state.gpu, client.ID)
		}
		t.srv.automationMu.Lock()
		if t.srv.desktopOwners[client.ID].owner == t.state {
			delete(t.srv.desktopOwners, client.ID)
		}
		t.srv.automationMu.Unlock()
		return "Desktop released.", nil
	}
	if client.Desktop == nil || !client.Desktop.Supported {
		return "", fmt.Errorf("device does not support screen capture")
	}
	if a.Action != "screenshot" && (!client.Desktop.Input || client.Desktop.ViewOnly) {
		return "", fmt.Errorf("device desktop is view-only")
	}
	if t.srv.clientGPUDesktopAvailable(client) {
		return t.executeGPU(ctx, client, a, raw)
	}
	if a.Source != "" {
		found := false
		for _, source := range client.Desktop.Sources {
			if source.ID == a.Source {
				found = true
			}
		}
		if !found {
			return "", fmt.Errorf("unknown screen source; use status")
		}
	}
	ctx, finish, err := t.begin(ctx)
	if err != nil {
		return "", err
	}
	defer finish()
	screen := t.state.screens[client.ID]
	if a.Action != "screenshot" {
		if screen.client != client || time.Since(screen.at) > 2*time.Minute {
			return "", fmt.Errorf("take a fresh screenshot before desktop input")
		}
		if a.Source == "" {
			a.Source = screen.source
		}
		if a.Source != screen.source {
			return "", fmt.Errorf("screen changed; take a new screenshot")
		}
	}
	if err = t.claim(client.ID); err != nil {
		return "", err
	}
	t.publish(automationView{Kind: "desktop", Device: client.ID, Source: a.Source, Action: a.Action})
	id := generateID()
	capture := &automationDesktop{messages: make(chan *protocol.Message, 8), frames: make(chan []byte, 1), done: make(chan struct{})}
	t.srv.desktopMu.Lock()
	if t.srv.desktops == nil {
		t.srv.desktops = make(map[string]*desktopRoute)
	}
	t.srv.desktops[id] = &desktopRoute{id: id, clientID: client.ID, automation: capture}
	t.srv.desktopMu.Unlock()
	defer func() {
		t.srv.desktopMu.Lock()
		delete(t.srv.desktops, id)
		t.srv.desktopMu.Unlock()
		_ = client.Send(&protocol.Message{Type: protocol.MsgDesktopClose, SessionID: id})
	}()
	request := protocol.Message{Type: protocol.MsgDesktopStart, SessionID: id, Format: "mjpeg", FPS: 2, Quality: 80, Source: a.Source, Width: 1280, Height: 720}
	if err = client.Send(&request); err != nil {
		return "", err
	}
	var ready *protocol.Message
	for ready == nil {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-capture.done:
			return "", fmt.Errorf("device disconnected")
		case msg := <-capture.messages:
			if msg.Error != "" {
				return "", fmt.Errorf("desktop: %s", msg.Error)
			}
			if msg.Type == protocol.MsgDesktopClose {
				return "", fmt.Errorf("desktop closed")
			}
			if msg.Type == protocol.MsgDesktopReady {
				ready = msg
			}
		}
	}
	if ready.Width <= 0 || ready.Height <= 0 {
		return "", fmt.Errorf("invalid desktop dimensions")
	}
	if a.Action != "screenshot" && (ready.Width != screen.width || ready.Height != screen.height) {
		return "", fmt.Errorf("screen dimensions changed; take a new screenshot")
	}
	// prepareInput shares the exact validation/coordinate rules with manual UI.
	bc := &desktopBrowserConn{client: client, authOK: true, session: id, request: request, frameWidth: ready.Width, frameHeight: ready.Height}
	input := func(m desktopMsg) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		p, ok := bc.prepareInput(m)
		if !ok {
			return fmt.Errorf("unsupported desktop input")
		}
		return client.Send(p)
	}
	point := func(x, y int) error {
		if x < 0 || y < 0 || x >= ready.Width || y >= ready.Height {
			return fmt.Errorf("coordinates outside %dx%d screenshot", ready.Width, ready.Height)
		}
		return nil
	}
	switch a.Action {
	case "screenshot":
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-capture.done:
			return "", fmt.Errorf("device disconnected")
		case frame := <-capture.frames:
			result, err := automationImage(frame)
			if err == nil {
				cfg, _ := jpeg.DecodeConfig(bytes.NewReader(frame))
				if cfg.Width != ready.Width || cfg.Height != ready.Height {
					return "", fmt.Errorf("capture dimensions do not match device coordinates")
				}
				if t.state.screens == nil {
					t.state.screens = make(map[string]automationScreen)
				}
				t.state.screens[client.ID] = automationScreen{client: client, source: a.Source, width: ready.Width, height: ready.Height, at: time.Now()}
			}
			return result, err
		}
	case "click", "drag", "scroll":
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields)
		if fields["x"] == nil || fields["y"] == nil {
			return "", fmt.Errorf("x and y from a recent screenshot are required")
		}
		if err = point(a.X, a.Y); err != nil {
			return "", err
		}
		if a.Action == "scroll" {
			err = input(desktopMsg{InputType: "wheel", X: a.X, Y: a.Y, DeltaX: a.DeltaX, DeltaY: a.DeltaY})
			break
		}
		if a.Action == "drag" {
			if fields["toX"] == nil || fields["toY"] == nil {
				return "", fmt.Errorf("drag requires toX and toY")
			}
			if err = point(a.ToX, a.ToY); err != nil {
				return "", err
			}
		}
		if err = input(desktopMsg{InputType: "mouse_down", X: a.X, Y: a.Y, Button: a.Button}); err != nil {
			return "", err
		}
		// Release even when takeover cancels between press and release.
		x, y := a.X, a.Y
		if a.Action == "drag" {
			err = input(desktopMsg{InputType: "mouse_move", X: a.ToX, Y: a.ToY, Button: a.Button})
			if err == nil {
				x, y = a.ToX, a.ToY
			}
		}
		release := client.Send(&protocol.Message{Type: protocol.MsgDesktopInput, SessionID: id, InputType: "mouse_up", X: x, Y: y, Button: a.Button})
		if err == nil {
			err = release
		}
	case "key", "text":
		if a.Action == "text" {
			if !client.Desktop.Clipboard {
				return "", fmt.Errorf("device clipboard text input is unsupported")
			}
			if err = client.Send(&protocol.Message{Type: protocol.MsgDesktopClipboard, SessionID: id, ClipboardAction: "set", Text: a.Text}); err != nil {
				return "", err
			}
			acknowledged := false
			for !acknowledged {
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				case <-capture.done:
					return "", fmt.Errorf("device disconnected")
				case m := <-capture.messages:
					if m.Error != "" {
						return "", fmt.Errorf("clipboard: %s", m.Error)
					}
					acknowledged = m.Type == protocol.MsgDesktopClipboard
				}
			}
			a.Key = "v"
			a.Code = "KeyV"
			a.Ctrl = client.OS != "darwin"
			a.Meta = client.OS == "darwin"
		}
		if a.Key == "" {
			return "", fmt.Errorf("key is required")
		}
		down := desktopMsg{InputType: "key_down", Key: a.Key, KeyCode: a.Code, CtrlKey: a.Ctrl, AltKey: a.Alt, ShiftKey: a.Shift, MetaKey: a.Meta}
		if err = input(down); err != nil {
			return "", err
		}
		down.InputType = "key_up"
		up, ok := bc.prepareInput(down)
		if !ok {
			return "", fmt.Errorf("invalid key release")
		}
		err = client.Send(up)
	default:
		return "", fmt.Errorf("unsupported computer action %q", a.Action)
	}
	if err != nil {
		return "", err
	}
	return marshalJobResult(map[string]any{"device": client.ID, "action": a.Action, "width": ready.Width, "height": ready.Height, "status": "sent", "next": "Take a screenshot to verify the result; input delivery is not an application acknowledgement."})
}
func automationImage(data []byte) (string, error) {
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > 16_000_000 {
		return "", fmt.Errorf("invalid screenshot")
	}
	if len(data) > 700*1024 {
		return "", fmt.Errorf("screenshot exceeds image size limit")
	}
	return "[image preview]\ndata:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data), nil
}

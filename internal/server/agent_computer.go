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
	return "Observe and operate the selected client's REAL desktop. Start with status for live screens, input capability and cua support; do not infer capability from old conversation claims. When cua=true, read the cua-driver skill and discover cua action=tools; prefer its semantic/accessibility actions. Use computer input only when CUA is unsupported or the discovered tools cannot perform the action, never to bypass permission denial, takeover or uncertain outcomes. Screenshots are optional for input; use one when coordinates or visual state need grounding, and observe afterward to verify. For computer click/drag/scroll use the selected source's pixel coordinates; native CUA tools have their own schema-defined coordinate spaces. key sends one press/release with optional modifiers; text enters Unicode (replacing the clipboard on Go clients). Screen control is exclusive until release, turn completion, error/cancel, human takeover or 2 minutes idle. These events close the native session and its cursor overlay. Use computer/CUA for the device's installed browser and existing logged-in tabs; browser operates a separate server-hosted browser, not that desktop session."
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
	owner    *agentAutomation
	tool     automationTool
	until    time.Time
	cleaning bool
	manual   int
	timer    *time.Timer
}

// The caller holds its resource opMu. Never wait on another resource while
// holding automationMu: old input must drain before its lease can be replaced.
func (t *automationTool) claim(device string) error {
	t.state.mu.Lock()
	defer t.state.mu.Unlock()
	if t.state.retired || t.state.paused || t.state.manualInputs > 0 {
		return fmt.Errorf("desktop automation is interrupted or retired; resume it before continuing")
	}
	s := t.srv
	s.automationMu.Lock()
	if s.desktopOwners == nil {
		s.desktopOwners = make(map[string]desktopAutomationLease)
	}
	lease := s.desktopOwners[device]
	if lease.cleaning || lease.manual > 0 {
		s.automationMu.Unlock()
		return fmt.Errorf("desktop control is being released; retry after cleanup completes")
	}
	if lease.owner != nil && lease.owner != t.state {
		if time.Now().Before(lease.until) {
			s.automationMu.Unlock()
			return fmt.Errorf("desktop is controlled by conversation %q for up to %s more; stop/release that conversation or use Take control, then retry after resuming", lease.tool.thread.id, time.Until(lease.until).Round(time.Second))
		}
		if !lease.owner.opMu.TryLock() {
			s.automationMu.Unlock()
			return fmt.Errorf("previous desktop input is still draining; retry after it finishes")
		}
		lease.cleaning = true
		s.desktopOwners[device] = lease
		s.automationMu.Unlock()
		lease.owner.screens = nil
		closeDesktopAutomation(lease.owner)
		lease.owner.opMu.Unlock()
		s.automationMu.Lock()
		current := s.desktopOwners[device]
		if current.owner != lease.owner || current.until != lease.until || current.manual > 0 {
			s.automationMu.Unlock()
			return fmt.Errorf("manual desktop input is in progress; retry after it finishes")
		}
	}
	if lease.timer != nil {
		lease.timer.Stop()
	}
	until := time.Now().Add(2 * time.Minute)
	lease = desktopAutomationLease{owner: t.state, tool: *t, until: until}
	lease.timer = time.AfterFunc(time.Until(until), func() { s.expireAutomationDesktop(device, t.state, until) })
	s.desktopOwners[device] = lease
	s.automationMu.Unlock()
	return nil
}

func (s *Server) expireAutomationDesktop(device string, state *agentAutomation, until time.Time) {
	state.opMu.Lock()
	defer state.opMu.Unlock()
	s.automationMu.Lock()
	lease := s.desktopOwners[device]
	if lease.owner != state || lease.until != until || lease.cleaning || lease.manual > 0 || time.Now().Before(until) {
		s.automationMu.Unlock()
		return
	}
	lease.cleaning = true
	s.desktopOwners[device] = lease
	s.automationMu.Unlock()
	state.screens = nil
	closeDesktopAutomation(state)
	s.automationMu.Lock()
	if current := s.desktopOwners[device]; current.owner == state && current.until == until && current.manual == 0 {
		delete(s.desktopOwners, device)
	}
	s.automationMu.Unlock()
}
func (s *Server) interruptDesktopAutomation(device string) {
	s.beginDesktopManualInput(device)()
}

// The caller holds this device-wide reservation until human input is sent.
func (s *Server) beginDesktopManualInput(device string) func() {
	s.automationMu.Lock()
	if s.desktopOwners == nil {
		s.desktopOwners = make(map[string]desktopAutomationLease)
	}
	lease := s.desktopOwners[device]
	lease.manual++
	if lease.timer != nil {
		lease.timer.Stop()
	}
	s.desktopOwners[device] = lease
	s.automationMu.Unlock()
	if lease.owner != nil {
		lease.owner.mu.Lock()
		lease.owner.manualInputs++
		if lease.owner.cancel != nil {
			lease.owner.cancel()
		}
		view := lease.owner.view
		lease.owner.mu.Unlock()
		lease.owner.opMu.Lock()
		lease.owner.screens = nil
		closeDesktopAutomation(lease.owner)
		lease.owner.opMu.Unlock()
		lease.tool.publish(view)
	}
	return func() {
		if lease.owner != nil {
			lease.owner.mu.Lock()
			lease.owner.manualInputs--
			lease.owner.mu.Unlock()
		}
		s.automationMu.Lock()
		current := s.desktopOwners[device]
		current.manual--
		if current.manual <= 0 {
			delete(s.desktopOwners, device)
		} else {
			s.desktopOwners[device] = current
		}
		s.automationMu.Unlock()
	}
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
	if t.thread.deviceScope != nil {
		scoped, release, scopeErr := t.thread.deviceScope.begin(ctx, a.Device)
		if scopeErr != nil {
			return "", scopeErr
		}
		defer release()
		ctx = scoped
	}
	client, err := t.client(a.Device)
	if err != nil {
		return "", err
	}
	bound := *t
	bound.automationTool = t.forResource("desktop:" + client.ID)
	t = &bound

	if a.Action == "native_tools" || a.Action == "native" {
		if client.Desktop != nil && client.Desktop.NativeCua {
			return t.executeNativeCua(ctx, client, a, raw)
		}
		if !t.srv.clientGPUDesktopAvailable(client) {
			return "", fmt.Errorf("native Cua tools unsupported: use a Cua-capable Go cgo or Rust client; pure-Go Go clients support screenshot/input only")
		}
		return t.executeGPU(ctx, client, a, raw)
	}
	if a.Action == "status" && t.srv.clientGPUDesktopAvailable(client) {
		return t.executeGPU(ctx, client, a, raw)
	}
	if a.Action == "status" {
		return marshalJobResult(map[string]any{"device": client.ID, "desktop": client.Desktop, "cua": client.Desktop != nil && client.Desktop.NativeCua})
	}
	if a.Action == "release" {
		if !t.state.opMu.TryLock() {
			return "", fmt.Errorf("wait for the current desktop operation before releasing")
		}
		defer t.state.opMu.Unlock()
		if d := t.state.desktop[client.ID]; d != nil {
			d.close()
			delete(t.state.desktop, client.ID)
		}
		delete(t.state.screens, client.ID)
		if d := t.state.gpu[client.ID]; d != nil {
			d.close()
			delete(t.state.gpu, client.ID)
		}
		t.srv.releaseAutomationDesktop(t.state)
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
	success := false
	defer func() {
		if !success {
			t.srv.clearAutomationDesktop(t.state)
		}
	}()
	screen := t.state.screens[client.ID]
	if a.Source == "" && screen.client == client {
		a.Source = screen.source
	}
	refreshedForSource := false
	if a.Action != "screenshot" && screen.client == client {
		if a.Source == "" {
			a.Source = screen.source
		}
		if a.Source != screen.source {
			// A source switch invalidates old coordinates. Return a fresh frame
			// rather than applying input grounded in a different screen.
			refreshedForSource = true
			a.Action = "screenshot"
		}
	}
	if err = t.claim(client.ID); err != nil {
		return "", err
	}
	t.publish(automationView{Kind: "desktop", Device: client.ID, Source: a.Source, Action: a.Action})
	session, err := t.openDesktop(ctx, client, a.Source)
	if err != nil {
		return "", err
	}
	id, capture, request, ready := session.id, session.capture, session.request, session.ready
	defer func() {
		if !request.OnDemand || !success {
			session.close()
			delete(t.state.desktop, client.ID)
		}
	}()
	if ready.Width <= 0 || ready.Height <= 0 {
		return "", fmt.Errorf("invalid desktop dimensions")
	}
	if a.Action != "screenshot" && screen.client == client && screen.width > 0 && screen.height > 0 && (ready.Width != screen.width || ready.Height != screen.height) {
		refreshedForSource = true
		a.Action = "screenshot"
	}
	var refreshedFrame []byte
	if request.OnDemand && (a.Action == "click" || a.Action == "drag" || a.Action == "scroll") {
		refreshedFrame, err = t.freshDesktopFrame(ctx, client, session)
		if err != nil {
			return "", err
		}
		cfg, decodeErr := jpeg.DecodeConfig(bytes.NewReader(refreshedFrame))
		if decodeErr != nil || cfg.Width <= 0 || cfg.Height <= 0 {
			return "", fmt.Errorf("invalid fresh desktop frame")
		}
		if screen.client == client && screen.width > 0 && screen.height > 0 && (cfg.Width != screen.width || cfg.Height != screen.height) {
			// A resolution change invalidates grounded coordinates. Return this
			// exact fresh frame rather than rejecting or applying stale input.
			refreshedForSource = true
			a.Action = "screenshot"
		}
		ready.Width, ready.Height = cfg.Width, cfg.Height
	}
	if a.Action != "screenshot" && client.Desktop.NativeCua {
		result, handled, err := preferComputerCua(ctx, session.cuaRequest, client, a, raw, ready.Width, ready.Height)
		if handled {
			success = err == nil && !cuaResultFailed(json.RawMessage(result))
			return result, err
		}
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
		frame := refreshedFrame
		if frame == nil {
			if request.OnDemand {
				if err := client.Send(&protocol.Message{Type: protocol.MsgDesktopSnapshot, SessionID: id}); err != nil {
					return "", err
				}
			}
			select {
			case msg := <-capture.messages:
				return "", fmt.Errorf("desktop: %s", msg.Error)
			case <-ctx.Done():
				return "", ctx.Err()
			case <-capture.done:
				return "", fmt.Errorf("device disconnected")
			case frame = <-capture.frames:
			}
		}
		result, err := automationImage(frame)
		if err == nil {
			cfg, decodeErr := jpeg.DecodeConfig(bytes.NewReader(frame))
			if decodeErr != nil || cfg.Width <= 0 || cfg.Height <= 0 {
				return "", fmt.Errorf("invalid desktop frame dimensions")
			}
			if t.state.screens == nil {
				t.state.screens = make(map[string]automationScreen)
			}
			t.state.screens[client.ID] = automationScreen{client: client, source: a.Source, width: cfg.Width, height: cfg.Height, at: time.Now()}
		}
		success = err == nil
		if err == nil && refreshedForSource {
			result = "The display source or dimensions changed. This is a fresh screenshot; discard previous coordinates and inspect it before input.\n" + result
		}
		return result, err
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
	success = true
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

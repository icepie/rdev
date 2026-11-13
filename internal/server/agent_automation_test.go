package server

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/jpeg"
	"strings"
	"testing"
	"time"

	"rdev/internal/protocol"
)

func testAutomation(t *testing.T, s *Server, device, thread string) automationTool {
	t.Helper()
	entry := &agentSessionEntry{deviceID: device, threads: make(map[string]*agentThread)}
	th := &agentThread{id: thread, automation: &agentAutomation{pages: make(map[string]automationPage)}}
	entry.threads[thread] = th
	return automationTool{srv: s, entry: entry, thread: th, state: th.automation}
}
func TestAutomationDeviceAuthorization(t *testing.T) {
	s := NewServer()
	s.clients = map[string]*ClientConn{"one": {ID: "one"}, "protected": {ID: "protected", Password: "password"}}
	bound := testAutomation(t, s, "one", "a")
	for _, device := range []string{"protected", "@server", "absent"} {
		if _, err := bound.client(device); err == nil {
			t.Fatalf("cross-device access: %s", device)
		}
	}
	if c, err := bound.client(""); err != nil || c.ID != "one" {
		t.Fatalf("default device: %v", err)
	}
	server := testAutomation(t, s, "@server", "b")
	for _, device := range []string{"", "@server", "protected", "absent"} {
		if _, err := server.client(device); err == nil {
			t.Fatalf("unauthorized server routing: %s", device)
		}
	}
	if _, err := server.client("one"); err != nil {
		t.Fatal(err)
	}
}
func TestManualInputCancelsActionWithoutPausingAutomation(t *testing.T) {
	s := NewServer()
	base := testAutomation(t, s, "device", "a")
	tool := computerTool{base}
	ctx, finish, err := base.begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := base.begin(context.Background()); err == nil {
		t.Fatal("concurrent operation accepted")
	}
	if err := tool.claim("device"); err != nil {
		t.Fatal(err)
	}
	other := computerTool{testAutomation(t, s, "device", "b")}
	if err := other.claim("device"); err == nil {
		t.Fatal("another thread took screen")
	}
	returned := make(chan struct{})
	go func() { s.interruptDesktopAutomation("device"); close(returned) }()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("takeover did not cancel")
	}
	select {
	case <-returned:
		t.Fatal("takeover acknowledged before operation released")
	default:
	}
	finish()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("takeover hung")
	}
	if _, finish, err := base.begin(context.Background()); err != nil {
		t.Fatal("manual input left automation paused", err)
	} else {
		finish()
	}
	if err := other.claim("device"); err != nil {
		t.Fatal("lease not released", err)
	}
}
func TestComputerInputWithoutScreenshotAndCoordinateValidation(t *testing.T) {
	s, client, transport := newProxyTestServer(t, "device", "")
	client.Desktop = &protocol.DesktopCapabilities{Supported: true, Input: true, Clipboard: true, Sources: []protocol.DesktopSource{{ID: "screen:primary"}}}
	base := testAutomation(t, s, "device", "a")
	tool := computerTool{base}
	var frame bytes.Buffer
	_ = jpeg.Encode(&frame, image.NewRGBA(image.Rect(0, 0, 1280, 720)), nil)
	transport.onSend = func(m *protocol.Message) {
		switch m.Type {
		case protocol.MsgDesktopStart:
			s.handleDesktopMessage(&protocol.Message{Type: protocol.MsgDesktopReady, SessionID: m.SessionID, Width: 1280, Height: 720, Format: "mjpeg"})
			s.handleDesktopFrame(m.SessionID, frame.Bytes())
		case protocol.MsgDesktopClipboard:
			s.handleDesktopMessage(&protocol.Message{Type: protocol.MsgDesktopClipboard, SessionID: m.SessionID, ClipboardAction: "content"})
		}
	}
	run := func(raw string) (string, error) { return tool.Execute(context.Background(), json.RawMessage(raw)) }
	if _, err := run(`{"action":"click","x":10,"y":20}`); err != nil {
		t.Fatalf("input without a prior screenshot: %v", err)
	}
	result, err := run(`{"action":"screenshot","source":"screen:primary"}`)
	if err != nil || !strings.HasPrefix(result, "[image preview]") {
		t.Fatalf("capture: %v", err)
	}
	if _, err := run(`{"action":"click","x":10,"y":20}`); err != nil {
		t.Fatal(err)
	}
	if _, err := run(`{"action":"key","key":"Enter"}`); err != nil {
		t.Fatalf("repeated input required a new screenshot: %v", err)
	}
	if _, err := run(`{"action":"screenshot"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := run(`{"action":"drag","x":10,"y":20,"toX":40,"toY":50}`); err != nil {
		t.Fatal(err)
	}
	if _, err := run(`{"action":"screenshot"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := run(`{"action":"text","text":"你好🌏"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := run(`{"action":"click","x":1280,"y":20}`); err == nil {
		t.Fatal("out of bounds coordinate accepted")
	}
	if _, err := run(`{"action":"click","source":"other","x":10,"y":20}`); err == nil {
		t.Fatal("unknown screen accepted")
	}
	transport.mu.Lock()
	var inputs []string
	for _, m := range transport.msgs {
		if m.Type == protocol.MsgDesktopInput {
			inputs = append(inputs, m.InputType)
		}
	}
	transport.mu.Unlock()
	if strings.Join(inputs, ",") != "mouse_down,mouse_up,mouse_down,mouse_up,key_down,key_up,mouse_down,mouse_move,mouse_up,key_down,key_up" {
		t.Fatalf("input order: %v", inputs)
	}
	s.desktopMu.RLock()
	routes := len(s.desktops)
	s.desktopMu.RUnlock()
	if routes != 0 {
		t.Fatal("capture route leaked")
	}
	s.mu.Lock()
	s.clients[client.ID] = &ClientConn{ID: client.ID, Desktop: client.Desktop, Transport: transport}
	s.mu.Unlock()
	if _, err := run(`{"action":"click","x":10,"y":20}`); err != nil {
		t.Fatalf("input after reconnect required a screenshot: %v", err)
	}
}

func TestComputerSourceChangeRefreshesBeforeInput(t *testing.T) {
	s, client, transport := newProxyTestServer(t, "device", "")
	client.Desktop = &protocol.DesktopCapabilities{AgentVersion: 1, Supported: true, Input: true, Sources: []protocol.DesktopSource{{ID: "screen:primary"}, {ID: "screen:secondary"}}}
	base := testAutomation(t, s, "device", "source-switch")
	defer closeDesktopAutomation(base.state)
	tool := computerTool{base}
	var inputs, snapshots int
	width, height := 1280, 720
	transport.onSend = func(m *protocol.Message) {
		switch m.Type {
		case protocol.MsgDesktopStart:
			s.handleDesktopMessage(&protocol.Message{Type: protocol.MsgDesktopReady, SessionID: m.SessionID, Width: width, Height: height})
		case protocol.MsgDesktopSnapshot:
			snapshots++
			var frame bytes.Buffer
			_ = jpeg.Encode(&frame, image.NewRGBA(image.Rect(0, 0, width, height)), nil)
			s.handleDesktopFrame(m.SessionID, frame.Bytes())
		case protocol.MsgDesktopInput:
			inputs++
		}
	}
	run := func(raw string) (string, error) { return tool.Execute(context.Background(), json.RawMessage(raw)) }
	if _, err := run(`{"action":"screenshot","source":"screen:primary"}`); err != nil {
		t.Fatal(err)
	}
	width = 800
	result, err := run(`{"action":"click","source":"screen:primary","x":10,"y":20}`)
	if err != nil || !strings.HasPrefix(result, "The display source or dimensions changed.") {
		t.Fatalf("resolution change did not return a fresh observation: %s %v", result, err)
	}
	if inputs != 0 || snapshots != 2 || base.state.screens[client.ID].width != width {
		t.Fatalf("stale input was sent or resolution was not refreshed: inputs=%d snapshots=%d screen=%+v", inputs, snapshots, base.state.screens[client.ID])
	}
	result, err = run(`{"action":"click","source":"screen:secondary","x":10,"y":20}`)
	if err != nil || !strings.HasPrefix(result, "The display source or dimensions changed.") {
	}
	if inputs != 0 || snapshots != 3 || base.state.screens[client.ID].source != "screen:secondary" {
		t.Fatalf("stale input was sent or screen was not refreshed: inputs=%d snapshots=%d screen=%+v", inputs, snapshots, base.state.screens[client.ID])
	}
	if _, err := run(`{"action":"click","source":"screen:secondary","x":10,"y":20}`); err != nil {
		t.Fatal("input after fresh screen observation:", err)
	}
	if inputs != 2 { // mouse down + mouse up
		t.Fatalf("freshly grounded click sent %d inputs", inputs)
	}
}
func TestBrowserNavigationURL(t *testing.T) {
	for _, raw := range []string{"file:///etc/passwd", "chrome://settings", "javascript:alert(1)", "http://u:p@example.test", "example.test"} {
		if browserNavigationURL(raw) == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	for _, raw := range []string{"https://example.test/path", "http://127.0.0.1:9000"} {
		if err := browserNavigationURL(raw); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAutomationRetirementReleasesDesktopAndRejectsOldTools(t *testing.T) {
	s := NewServer()
	base := testAutomation(t, s, "device", "a")
	tool := computerTool{base}
	if err := tool.claim("device"); err != nil {
		t.Fatal(err)
	}
	s.retireAutomation(base.state)
	s.agentWG.Wait()
	if _, _, err := base.begin(context.Background()); err == nil {
		t.Fatal("retired tools can run")
	}
	base.state.mu.Lock()
	base.state.paused = false // a delayed resume must not revive old tools
	base.state.mu.Unlock()
	if err := base.claim("device"); err == nil {
		t.Fatal("retired tool reclaimed ownership")
	}
	child := base.forResource("desktop:other")
	if _, _, err := child.begin(context.Background()); err == nil {
		t.Fatal("new child of retired root can run")
	}
	s.automationMu.Lock()
	owners := len(s.desktopOwners)
	s.automationMu.Unlock()
	if owners != 0 {
		t.Fatal("retired conversation owns desktop")
	}
}

func TestBrowserStatusDoesNotLaunchChrome(t *testing.T) {
	s, _, _ := newProxyTestServer(t, "device", "")
	s.BrowserEnabled = true
	t.Setenv("RDEV_CHROME_PATH", "/missing/test-chrome")
	tool := browserTool{testAutomation(t, s, "device", "a")}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"action":"status"}`))
	if err != nil || !strings.Contains(result, `"running":false`) {
		t.Fatalf("status: %s %v", result, err)
	}
	s.browserMu.Lock()
	started := len(s.browsers)
	s.browserMu.Unlock()
	if started != 0 {
		t.Fatal("status started Chrome")
	}
}

func TestComputerOnDemandSessionReuseAndRelease(t *testing.T) {
	s, client, transport := newProxyTestServer(t, "device", "")
	client.Desktop = &protocol.DesktopCapabilities{AgentVersion: 1, Supported: true, Input: true, Sources: []protocol.DesktopSource{{ID: "screen:primary"}, {ID: "screen:secondary"}}}
	base := testAutomation(t, s, "device", "native")
	defer closeDesktopAutomation(base.state)
	tool := computerTool{base}
	var frame bytes.Buffer
	_ = jpeg.Encode(&frame, image.NewRGBA(image.Rect(0, 0, 1280, 720)), nil)
	starts, shots, closes := 0, 0, 0
	transport.onSend = func(m *protocol.Message) {
		switch m.Type {
		case protocol.MsgDesktopStart:
			starts++
			if !m.OnDemand {
				t.Error("continuous capture requested")
			}
			if m.Source != "screen:secondary" {
				t.Errorf("capture switched away from selected monitor: %q", m.Source)
			}
			s.handleDesktopMessage(&protocol.Message{Type: protocol.MsgDesktopReady, SessionID: m.SessionID, Width: 1280, Height: 720})
		case protocol.MsgDesktopSnapshot:
			shots++
			s.handleDesktopFrame(m.SessionID, frame.Bytes())
		case protocol.MsgDesktopClose:
			closes++
		}
	}
	for _, args := range []string{`{"action":"screenshot","source":"screen:secondary"}`, `{"action":"click","x":2,"y":3}`, `{"action":"screenshot"}`, `{"action":"key","key":"Enter"}`, `{"action":"screenshot"}`} {
		if _, err := tool.Execute(context.Background(), json.RawMessage(args)); err != nil {
			t.Fatal(err)
		}
	}
	if starts != 1 || shots != 4 || closes != 0 {
		t.Fatalf("starts=%d shots=%d closes=%d", starts, shots, closes)
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"action":"release"}`)); err != nil {
		t.Fatal(err)
	}
	if closes != 1 || len(s.desktops) != 0 || len(base.state.desktop) != 0 {
		t.Fatal("release leaked capture")
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"action":"click","source":"screen:secondary","x":2,"y":3}`)); err != nil {
		t.Fatalf("input after release required a screenshot: %v", err)
	}
	if starts != 2 || shots != 5 || closes != 1 {
		t.Fatalf("post-release input did not start a new session: starts=%d shots=%d closes=%d", starts, shots, closes)
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"action":"release"}`)); err != nil {
		t.Fatal(err)
	}
	if closes != 2 || len(s.desktops) != 0 || len(base.state.desktop) != 0 {
		t.Fatal("second release leaked capture")
	}
}

func TestAutomationResourcesRunIndependentlyAndManualInputIsScoped(t *testing.T) {
	srv := NewServer()
	root := testAutomation(t, srv, "@server", "parallel")
	a := root.forResource("desktop:a")
	b := root.forResource("desktop:b")
	browser := root.forResource("browser:a")
	aCtx, doneA, err := a.begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	bCtx, doneB, err := b.begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer doneB()
	_, doneBrowser, err := browser.begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer doneBrowser()
	duplicate := root.forResource("desktop:a")
	if _, _, err := duplicate.begin(context.Background()); err == nil {
		t.Fatal("same resource ran concurrently")
	}
	if err := a.claim("a"); err != nil {
		t.Fatal(err)
	}
	interrupted := make(chan struct{})
	go func() { srv.interruptDesktopAutomation("a"); close(interrupted) }()
	select {
	case <-aCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("input did not cancel target")
	}
	if bCtx.Err() != nil {
		t.Fatal("manual input canceled another device")
	}
	doneA()
	select {
	case <-interrupted:
	case <-time.After(time.Second):
		t.Fatal("input drain stuck")
	}
}

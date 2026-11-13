package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"rdev/internal/protocol"
)

func computerCuaTestInventory() json.RawMessage {
	return json.RawMessage(`{"tools":[
		{"name":"click","inputSchema":{"properties":{"target":{},"delivery_mode":{},"x":{},"y":{},"button":{}},"required":["target","delivery_mode"]}},
		{"name":"drag","inputSchema":{"properties":{"target":{},"from_x":{},"from_y":{},"to_x":{},"to_y":{},"button":{}}}},
		{"name":"scroll","inputSchema":{"properties":{"target":{},"x":{},"y":{},"direction":{},"by":{},"amount":{}}}},
		{"name":"press_key","inputSchema":{"properties":{"target":{},"key":{},"modifiers":{}}}},
		{"name":"type_text","inputSchema":{"properties":{"target":{},"text":{}}}},
		{"name":"get_desktop_state","inputSchema":{"properties":{}}}
	]}`)
}

func TestComputerCuaPreferredActions(t *testing.T) {
	client := &ClientConn{OS: "linux", Desktop: &protocol.DesktopCapabilities{Sources: []protocol.DesktopSource{{ID: "primary", Primary: true, Kind: "monitor", Width: 1920, Height: 1080}}}}
	for _, tc := range []struct {
		raw   string
		name  string
		check func(*testing.T, map[string]any)
	}{
		{`{"action":"click","source":"primary","x":100,"y":50,"button":2}`, "click", func(t *testing.T, a map[string]any) {
			if a["x"] != float64(200) || a["y"] != float64(100) || a["button"] != "right" || a["delivery_mode"] != "foreground" {
				t.Fatalf("click args: %#v", a)
			}
		}},
		{`{"action":"drag","source":"primary","x":10,"y":20,"toX":100,"toY":200,"button":1}`, "drag", func(t *testing.T, a map[string]any) {
			if a["from_x"] != float64(20) || a["to_y"] != float64(400) || a["button"] != "middle" {
				t.Fatalf("drag args: %#v", a)
			}
		}},
		{`{"action":"scroll","source":"primary","x":10,"y":20,"deltaY":-240}`, "scroll", func(t *testing.T, a map[string]any) {
			if a["direction"] != "up" || a["by"] != "line" || a["amount"] != 2 {
				t.Fatalf("scroll args: %#v", a)
			}
		}},
		{`{"action":"key","key":"ArrowLeft","ctrl":true,"shift":true,"meta":true}`, "press_key", func(t *testing.T, a map[string]any) {
			if a["key"] != "left" || strings.Join(a["modifiers"].([]string), ",") != "ctrl,shift,super" {
				t.Fatalf("key args: %#v", a)
			}
		}},
		{`{"action":"text","text":"你好 CUA"}`, "type_text", func(t *testing.T, a map[string]any) {
			if a["text"] != "你好 CUA" {
				t.Fatalf("text args: %#v", a)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var a computerAction
			if err := json.Unmarshal([]byte(tc.raw), &a); err != nil {
				t.Fatal(err)
			}
			calls := 0
			request := func(_ context.Context, value any) (json.RawMessage, error) {
				v := value.(map[string]any)
				if v["action"] == "tools" {
					return computerCuaTestInventory(), nil
				}
				if v["name"] == "get_desktop_state" {
					return json.RawMessage(`{"structuredContent":{"screenshot_width":1920,"screenshot_height":1080,"screen_width":1920,"screen_height":1080}}`), nil
				}
				calls++
				if v["name"] != tc.name {
					t.Fatalf("tool: %v", v)
				}
				tc.check(t, v["arguments"].(map[string]any))
				return json.RawMessage(`{"content":[],"isError":false}`), nil
			}
			_, handled, err := preferComputerCua(context.Background(), request, client, a, []byte(tc.raw), 960, 540)
			if err != nil || !handled || calls != 1 {
				t.Fatalf("handled=%v calls=%d err=%v", handled, calls, err)
			}
		})
	}
}

func TestComputerCuaUnsupportedAndUncertain(t *testing.T) {
	client := &ClientConn{OS: "linux"}
	for _, tc := range []struct {
		name      string
		inventory json.RawMessage
		result    json.RawMessage
		err       error
		handled   bool
		wantErr   bool
	}{
		{"missing-tool", json.RawMessage(`{"tools":[]}`), nil, nil, false, false},
		{"incompatible-schema", json.RawMessage(`{"tools":[{"name":"type_text","inputSchema":{"properties":{"text":{}},"required":["pid"]}}]}`), nil, nil, false, false},
		{"denied", computerCuaTestInventory(), json.RawMessage(`{"isError":true,"content":[{"type":"text","text":"permission denied"}]}`), nil, true, false},
		{"uncertain", computerCuaTestInventory(), nil, errors.New("device disconnected after send"), true, true},
		{"malformed-inventory", json.RawMessage(`{}`), nil, nil, true, true},
		{"discovery-denied", json.RawMessage(`{"isError":true}`), nil, nil, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			request := func(_ context.Context, value any) (json.RawMessage, error) {
				if value.(map[string]any)["action"] == "tools" {
					return tc.inventory, nil
				}
				calls++
				return tc.result, tc.err
			}
			_, handled, err := preferComputerCua(context.Background(), request, client, computerAction{Action: "text", Text: "hello"}, nil, 100, 100)
			if handled != tc.handled || (err != nil) != tc.wantErr || calls > 1 {
				t.Fatalf("handled=%v calls=%d err=%v", handled, calls, err)
			}
		})
	}
	// A name-only GPU source, secondary screen, and unrepresentable key/scroll
	// shapes must never issue a native action or guess the primary monitor.
	for _, raw := range []string{
		`{"action":"click","source":"gpu:0","x":5,"y":5}`,
		`{"action":"drag","source":"gpu:1","x":5,"y":5,"toX":10,"toY":10}`,
		`{"action":"key","key":"AudioVolumeUp"}`,
	} {
		var a computerAction
		_ = json.Unmarshal([]byte(raw), &a)
		_, handled, err := preferComputerCua(context.Background(), func(context.Context, any) (json.RawMessage, error) {
			t.Fatal("unexpected native request")
			return nil, nil
		}, client, a, []byte(raw), 100, 100)
		if handled || err != nil {
			t.Fatalf("unsupported shape: handled=%v err=%v", handled, err)
		}
	}
}

func TestComputerNativePreferenceDoesNotBypassGatesOrRetry(t *testing.T) {
	for _, mode := range []string{"preferred", "unsupported", "denied", "uncertain", "stale", "no-screenshot", "legacy-no-screenshot", "view-only"} {
		t.Run(mode, func(t *testing.T) {
			srv, client, transport := newProxyTestServer(t, "cua-device", "")
			client.Desktop = &protocol.DesktopCapabilities{Supported: true, Input: true, NativeCua: mode != "legacy-no-screenshot", AgentVersion: 1, Sources: []protocol.DesktopSource{{ID: "auto"}}, ViewOnly: mode == "view-only"}
			tool := &computerTool{automationTool: testAutomation(t, srv, client.ID, "thread")}
			t.Cleanup(func() { tool.state.opMu.Lock(); closeDesktopAutomation(tool.state); tool.state.opMu.Unlock() })
			at := time.Now()
			if mode == "stale" {
				at = at.Add(-3 * time.Minute)
			}
			if mode != "no-screenshot" && mode != "legacy-no-screenshot" {
				tool.state.screens = map[string]automationScreen{client.ID: {client: client, source: "auto", width: 1280, height: 720, at: at}}
			}
			native, legacy := 0, 0
			transport.onSend = func(msg *protocol.Message) {
				switch msg.Type {
				case protocol.MsgDesktopStart:
					srv.handleDesktopMessage(&protocol.Message{Type: protocol.MsgDesktopReady, SessionID: msg.SessionID, Width: 1280, Height: 720})
				case protocol.MsgDesktopInput:
					legacy++
				case protocol.MsgDesktopCua:
					var request struct {
						Action string `json:"action"`
					}
					_ = json.Unmarshal(msg.CuaRequest, &request)
					result, message := computerCuaTestInventory(), ""
					if mode == "unsupported" {
						result = json.RawMessage(`{"tools":[]}`)
					}
					if request.Action == "call" {
						native++
						result = json.RawMessage(`{"content":[],"isError":false}`)
						if mode == "denied" {
							result = json.RawMessage(`{"isError":true}`)
						}
						if mode == "uncertain" {
							message = "connection lost after dispatch"
						}
					}
					srv.handleDesktopMessage(&protocol.Message{Type: protocol.MsgDesktopCuaResult, SessionID: msg.SessionID, RequestID: msg.RequestID, CuaResult: result, Error: message})
				}
			}
			_, err := tool.Execute(context.Background(), json.RawMessage(`{"action":"key","key":"Enter"}`))
			wantErr := mode == "uncertain" || mode == "view-only"
			if (err != nil) != wantErr {
				t.Fatalf("err=%v", err)
			}
			if mode == "unsupported" || mode == "legacy-no-screenshot" {
				if legacy != 2 || (mode == "unsupported" && native != 0) {
					t.Fatalf("legacy=%d native=%d", legacy, native)
				}
			} else if legacy != 0 {
				t.Fatalf("fallback after native/gate: %d", legacy)
			}
			if mode == "preferred" || mode == "denied" || mode == "uncertain" || mode == "stale" || mode == "no-screenshot" {
				if native != 1 {
					t.Fatalf("native calls=%d", native)
				}
			}
			if mode == "preferred" || mode == "stale" || mode == "no-screenshot" || mode == "legacy-no-screenshot" {
				if _, err := tool.Execute(context.Background(), json.RawMessage(`{"action":"key","key":"Enter"}`)); err != nil {
					t.Fatalf("repeated input required another screenshot: %v", err)
				}
			}
		})
	}
}

func TestComputerGPUPreferenceAndUnsupportedFallback(t *testing.T) {
	for _, mode := range []string{"preferred", "unsupported", "denied", "uncertain", "no-capability", "unmapped-source"} {
		t.Run(mode, func(t *testing.T) {
			fixture := &gpuAgentFixture{cua: mode != "no-capability", nativeTools: computerCuaTestInventory(), nativeResult: json.RawMessage(`{"CuaResult":{"result":{"content":[],"isError":false}}}`), nativeRequests: make(chan json.RawMessage, 8), inputs: make(chan string, 8)}
			if mode == "unsupported" {
				fixture.nativeTools = json.RawMessage(`{"tools":[]}`)
			}
			if mode == "denied" {
				fixture.nativeResult = json.RawMessage(`{"CuaResult":{"result":{"isError":true}}}`)
			}
			if mode == "uncertain" {
				fixture.nativeResult = json.RawMessage(`{"CuaResult":{"error":"uncertain delivery"}}`)
			}
			tool, _ := newGPUCuaTool(t, fixture)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := tool.Execute(ctx, json.RawMessage(`{"action":"screenshot"}`)); err != nil {
				t.Fatal(err)
			}
			raw := json.RawMessage(`{"action":"text","text":"native Unicode 你好"}`)
			if mode == "unmapped-source" {
				raw = json.RawMessage(`{"action":"click","x":20,"y":20}`)
			}
			_, err := tool.Execute(ctx, raw)
			if (err != nil) != (mode == "uncertain") {
				t.Fatalf("err=%v", err)
			}
			// A subsequent capture is a transport barrier for legacy input too.
			if _, err := tool.Execute(ctx, json.RawMessage(`{"action":"screenshot"}`)); err != nil {
				t.Fatal(err)
			}
			calls := 0
			for len(fixture.nativeRequests) > 0 {
				var req struct {
					Action string `json:"action"`
					Name   string `json:"name"`
				}
				_ = json.Unmarshal(<-fixture.nativeRequests, &req)
				if req.Action == "call" {
					calls++
					if req.Name != "type_text" {
						t.Fatalf("unexpected native action: %s", req.Name)
					}
				}
			}
			fallback := mode == "unsupported" || mode == "no-capability" || mode == "unmapped-source"
			if fallback {
				if calls != 0 || len(fixture.inputs) == 0 {
					t.Fatalf("missing fallback: calls=%d inputs=%d", calls, len(fixture.inputs))
				}
			} else if calls != 1 || len(fixture.inputs) != 0 {
				t.Fatalf("native retry/fallback: calls=%d inputs=%d", calls, len(fixture.inputs))
			}
		})
	}
}

func TestComputerCuaPlatformModifiers(t *testing.T) {
	for platform, meta := range map[string]string{"linux": "super", "darwin": "cmd", "windows": "win"} {
		key, modifiers, ok := computerCuaKey(computerAction{Key: "Enter", Ctrl: true, Alt: true, Shift: true, Meta: true}, platform)
		if !ok || key != "return" || strings.Join(modifiers, ",") != "ctrl,alt,shift,"+meta {
			t.Fatalf("%s: key=%s modifiers=%v supported=%v", platform, key, modifiers, ok)
		}
	}
}

func TestComputerGPUPreferredTakeoverDoesNotFallback(t *testing.T) {
	fixture := &gpuAgentFixture{cua: true, nativeTools: computerCuaTestInventory(), nativeBlock: true, nativeRequests: make(chan json.RawMessage, 8), inputs: make(chan string, 8)}
	tool, client := newGPUCuaTool(t, fixture)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := tool.Execute(ctx, json.RawMessage(`{"action":"screenshot"}`)); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := tool.Execute(ctx, json.RawMessage(`{"action":"text","text":"one action"}`))
		result <- err
	}()
	for range 2 {
		select {
		case <-fixture.nativeRequests:
		case <-ctx.Done():
			t.Fatal("native action was not dispatched")
		}
	}
	tool.srv.interruptDesktopAutomation(client.ID)
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("takeover did not cancel native call")
		}
	case <-ctx.Done():
		t.Fatal("takeover did not drain native call")
	}
	if len(fixture.inputs) != 0 {
		t.Fatal("takeover triggered legacy fallback")
	}
}

func TestComputerCuaCoordinateCapabilityBoundaries(t *testing.T) {
	client := &ClientConn{Desktop: &protocol.DesktopCapabilities{Sources: []protocol.DesktopSource{{ID: "primary", Primary: true, Kind: "monitor", Width: 1920, Height: 1080}}}}
	for _, tc := range []struct {
		name        string
		raw         string
		observation json.RawMessage
		handled     bool
		wantErr     bool
	}{
		{"different-desktop", `{"action":"click","source":"primary","x":10,"y":10}`, json.RawMessage(`{"structuredContent":{"screenshot_width":1280,"screenshot_height":720,"screen_width":1280,"screen_height":720}}`), false, false},
		{"missing-dimensions", `{"action":"click","source":"primary","x":10,"y":10}`, json.RawMessage(`{"content":[]}`), true, true},
		{"capture-denied", `{"action":"click","source":"primary","x":10,"y":10}`, json.RawMessage(`{"isError":true}`), true, true},
		{"dual-axis", `{"action":"scroll","source":"primary","x":10,"y":10,"deltaX":120,"deltaY":120}`, nil, false, false},
		{"fractional-wheel", `{"action":"scroll","source":"primary","x":10,"y":10,"deltaY":30}`, nil, false, false},
		{"missing-coordinate", `{"action":"click","source":"primary","y":10}`, nil, true, true},
		{"outside-frame", `{"action":"click","source":"primary","x":1000,"y":10}`, nil, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var a computerAction
			_ = json.Unmarshal([]byte(tc.raw), &a)
			request := func(_ context.Context, value any) (json.RawMessage, error) {
				v := value.(map[string]any)
				if v["action"] == "tools" {
					return computerCuaTestInventory(), nil
				}
				if v["name"] != "get_desktop_state" {
					t.Fatalf("unsafe native action: %#v", v)
				}
				return tc.observation, nil
			}
			_, handled, err := preferComputerCua(context.Background(), request, client, a, []byte(tc.raw), 960, 540)
			if handled != tc.handled || (err != nil) != tc.wantErr {
				t.Fatalf("handled=%v err=%v", handled, err)
			}
		})
	}
}

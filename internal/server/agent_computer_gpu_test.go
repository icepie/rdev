package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lxzan/gws"
	"rdev/internal/protocol"
)

type gpuAgentFixture struct {
	gws.BuiltinEventHandler
	connections    atomic.Int32
	configs        atomic.Int32
	snapshots      atomic.Int32
	inputs         chan string
	cua            bool
	nativeResult   json.RawMessage
	nativeTools    json.RawMessage
	nativeBlock    bool
	nativeRequests chan json.RawMessage
	requests       atomic.Int32
}

func (h *gpuAgentFixture) OnOpen(c *gws.Conn) {
	h.connections.Add(1)
	caps, _ := json.Marshal(map[string]any{"AgentCapabilities": map[string]any{"version": 1, "cua": h.cua}})
	_ = c.WriteMessage(gws.OpcodeText, caps)
}
func (h *gpuAgentFixture) OnClose(c *gws.Conn, _ error) {
	if value, ok := c.Session().Load("cancel"); ok {
		value.(context.CancelFunc)()
	}
}
func (h *gpuAgentFixture) OnMessage(c *gws.Conn, m *gws.Message) {
	defer m.Close()
	h.requests.Add(1)
	var name string
	if json.Unmarshal(m.Bytes(), &name) == nil {
		if name == "GetScreenshot" {
			h.snapshots.Add(1)
			var out bytes.Buffer
			_ = png.Encode(&out, image.NewRGBA(image.Rect(0, 0, 320, 180)))
			data, _ := json.Marshal(map[string]string{"Screenshot": base64.StdEncoding.EncodeToString(out.Bytes())})
			_ = c.WriteMessage(gws.OpcodeText, data)
		}
		if name == "GetCapturableList" {
			_ = c.WriteMessage(gws.OpcodeText, []byte(`{"CapturableList":["Primary","Secondary"]}`))
		}
		return
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(m.Bytes(), &fields)
	if request := fields["Cua"]; request != nil {
		if h.nativeRequests != nil {
			h.nativeRequests <- bytes.Clone(request)
		}
		var action struct {
			Action string `json:"action"`
		}
		_ = json.Unmarshal(request, &action)
		if action.Action == "tools" {
			if h.nativeTools != nil {
				payload, _ := json.Marshal(map[string]any{"CuaResult": map[string]any{"result": h.nativeTools}})
				_ = c.WriteMessage(gws.OpcodeText, payload)
			} else {
				_ = c.WriteMessage(gws.OpcodeText, []byte(`{"CuaResult":{"result":{"tools":[{"name":"list_apps","inputSchema":{"type":"object"}},{"name":"click","inputSchema":{"type":"object","required":["element_index"]}},{"name":"set_agent_cursor_enabled","inputSchema":{"type":"object","properties":{"enabled":{"type":"boolean"}}}},{"name":"shell","inputSchema":{"type":"object"}}]}}}`))
			}
		} else if !h.nativeBlock {
			_ = c.WriteMessage(gws.OpcodeText, h.nativeResult)
		}
		return
	}
	if fields["Config"] != nil {
		h.configs.Add(1)
		var config struct {
			FrameRate float64 `json:"frame_rate"`
		}
		_ = json.Unmarshal(fields["Config"], &config)
		if config.FrameRate != 0 {
			panic("Agent started continuous video")
		}
		_ = c.WriteMessage(gws.OpcodeText, []byte(`"ConfigOk"`))
		return
	}
	for _, key := range []string{"PointerEvent", "KeyboardEvent", "WheelEvent", "TextInputEvent"} {
		if fields[key] != nil {
			select {
			case h.inputs <- string(m.Bytes()):
			default:
			}
		}
	}
}

// A synthetic client forwards the real multiplexed tunnel bytes to an embedded
// GPU protocol fixture. No desktop_start response or legacy screenshot exists.
func gpuFixtureTunnel(t *testing.T, device, address string) *gpuDesktopTunnel {
	t.Helper()
	tunnel := newGPUDesktopTunnel(device, nil)
	var mu sync.Mutex
	conns := map[uint64]net.Conn{}
	go func() {
		for {
			select {
			case <-tunnel.closed:
				return
			case frame := <-tunnel.send:
				id := binary.BigEndian.Uint64(frame[1:9])
				switch frame[0] {
				case gpuDesktopTunnelFrameOpen:
					c, err := net.DialTimeout("tcp", address, time.Second)
					if err != nil {
						tunnel.closeStream(id)
						continue
					}
					mu.Lock()
					conns[id] = c
					mu.Unlock()
					go func() {
						defer c.Close()
						buf := make([]byte, 32768)
						for {
							n, err := c.Read(buf)
							if n > 0 {
								tunnel.dispatchData(id, buf[:n])
							}
							if err != nil {
								tunnel.closeStream(id)
								return
							}
						}
					}()
				case gpuDesktopTunnelFrameData:
					mu.Lock()
					c := conns[id]
					mu.Unlock()
					if c != nil {
						_, _ = c.Write(frame[9:])
					}
				case gpuDesktopTunnelFrameClose:
					mu.Lock()
					c := conns[id]
					delete(conns, id)
					mu.Unlock()
					if c != nil {
						c.Close()
					}
				}
			}
		}
	}()
	t.Cleanup(func() {
		tunnel.close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
	})
	return tunnel
}
func TestComputerGPUTunnelCaptureInputAndSessionReuse(t *testing.T) {
	fixture := &gpuAgentFixture{inputs: make(chan string, 32)}
	upgrade := gws.NewUpgrader(fixture, nil)
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws" {
			t.Errorf("wrong GPU path %s", r.URL.Path)
		}
		c, err := upgrade.Upgrade(w, r)
		if err == nil {
			go c.ReadLoop()
		}
	}))
	defer web.Close()
	srv, client, transport := newProxyTestServer(t, "gpu-device", "")
	client.Desktop = &protocol.DesktopCapabilities{Supported: true, Input: true, Backends: []string{"gpu-desktop-tunnel"}, Sources: []protocol.DesktopSource{{ID: "auto"}}}
	tunnel := gpuFixtureTunnel(t, client.ID, strings.TrimPrefix(web.URL, "http://"))
	srv.gpuDesktopTunnels[client.ID] = tunnel
	base := testAutomation(t, srv, "gpu-device", "thread")
	tool := computerTool{base}
	defer func() { base.state.opMu.Lock(); closeDesktopAutomation(base.state); base.state.opMu.Unlock() }()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	run := func(args string) string {
		t.Helper()
		out, err := tool.Execute(ctx, json.RawMessage(args))
		if err != nil {
			t.Fatalf("%s: %v", args, err)
		}
		return out
	}
	shot := run(`{"action":"screenshot"}`)
	if !strings.HasPrefix(shot, "[image preview]") {
		t.Fatal("missing image")
	}
	run(`{"action":"click","x":160,"y":90}`)
	run(`{"action":"screenshot"}`)
	run(`{"action":"text","text":"你好 GPU"}`)
	run(`{"action":"screenshot"}`)
	run(`{"action":"key","key":"Enter"}`)
	run(`{"action":"screenshot"}`)
	if fixture.connections.Load() != 1 || fixture.configs.Load() != 1 {
		t.Fatalf("reconnected per action: connections=%d configs=%d", fixture.connections.Load(), fixture.configs.Load())
	}
	if fixture.snapshots.Load() != 4 {
		t.Fatalf("input captured extra frames: %d", fixture.snapshots.Load())
	}
	if got := base.state.screens[client.ID]; got.width != 320 || got.height != 180 || got.source != "gpu:0" {
		t.Fatalf("wrong capture coordinates: %+v", got)
	}
	if transport.lastMsg() != nil {
		t.Fatal("GPU tool sent legacy desktop protocol")
	}
	textSeen := false
	for len(fixture.inputs) > 0 {
		raw := <-fixture.inputs
		if strings.Contains(raw, "你好 GPU") {
			textSeen = true
		}
	}
	if !textSeen {
		t.Fatal("native Unicode input missing")
	}
	run(`{"action":"screenshot","source":"gpu:1"}`)
	run(`{"action":"screenshot"}`)
	if fixture.connections.Load() != 1 || fixture.configs.Load() != 2 || fixture.snapshots.Load() != 6 {
		t.Fatal("source switch reconnected or did not retain selected screen")
	}
	run(`{"action":"release"}`)
	if len(base.state.gpu) != 0 {
		t.Fatal("release retained capture connection")
	}
}
func TestGPUDesktopSourceValidation(t *testing.T) {
	for _, source := range []string{"gpu:-1", "gpu:2", "../ws", "1"} {
		if _, err := gpuSourceIndex(source, 2); err == nil {
			t.Fatal("accepted", source)
		}
	}
	if id, err := gpuSourceIndex("gpu:1", 2); err != nil || id != 1 {
		t.Fatal(fmt.Sprint(id, err))
	}
}

func TestGPUOldClientRequiresUpgradeWithoutStartingDecoder(t *testing.T) {
	d := &gpuAgentDesktop{}
	_, err := d.freshFrame(context.Background())
	if err == nil || !strings.Contains(err.Error(), "update this device") {
		t.Fatalf("missing capability should fail before sending a request: %v", err)
	}
}

func newGPUCuaTool(t *testing.T, fixture *gpuAgentFixture) (*computerTool, *ClientConn) {
	t.Helper()
	upgrade := gws.NewUpgrader(fixture, nil)
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrade.Upgrade(w, r)
		if err == nil {
			go c.ReadLoop()
		}
	}))
	t.Cleanup(web.Close)
	srv, client, _ := newProxyTestServer(t, "cua-device", "")
	client.Desktop = &protocol.DesktopCapabilities{Supported: true, Input: true, Backends: []string{"gpu-desktop-tunnel"}}
	srv.gpuDesktopTunnels[client.ID] = gpuFixtureTunnel(t, client.ID, strings.TrimPrefix(web.URL, "http://"))
	base := testAutomation(t, srv, client.ID, "cua-thread")
	t.Cleanup(func() {
		base.state.opMu.Lock()
		closeDesktopAutomation(base.state)
		base.state.opMu.Unlock()
	})
	return &computerTool{base}, client
}

func TestComputerCuaUnsupportedDoesNotSend(t *testing.T) {
	for _, action := range []string{"native_tools", "native"} {
		t.Run(action+"_Go", func(t *testing.T) {
			srv, client, transport := newProxyTestServer(t, "go-device", "")
			client.Desktop = &protocol.DesktopCapabilities{Supported: true, Input: true}
			tool := computerTool{testAutomation(t, srv, client.ID, "thread")}
			_, err := tool.Execute(context.Background(), json.RawMessage(`{"action":"`+action+`","tool":"list_apps"}`))
			if err == nil || !strings.Contains(err.Error(), "native Cua tools unsupported") {
				t.Fatalf("missing explicit unsupported error: %v", err)
			}
			if transport.lastMsg() != nil {
				t.Fatal("unsupported Go client received a command")
			}
		})
		t.Run(action+"_old_Rust", func(t *testing.T) {
			fixture := &gpuAgentFixture{}
			tool, _ := newGPUCuaTool(t, fixture)
			_, err := tool.Execute(context.Background(), json.RawMessage(`{"action":"`+action+`","tool":"list_apps"}`))
			if err == nil || !strings.Contains(err.Error(), "Cua-capable Rust client") {
				t.Fatalf("missing explicit unsupported error: %v", err)
			}
			if fixture.requests.Load() != 0 {
				t.Fatal("unsupported Rust client received a websocket command")
			}
		})
	}
}

func TestComputerCuaDiscoveryResultsAndSession(t *testing.T) {
	const response = `{"CuaResult":{"result":{"content":[{"type":"text","text":"permission refused"},{"type":"image","mimeType":"image/png","data":"aW1hZ2U="}],"structuredContent":{"element_index":7},"isError":true}}}`
	fixture := &gpuAgentFixture{cua: true, nativeResult: json.RawMessage(response), nativeRequests: make(chan json.RawMessage, 8)}
	tool, client := newGPUCuaTool(t, fixture)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tools, err := tool.Execute(ctx, json.RawMessage(`{"action":"native_tools"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tools, `"set_agent_cursor_enabled"`) || strings.Contains(tools, `"inputSchema"`) || !strings.Contains(tools, `"shell"`) {
		t.Fatalf("catalog omitted names or included full schemas: %s", tools)
	}
	selected, err := tool.Execute(ctx, json.RawMessage(`{"action":"native_tools","tool":"click"}`))
	if err != nil || !strings.Contains(selected, `"required":["element_index"]`) || strings.Contains(selected, `"name":"shell"`) {
		t.Fatalf("selected tool schema missing or unrelated definitions leaked: %s %v", selected, err)
	}
	for range 2 {
		if _, err := tool.Execute(ctx, json.RawMessage(`{"action":"screenshot"}`)); err != nil {
			t.Fatal(err)
		}
		result, err := tool.Execute(ctx, json.RawMessage(`{"action":"native","tool":"click","arguments":{"element_index":7}}`))
		if err != nil {
			t.Fatal(err)
		}
		want := `{"result":{"content":[{"type":"text","text":"permission refused"},{"type":"image","mimeType":"image/png","data":"aW1hZ2U="}],"structuredContent":{"element_index":7},"isError":true}}`
		if result != want {
			t.Fatalf("MCP result changed: %s", result)
		}
	}
	status, err := tool.Execute(ctx, json.RawMessage(`{"action":"status"}`))
	if err != nil || !strings.Contains(status, `"cua":true`) {
		t.Fatalf("status missing Cua capability: %s %v", status, err)
	}
	if fixture.connections.Load() != 3 || fixture.configs.Load() != 2 || fixture.snapshots.Load() != 2 || len(tool.state.screens) != 0 || tool.state.gpu[client.ID] != nil {
		t.Fatal("native error retained resources or skipped observations")
	}
	for i := range 4 {
		request := <-fixture.nativeRequests
		if i == 0 || i == 2 {
			if string(request) != `{"action":"tools"}` {
				t.Fatalf("wrong tools/list framing: %s", request)
			}
		} else if string(request) != `{"action":"call","name":"click","arguments":{"element_index":7}}` {
			t.Fatalf("wrong tools/call framing: %s", request)
		}
	}
	if _, err := tool.Execute(ctx, json.RawMessage(`{"action":"release"}`)); err != nil || len(tool.state.gpu) != 0 {
		t.Fatalf("native release failed: %v", err)
	}
}

func TestComputerCuaErrorAndUnavailableTool(t *testing.T) {
	fixture := &gpuAgentFixture{cua: true, nativeResult: json.RawMessage(`{"CuaResult":{"error":"Accessibility permission denied"}}`), nativeRequests: make(chan json.RawMessage, 8)}
	tool, client := newGPUCuaTool(t, fixture)
	for range 2 {
		if _, err := tool.Execute(context.Background(), json.RawMessage(`{"action":"screenshot"}`)); err != nil {
			t.Fatal(err)
		}
		_, err := tool.Execute(context.Background(), json.RawMessage(`{"action":"native","tool":"list_apps"}`))
		if err == nil || !strings.Contains(err.Error(), "Accessibility permission denied") {
			t.Fatalf("lost driver error: %v", err)
		}
	}
	if fixture.connections.Load() != 2 || tool.state.gpu[client.ID] != nil || len(fixture.nativeRequests) != 4 {
		t.Fatal("driver error retained private control resources")
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"action":"screenshot"}`)); err != nil {
		t.Fatal(err)
	}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"action":"native","tool":"scroll"}`))
	if err == nil || !strings.Contains(err.Error(), "unavailable") || len(fixture.nativeRequests) != 5 {
		t.Fatalf("undiscovered tool was sent: %v", err)
	}
}

func TestComputerCuaManualTakeoverCancels(t *testing.T) {
	fixture := &gpuAgentFixture{cua: true, nativeBlock: true, nativeRequests: make(chan json.RawMessage, 8)}
	tool, client := newGPUCuaTool(t, fixture)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := tool.Execute(ctx, json.RawMessage(`{"action":"screenshot"}`)); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := tool.Execute(ctx, json.RawMessage(`{"action":"native","tool":"click","arguments":{"element_index":7}}`))
		result <- err
	}()
	for range 2 {
		select {
		case <-fixture.nativeRequests:
		case <-ctx.Done():
			t.Fatal("native request never reached client")
		}
	}
	if _, err := tool.Execute(ctx, json.RawMessage(`{"action":"native_tools"}`)); err == nil || !strings.Contains(err.Error(), "another automation action") {
		t.Fatalf("concurrent native operation accepted: %v", err)
	}
	tool.srv.interruptDesktopAutomation(client.ID)
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "canceled") {
			t.Fatalf("takeover did not cancel native call: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("native call did not stop")
	}
	if len(tool.state.gpu) != 0 {
		t.Fatal("takeover retained native driver connection")
	}
}

func TestGPUCuaFramingAndBounds(t *testing.T) {
	for _, raw := range []string{`{}`, `null`, `{"result":null}`, `{"result":[]}`, `{"result":{},"error":"failure"}`, `{"result":`, strings.Repeat(" ", gpuCuaResultMax+1)} {
		if _, err := gpuCuaResult(json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid result accepted: %.80s", raw)
		}
	}
	for _, raw := range []string{`{"tool":"list_apps","arguments":[]}`, `{"tool":""}`, `{"tool":"list_apps","arguments":{"text":"` + strings.Repeat("x", gpuCuaRequestMax) + `"}}`} {
		if _, err := gpuCuaRequest("native", json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid request accepted: %.80s", raw)
		}
	}
	if _, err := gpuCuaRequest("native", json.RawMessage(`{"tool":"set_agent_cursor_enabled","arguments":{"enabled":true}}`)); err != nil {
		t.Fatalf("valid discovered CUA tool rejected: %v", err)
	}
	d := &gpuAgentDesktop{messages: make(chan json.RawMessage, 1), done: make(chan struct{})}
	d.messages <- json.RawMessage(`{"AgentCapabilities":{"version":1}}`)
	if err := d.requireCua(context.Background()); err == nil || !d.capabilities || d.cua {
		t.Fatalf("missing Cua capability was accepted: %v", err)
	}
}

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
	connections atomic.Int32
	configs     atomic.Int32
	snapshots   atomic.Int32
	inputs      chan string
}

func (h *gpuAgentFixture) OnOpen(c *gws.Conn) {
	h.connections.Add(1)
	_ = c.WriteMessage(gws.OpcodeText, []byte(`{"AgentCapabilities":{"version":1}}`))
}
func (h *gpuAgentFixture) OnClose(c *gws.Conn, _ error) {
	if value, ok := c.Session().Load("cancel"); ok {
		value.(context.CancelFunc)()
	}
}
func (h *gpuAgentFixture) OnMessage(c *gws.Conn, m *gws.Message) {
	defer m.Close()
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
	run(`{"action":"text","text":"你好 GPU"}`)
	run(`{"action":"key","key":"Enter"}`)
	run(`{"action":"screenshot"}`)
	if fixture.connections.Load() != 1 || fixture.configs.Load() != 1 {
		t.Fatalf("reconnected per action: connections=%d configs=%d", fixture.connections.Load(), fixture.configs.Load())
	}
	if fixture.snapshots.Load() != 2 {
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
	if fixture.connections.Load() != 1 || fixture.configs.Load() != 2 || fixture.snapshots.Load() != 4 {
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

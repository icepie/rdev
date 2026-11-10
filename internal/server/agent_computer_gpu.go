package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"image/png"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lxzan/gws"
)

// GPU clients expose native input and on-demand images through their existing
// desktop tunnel, not desktop_start. Keep the control/capture session between
// tool calls; neither the web viewer nor the device capture is restarted.
type gpuAgentDesktop struct {
	gws.BuiltinEventHandler
	client        *ClientConn
	tunnel        *gpuDesktopTunnel
	socket        *gws.Conn
	messages      chan json.RawMessage
	agentVersion  int
	autojs        bool
	cua           bool
	capabilities  bool
	nativeSession bool
	cuaTools      map[string]bool
	done          chan struct{}
	once          sync.Once
	touched       atomic.Int64
	source        string
	sourceName    string
	configured    bool
	names         []string
}

func (d *gpuAgentDesktop) close() {
	d.once.Do(func() {
		close(d.done)
		if d.socket != nil {
			_ = d.socket.NetConn().Close()
		}
	})
}
func (d *gpuAgentDesktop) OnClose(_ *gws.Conn, _ error) { d.close() }
func (d *gpuAgentDesktop) OnMessage(_ *gws.Conn, m *gws.Message) {
	defer m.Close()
	if m.Opcode == gws.OpcodeText {
		select {
		case d.messages <- bytes.Clone(m.Bytes()):
		case <-d.done:
		}
	}
}
func (d *gpuAgentDesktop) send(value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_ = d.socket.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return d.socket.WriteMessage(gws.OpcodeText, raw)
}
func (d *gpuAgentDesktop) wait(ctx context.Context, name string) (json.RawMessage, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-d.done:
			return nil, fmt.Errorf("GPU desktop disconnected")
		case raw := <-d.messages:
			var unit string
			if json.Unmarshal(raw, &unit) == nil && unit == name {
				return nil, nil
			}
			var fields map[string]json.RawMessage
			if json.Unmarshal(raw, &fields) != nil {
				continue
			}
			if value := fields["AgentCapabilities"]; value != nil {
				var caps struct {
					Version int
					Autojs  bool
					Cua     bool
				}
				if json.Unmarshal(value, &caps) == nil {
					d.agentVersion = caps.Version
					d.autojs = caps.Autojs
					d.cua = caps.Cua
					d.capabilities = true
				}
			}
			for _, key := range []string{"Error", "ConfigError"} {
				if value, ok := fields[key]; ok {
					return nil, fmt.Errorf("GPU desktop: %s", value)
				}
			}
			if value, ok := fields[name]; ok {
				return value, nil
			}
		}
	}
}

const gpuCuaRequestMax = 64 << 10
const gpuCuaResultMax = 8 << 20

func (d *gpuAgentDesktop) requireCua(ctx context.Context) error {
	if !d.capabilities {
		capCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if _, err := d.wait(capCtx, "AgentCapabilities"); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("native Cua tools unsupported: this device did not advertise Cua capability; use a supported Rust client (%v)", err)
		}
	}
	if !d.cua {
		return fmt.Errorf("native Cua tools unsupported: this device requires a Cua-capable Rust client; Go and older clients support screenshot/input only")
	}
	return nil
}

func gpuCuaRequest(action string, raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) > gpuCuaRequestMax {
		return nil, fmt.Errorf("native Cua request exceeds %d byte limit", gpuCuaRequestMax)
	}
	request := struct {
		Action    string                     `json:"action"`
		Name      string                     `json:"name,omitempty"`
		Arguments map[string]json.RawMessage `json:"arguments,omitempty"`
	}{Action: "tools"}
	if action == "native" {
		var args struct {
			Tool      string                     `json:"tool"`
			Arguments map[string]json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, fmt.Errorf("invalid native Cua arguments: %w", err)
		}
		if strings.TrimSpace(args.Tool) == "" || len(args.Tool) > 128 {
			return nil, fmt.Errorf("native Cua call requires a tool name from action=tools")
		}
		request.Action, request.Name, request.Arguments = "call", args.Tool, args.Arguments
	} else if action != "native_tools" {
		return nil, fmt.Errorf("unsupported native Cua action %q", action)
	}
	payload, err := json.Marshal(map[string]any{"Cua": request})
	if err != nil {
		return nil, err
	}
	if len(payload) > gpuCuaRequestMax {
		return nil, fmt.Errorf("native Cua request exceeds %d byte limit", gpuCuaRequestMax)
	}
	return payload, nil
}

// Keep the MCP result intact: tool schemas, structuredContent, content images,
// and isError all belong to the result, not a screenshot-only preview envelope.
func gpuCuaResult(raw json.RawMessage) (string, error) {
	if len(raw) > gpuCuaResultMax {
		return "", fmt.Errorf("native Cua result exceeds %d byte limit", gpuCuaResultMax)
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *string         `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return "", fmt.Errorf("invalid CuaResult: %w", err)
	}
	if envelope.Error != nil {
		if envelope.Result != nil {
			return "", fmt.Errorf("invalid CuaResult: both result and error")
		}
		return "", fmt.Errorf("Cua: %s", *envelope.Error)
	}
	result := bytes.TrimSpace(envelope.Result)
	if len(result) == 0 || result[0] != '{' {
		return "", fmt.Errorf("invalid CuaResult: missing MCP result object")
	}
	return marshalJobResult(map[string]any{"result": envelope.Result})
}

func (d *gpuAgentDesktop) discoverCuaTools(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := d.send(json.RawMessage(`{"Cua":{"action":"tools"}}`)); err != nil {
		return "", err
	}
	raw, err := d.wait(ctx, "CuaResult")
	if err != nil {
		return "", err
	}
	framed, err := gpuCuaResult(raw)
	if err != nil {
		return "", err
	}
	var envelope struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(framed), &envelope); err != nil {
		return "", err
	}
	var tools []json.RawMessage
	if err := json.Unmarshal(envelope.Result["tools"], &tools); err != nil || tools == nil {
		return "", fmt.Errorf("invalid Cua tools/list result")
	}
	available := make([]json.RawMessage, 0, len(tools))
	names := make(map[string]bool)
	for _, tool := range tools {
		var schema struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(tool, &schema); err != nil {
			return "", fmt.Errorf("invalid Cua tool schema: %w", err)
		}
		if schema.Name == "" || len(schema.Name) > 128 {
			return "", fmt.Errorf("invalid Cua tool name")
		}
		available = append(available, tool)
		names[schema.Name] = true
	}
	envelope.Result["tools"], err = json.Marshal(available)
	if err != nil {
		return "", err
	}
	d.cuaTools = names
	return marshalJobResult(envelope)
}

// Adapt an authenticated multiplexed tunnel stream to the websocket dialer.
// There is no localhost HTTP callback, admin-token URL, or server-network dial.
type gpuAgentDialer struct{ tunnel *gpuDesktopTunnel }

func (d gpuAgentDialer) Dial(_, _ string) (net.Conn, error) {
	stream, err := d.tunnel.openStream()
	if err != nil {
		return nil, err
	}
	local, remote := net.Pipe()
	var once sync.Once
	closeAll := func() { once.Do(func() { _ = remote.Close(); d.tunnel.sendClose(stream.id) }) }
	go func() {
		defer closeAll()
		buf := make([]byte, gpuDesktopTunnelChunkSize)
		for {
			n, err := remote.Read(buf)
			if n > 0 && !d.tunnel.sendData(stream.id, buf[:n]) {
				return
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		defer closeAll()
		for {
			select {
			case <-d.tunnel.closed:
				return
			case <-stream.closed:
				return
			case data, ok := <-stream.data:
				if !ok {
					return
				}
				if _, err := remote.Write(data); err != nil {
					return
				}
			}
		}
	}()
	return local, nil
}
func (s *Server) newGPUAgentDesktop(ctx context.Context, client *ClientConn) (*gpuAgentDesktop, error) {
	tunnel := s.gpuDesktopTunnel(client.ID)
	if tunnel == nil {
		return nil, fmt.Errorf("GPU desktop tunnel is not connected; wait for the device to reconnect")
	}
	d := &gpuAgentDesktop{client: client, tunnel: tunnel, messages: make(chan json.RawMessage, 32), done: make(chan struct{})}
	socket, _, err := gws.NewClient(d, &gws.ClientOption{Addr: "ws://127.0.0.1:1701/ws", HandshakeTimeout: 5 * time.Second, ReadMaxPayloadSize: 8 << 20, NewDialer: func() (gws.Dialer, error) { return gpuAgentDialer{tunnel}, nil }})
	if err != nil {
		return nil, err
	}
	d.socket = socket
	d.touched.Store(time.Now().UnixNano())
	go socket.ReadLoop()
	if err := ctx.Err(); err != nil {
		d.close()
		return nil, err
	}
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-d.done:
				return
			case <-ticker.C:
				if time.Since(time.Unix(0, d.touched.Load())) > 2*time.Minute {
					d.close()
					return
				}
			}
		}
	}()
	return d, nil
}

// Request one image from the client's capture worker. No video stream or decoder.
func (d *gpuAgentDesktop) freshFrame(ctx context.Context) ([]byte, error) {
	if d.agentVersion < 1 {
		return nil, fmt.Errorf("update this device's client to enable native Agent screenshots")
	}
	if err := d.send("GetScreenshot"); err != nil {
		return nil, err
	}
	raw, err := d.wait(ctx, "Screenshot")
	if err != nil {
		return nil, err
	}
	var encoded string
	if err = json.Unmarshal(raw, &encoded); err != nil {
		return nil, err
	}
	if len(encoded) > 5<<20 {
		return nil, fmt.Errorf("screenshot exceeds image size limit")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	// Native clients use PNG to avoid codec dependencies; normalize for vision tools.
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 1280 || cfg.Height > 720 {
		return nil, fmt.Errorf("invalid native screenshot dimensions")
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err = jpeg.Encode(&out, img, &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
func gpuSourceIndex(source string, n int) (int, error) {
	if source == "" || source == "auto" {
		if n > 0 {
			return 0, nil
		}
		return 0, fmt.Errorf("device reports no capturable screens")
	}
	id, err := strconv.Atoi(strings.TrimPrefix(source, "gpu:"))
	if err != nil || id < 0 || id >= n || !strings.HasPrefix(source, "gpu:") {
		return 0, fmt.Errorf("unknown GPU screen; use computer status for source IDs")
	}
	return id, nil
}
func (t *computerTool) executeGPU(ctx context.Context, client *ClientConn, a computerAction, raw json.RawMessage) (string, error) {
	ctx, finish, err := t.begin(ctx)
	if err != nil {
		return "", err
	}
	defer finish()
	if t.state.gpu == nil {
		t.state.gpu = make(map[string]*gpuAgentDesktop)
	}
	d := t.state.gpu[client.ID]
	if d != nil {
		select {
		case <-d.done:
			d = nil
		default:
		}
	}
	if d != nil && (d.client != client || d.tunnel != t.srv.gpuDesktopTunnel(client.ID)) {
		d.close()
		d = nil
	}
	if d == nil {
		d, err = t.srv.newGPUAgentDesktop(ctx, client)
		if err != nil {
			return "", err
		}
		t.state.gpu[client.ID] = d
	}
	d.touched.Store(time.Now().UnixNano())
	// Failed/canceled actions must not leave a stuck input or stale capture.
	success := false
	defer func() {
		if !success {
			if d != nil {
				d.close()
			}
			delete(t.state.gpu, client.ID)
		}
	}()
	if a.Action == "native_tools" || a.Action == "native" {
		if err = d.requireCua(ctx); err != nil {
			return "", err
		}
		request, err := gpuCuaRequest(a.Action, raw)
		if err != nil {
			return "", err
		}
		if err = t.claim(client.ID); err != nil {
			return "", err
		}
		t.publish(automationView{Kind: "desktop", Device: client.ID, Action: a.Action})
		if a.Action == "native_tools" {
			result, err := d.discoverCuaTools(ctx)
			success = err == nil && ctx.Err() == nil
			d.nativeSession = success
			return result, err
		}
		if d.cuaTools == nil {
			if _, err := d.discoverCuaTools(ctx); err != nil {
				return "", err
			}
		}
		var args struct {
			Tool string `json:"tool"`
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			return "", err
		}
		if !d.cuaTools[args.Tool] {
			return "", fmt.Errorf("native Cua tool %q is unavailable on this device; discover tools with cua action=tools", args.Tool)
		}
		if err = ctx.Err(); err != nil {
			return "", err
		}
		if err = d.send(request); err != nil {
			return "", err
		}
		result, err := d.wait(ctx, "CuaResult")
		if err != nil {
			return "", err
		}
		// Completed tool/permission errors do not discard native element tokens.
		// Transport failure and cancellation still close the private driver.
		if err = ctx.Err(); err != nil {
			return "", err
		}
		success = true
		d.nativeSession = true
		return gpuCuaResult(result)
	}
	names := d.names
	if names == nil || a.Action == "status" {
		if err = d.send("GetCapturableList"); err != nil {
			return "", err
		}
		list, err := d.wait(ctx, "CapturableList")
		if err != nil {
			return "", err
		}
		if err = json.Unmarshal(list, &names); err != nil {
			return "", err
		}
		d.names = names
	}
	if a.Action == "status" {
		sources := make([]map[string]string, 0, len(names))
		for i, name := range names {
			sources = append(sources, map[string]string{"id": fmt.Sprintf("gpu:%d", i), "label": name})
		}
		success = true
		if !d.configured && !d.nativeSession {
			d.close()
			delete(t.state.gpu, client.ID)
		}
		return marshalJobResult(map[string]any{"device": client.ID, "transport": "gpu-desktop-tunnel", "sources": sources, "desktop": client.Desktop, "cua": d.cua})
	}
	screen := t.state.screens[client.ID]
	if a.Source == "" && screen.client == client {
		a.Source = screen.source
	}
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
	index, err := gpuSourceIndex(a.Source, len(names))
	if err != nil {
		return "", err
	}
	source := fmt.Sprintf("gpu:%d", index)
	if d.configured && (d.source != source || d.sourceName != names[index]) {
		if a.Action != "screenshot" {
			return "", fmt.Errorf("GPU screen changed; take a fresh screenshot")
		}
		d.configured = false
	}
	if err = t.claim(client.ID); err != nil {
		return "", err
	}
	if !d.configured {
		if a.Action != "screenshot" {
			return "", fmt.Errorf("GPU desktop reconnected; take a new screenshot")
		}
		if d.agentVersion < 1 {
			return "", fmt.Errorf("update this device's client to enable native Agent screenshots")
		}
		d.source = source
		d.sourceName = names[index]
		d.names = names
		config := map[string]any{"capturable_id": index, "capture_cursor": false, "max_width": 1280, "max_height": 720, "frame_rate": 0, "client_name": "rdev-agent", "encoder": "auto", "pointer_backend": "auto", "keyboard_backend": "auto"}
		if err = d.send(map[string]any{"Config": config}); err != nil {
			return "", err
		}
		if _, err = d.wait(ctx, "ConfigOk"); err != nil {
			return "", err
		}
		d.configured = true
	}
	t.publish(automationView{Kind: "desktop", Device: client.ID, Source: source, Action: a.Action})
	if a.Action == "screenshot" {
		frame, err := d.freshFrame(ctx)
		if err != nil {
			return "", err
		}
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(frame))
		if err != nil {
			return "", err
		}
		result, err := automationImage(frame)
		if err != nil {
			return "", err
		}
		if t.state.screens == nil {
			t.state.screens = make(map[string]automationScreen)
		}
		t.state.screens[client.ID] = automationScreen{client: client, source: source, width: cfg.Width, height: cfg.Height, at: time.Now()}
		success = true
		return result, nil
	}
	if err = gpuDesktopInput(ctx, d, a, raw, screen.width, screen.height); err != nil {
		return "", err
	}
	success = true
	return marshalJobResult(map[string]any{"device": client.ID, "action": a.Action, "source": source, "status": "sent", "next": "Take a screenshot to verify the application result."})
}
func gpuDesktopInput(ctx context.Context, d *gpuAgentDesktop, a computerAction, raw json.RawMessage, width, height int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	send := func(v any) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return d.send(v)
	}
	pointer := func(kind string, x, y, buttons int) map[string]any {
		button := []int{1, 4, 2}[a.Button]
		return map[string]any{"PointerEvent": map[string]any{"event_type": kind, "pointer_id": 1, "timestamp": time.Now().UnixMicro(), "is_primary": true, "pointer_type": "mouse", "button": button, "buttons": buttons, "x": float64(x) / float64(width), "y": float64(y) / float64(height), "movement_x": 0, "movement_y": 0, "pressure": 0.5, "tilt_x": 0, "tilt_y": 0, "twist": 0, "width": 0.001, "height": 0.001}}
	}
	switch a.Action {
	case "click", "drag", "scroll":
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields)
		valid := func(x, y int) bool { return x >= 0 && y >= 0 && x < width && y < height }
		if fields["x"] == nil || fields["y"] == nil || !valid(a.X, a.Y) || a.Button < 0 || a.Button > 2 {
			return fmt.Errorf("valid screenshot coordinates and button are required")
		}
		if a.Action == "scroll" {
			if err := send(pointer("pointermove", a.X, a.Y, 0)); err != nil {
				return err
			}
			return send(map[string]any{"WheelEvent": map[string]any{"dx": a.DeltaX, "dy": a.DeltaY, "timestamp": time.Now().UnixMicro()}})
		}
		if a.Action == "drag" && (fields["toX"] == nil || fields["toY"] == nil || !valid(a.ToX, a.ToY)) {
			return fmt.Errorf("drag requires valid toX/toY coordinates")
		}
		x, y := a.X, a.Y
		defer func() { _ = d.send(pointer("pointerup", x, y, 0)) }()
		bits := []int{1, 4, 2}[a.Button]
		if err := send(pointer("pointerdown", x, y, bits)); err != nil {
			return err
		}
		if a.Action == "drag" {
			if err := send(pointer("pointermove", a.ToX, a.ToY, bits)); err != nil {
				return err
			}
			x, y = a.ToX, a.ToY
		}
		return nil
	case "text":
		return send(map[string]any{"TextInputEvent": map[string]string{"text": a.Text}})
	case "key":
		if a.Key == "" {
			return fmt.Errorf("key is required")
		}
		code := a.Code
		if code == "" {
			code = a.Key
			if len(a.Key) == 1 && a.Key[0] >= 'a' && a.Key[0] <= 'z' {
				code = "Key" + strings.ToUpper(a.Key)
			}
		}
		key := map[string]any{"event_type": "down", "key": a.Key, "code": code, "location": 0, "ctrl": a.Ctrl, "alt": a.Alt, "shift": a.Shift, "meta": a.Meta}
		defer func() {
			key["event_type"] = "up"
			_ = d.send(map[string]any{"KeyboardEvent": key})
			_ = d.send("ReleaseKeyboard")
		}()
		return send(map[string]any{"KeyboardEvent": key})
	default:
		return fmt.Errorf("unsupported computer action %q", a.Action)
	}
}

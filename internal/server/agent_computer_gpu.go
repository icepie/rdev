package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lxzan/gws"
)

// GPU clients expose native input and fragmented MP4 through their existing
// desktop tunnel, not desktop_start. Keep the control/decoder session between
// tool calls; neither the web viewer nor the device capture is restarted.
type gpuAgentDesktop struct {
	gws.BuiltinEventHandler
	client     *ClientConn
	tunnel     *gpuDesktopTunnel
	socket     *gws.Conn
	messages   chan json.RawMessage
	video      chan []byte
	frames     chan []byte
	done       chan struct{}
	once       sync.Once
	mu         sync.Mutex
	cancel     context.CancelFunc
	touched    atomic.Int64
	source     string
	sourceName string
	configured bool
	names      []string
}

func (d *gpuAgentDesktop) close() {
	d.once.Do(func() {
		close(d.done)
		d.mu.Lock()
		if d.cancel != nil {
			d.cancel()
		}
		d.mu.Unlock()
		if d.socket != nil {
			_ = d.socket.NetConn().Close()
		}
	})
}
func (d *gpuAgentDesktop) OnClose(_ *gws.Conn, _ error) { d.close() }
func (d *gpuAgentDesktop) OnMessage(_ *gws.Conn, m *gws.Message) {
	defer m.Close()
	if m.Opcode == gws.OpcodeBinary {
		select {
		case d.video <- bytes.Clone(m.Bytes()):
		case <-d.done:
		}
		return
	}
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
	d := &gpuAgentDesktop{client: client, tunnel: tunnel, messages: make(chan json.RawMessage, 32), video: make(chan []byte, 16), frames: make(chan []byte, 1), done: make(chan struct{})}
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

// Decode locally, preserving the captured frame's pixel dimensions. A bounded
// latest-frame mailbox prevents slow models from accumulating video in memory.
func (d *gpuAgentDesktop) startDecoder() error {
	executable, err := exec.LookPath("ffmpeg")
	if err != nil {
		return fmt.Errorf("GPU screenshots require FFmpeg on the server; install ffmpeg and retry")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, executable, "-hide_banner", "-loglevel", "error", "-threads", "1", "-protocol_whitelist", "pipe", "-probesize", "32768", "-analyzeduration", "0", "-f", "mp4", "-i", "pipe:0", "-an", "-threads", "1", "-f", "image2pipe", "-vcodec", "mjpeg", "-q:v", "6", "pipe:1")
	input, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		input.Close()
		return err
	}
	if err = cmd.Start(); err != nil {
		cancel()
		input.Close()
		output.Close()
		return err
	}
	// Published before any decoder goroutine can close the connection.
	d.mu.Lock()
	d.cancel = cancel
	select {
	case <-d.done:
		cancel()
	default:
	}
	d.mu.Unlock()
	go func() {
		defer input.Close()
		for {
			select {
			case <-d.done:
				return
			case data := <-d.video:
				if _, err := input.Write(data); err != nil {
					return
				}
			}
		}
	}()
	go func() {
		defer d.close()
		defer cmd.Wait()
		reader := bufio.NewReader(output)
		for {
			frame, err := readGPUJPEG(reader)
			if err != nil {
				cancel()
				return
			}
			select {
			case d.frames <- frame:
			default:
				select {
				case <-d.frames:
				default:
				}
				select {
				case d.frames <- frame:
				default:
				}
			}
		}
	}()
	return nil
}
func readGPUJPEG(r *bufio.Reader) ([]byte, error) {
	var frame []byte
	for {
		part, err := r.ReadSlice(0xd9)
		frame = append(frame, part...)
		if len(frame) > 700*1024 {
			return nil, fmt.Errorf("GPU screenshot exceeds image size limit")
		}
		if err != nil && err != bufio.ErrBufferFull {
			return nil, err
		}
		if len(frame) > 1 && frame[len(frame)-2] == 0xff && frame[len(frame)-1] == 0xd9 {
			if !bytes.HasPrefix(frame, []byte{0xff, 0xd8}) {
				return nil, fmt.Errorf("invalid GPU screenshot")
			}
			return frame, nil
		}
	}
}
func (d *gpuAgentDesktop) freshFrame(ctx context.Context) ([]byte, error) {
	select {
	case <-d.frames:
	default:
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-d.done:
			return nil, fmt.Errorf("GPU capture ended; check the device capture permissions and server FFmpeg decoder")
		case frame := <-d.frames:
			return frame, nil
		case raw := <-d.messages:
			var msg map[string]json.RawMessage
			if json.Unmarshal(raw, &msg) == nil {
				for _, k := range []string{"Error", "ConfigError"} {
					if e := msg[k]; e != nil {
						return nil, fmt.Errorf("GPU desktop: %s", e)
					}
				}
			}
		}
	}
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
	// Failed/canceled actions must not leave a stuck input or stale decoder.
	success := false
	defer func() {
		if !success {
			if d != nil {
				d.close()
			}
			delete(t.state.gpu, client.ID)
		}
	}()
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
		if !d.configured {
			d.close()
			delete(t.state.gpu, client.ID)
		}
		return marshalJobResult(map[string]any{"device": client.ID, "transport": "gpu-desktop-tunnel", "sources": sources, "desktop": client.Desktop})
	}
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
	index, err := gpuSourceIndex(a.Source, len(names))
	if err != nil {
		return "", err
	}
	source := fmt.Sprintf("gpu:%d", index)
	if d.configured && (d.source != source || d.sourceName != names[index]) {
		d.close()
		delete(t.state.gpu, client.ID)
		if a.Action != "screenshot" {
			return "", fmt.Errorf("GPU screen changed; take a fresh screenshot")
		}
		d, err = t.srv.newGPUAgentDesktop(ctx, client)
		if err != nil {
			return "", err
		}
		t.state.gpu[client.ID] = d
		if err = d.send("GetCapturableList"); err != nil {
			return "", err
		}
		if _, err = d.wait(ctx, "CapturableList"); err != nil {
			return "", err
		}
	}
	if err = t.claim(client.ID); err != nil {
		return "", err
	}
	if !d.configured {
		if a.Action != "screenshot" {
			return "", fmt.Errorf("GPU desktop reconnected; take a new screenshot")
		}
		if err = d.startDecoder(); err != nil {
			return "", err
		}
		d.source = source
		d.sourceName = names[index]
		d.names = names
		config := map[string]any{"capturable_id": index, "capture_cursor": false, "max_width": 1280, "max_height": 720, "frame_rate": 2, "client_name": "rdev-agent", "encoder": "auto", "pointer_backend": "auto", "keyboard_backend": "auto"}
		if err = d.send(map[string]any{"Config": config}); err != nil {
			return "", err
		}
		if _, err = d.wait(ctx, "ConfigOk"); err != nil {
			return "", err
		}
		d.configured = true
	}
	t.publish(automationView{Kind: "desktop", Device: client.ID, Source: source, Action: a.Action})
	frame, err := d.freshFrame(ctx)
	if err != nil {
		return "", err
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(frame))
	if err != nil {
		return "", err
	}
	if a.Action == "screenshot" {
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
	if cfg.Width != screen.width || cfg.Height != screen.height {
		return "", fmt.Errorf("screen dimensions changed; take a new screenshot")
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

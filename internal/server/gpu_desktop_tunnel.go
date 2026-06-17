package server

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lxzan/gws"
)

const (
	gpuDesktopTunnelWriteWait  = 10 * time.Second
	gpuDesktopTunnelReadWait   = 75 * time.Second
	gpuDesktopTunnelPingPeriod = 25 * time.Second
	gpuDesktopTunnelOpenWait   = 10 * time.Second
	gpuDesktopTunnelFrameOpen  = byte(1)
	gpuDesktopTunnelFrameData  = byte(2)
	gpuDesktopTunnelFrameClose = byte(3)
	gpuDesktopTunnelChunkSize  = 64 * 1024
	gpuDesktopProxyPrefix      = "/gpu-desktop"
)

type gpuDesktopTunnelOpen struct {
	StreamID uint64 `json:"streamId"`
}

type gpuDesktopStream struct {
	id     uint64
	data   chan []byte
	closed chan struct{}
	once   sync.Once
}

type gpuDesktopTunnel struct {
	deviceID string
	conn     *gws.Conn
	send     chan []byte
	streams  map[uint64]*gpuDesktopStream
	mu       sync.Mutex
	nextID   uint64
	closed   chan struct{}
	once     sync.Once
}

type gpuDesktopTunnelHandler struct {
	gws.BuiltinEventHandler
	srv      *Server
	deviceID string
	tunnel   *gpuDesktopTunnel
}

func (s *Server) HandleGPUDesktopTunnel(w http.ResponseWriter, r *http.Request) {
	deviceID := strings.TrimSpace(r.URL.Query().Get("device"))
	if deviceID == "" {
		deviceID = strings.TrimSpace(r.URL.Query().Get("deviceId"))
	}
	if deviceID == "" {
		http.Error(w, "device is required", http.StatusBadRequest)
		return
	}
	client := s.clientByID(deviceID)
	if client == nil {
		http.Error(w, "device is not connected", http.StatusNotFound)
		return
	}
	if client.Password != "" && passwordFingerprint(r.URL.Query().Get("password")) != passwordFingerprint(client.Password) {
		http.Error(w, "wrong device password", http.StatusUnauthorized)
		return
	}
	if instanceID := strings.TrimSpace(r.URL.Query().Get("instanceId")); instanceID != "" && instanceID != client.InstanceID {
		http.Error(w, "wrong device instance", http.StatusUnauthorized)
		return
	}

	handler := &gpuDesktopTunnelHandler{srv: s, deviceID: deviceID}
	upgrader := gws.NewUpgrader(handler, &gws.ServerOption{
		ReadMaxPayloadSize: 16 * 1024 * 1024,
		CheckUtf8Enabled:   false,
	})
	socket, err := upgrader.Upgrade(w, r)
	if err != nil {
		return
	}
	socket.ReadLoop()
}

func (h *gpuDesktopTunnelHandler) OnOpen(socket *gws.Conn) {
	_ = socket.SetDeadline(time.Now().Add(gpuDesktopTunnelReadWait))
	tunnel := newGPUDesktopTunnel(h.deviceID, socket)
	h.tunnel = tunnel
	h.srv.registerGPUDesktopTunnel(tunnel)
	go tunnel.writeLoop()
}

func (h *gpuDesktopTunnelHandler) OnMessage(socket *gws.Conn, message *gws.Message) {
	defer message.Close()
	if message.Opcode != gws.OpcodeBinary {
		return
	}
	payload := message.Bytes()
	if len(payload) < 9 {
		return
	}
	frameType := payload[0]
	streamID := binary.BigEndian.Uint64(payload[1:9])
	body := payload[9:]
	switch frameType {
	case gpuDesktopTunnelFrameData:
		h.tunnel.dispatchData(streamID, body)
	case gpuDesktopTunnelFrameClose:
		h.tunnel.closeStream(streamID)
	}
}

func (h *gpuDesktopTunnelHandler) OnPing(socket *gws.Conn, payload []byte) {
	_ = socket.SetDeadline(time.Now().Add(gpuDesktopTunnelReadWait))
	_ = socket.WritePong(payload)
}

func (h *gpuDesktopTunnelHandler) OnPong(socket *gws.Conn, payload []byte) {
	_ = socket.SetDeadline(time.Now().Add(gpuDesktopTunnelReadWait))
}

func (h *gpuDesktopTunnelHandler) OnClose(socket *gws.Conn, err error) {
	if h.tunnel != nil {
		h.srv.unregisterGPUDesktopTunnel(h.tunnel)
		h.tunnel.close()
	}
}

func newGPUDesktopTunnel(deviceID string, conn *gws.Conn) *gpuDesktopTunnel {
	return &gpuDesktopTunnel{
		deviceID: deviceID,
		conn:     conn,
		send:     make(chan []byte, 256),
		streams:  make(map[uint64]*gpuDesktopStream),
		closed:   make(chan struct{}),
	}
}

func (s *Server) registerGPUDesktopTunnel(tunnel *gpuDesktopTunnel) {
	s.gpuDesktopMu.Lock()
	if old := s.gpuDesktopTunnels[tunnel.deviceID]; old != nil {
		old.close()
	}
	s.gpuDesktopTunnels[tunnel.deviceID] = tunnel
	s.gpuDesktopMu.Unlock()
	log.Printf("gpu desktop tunnel registered: device=%s", tunnel.deviceID)
}

func (s *Server) unregisterGPUDesktopTunnel(tunnel *gpuDesktopTunnel) {
	s.gpuDesktopMu.Lock()
	if s.gpuDesktopTunnels[tunnel.deviceID] == tunnel {
		delete(s.gpuDesktopTunnels, tunnel.deviceID)
		log.Printf("gpu desktop tunnel unregistered: device=%s", tunnel.deviceID)
	}
	s.gpuDesktopMu.Unlock()
}

func (s *Server) gpuDesktopTunnel(deviceID string) *gpuDesktopTunnel {
	s.gpuDesktopMu.RLock()
	tunnel := s.gpuDesktopTunnels[deviceID]
	s.gpuDesktopMu.RUnlock()
	return tunnel
}

func (s *Server) closeGPUDesktopTunnelForClient(deviceID string) {
	s.gpuDesktopMu.Lock()
	tunnel := s.gpuDesktopTunnels[deviceID]
	if tunnel != nil {
		delete(s.gpuDesktopTunnels, deviceID)
	}
	s.gpuDesktopMu.Unlock()
	if tunnel != nil {
		tunnel.close()
	}
}

func (t *gpuDesktopTunnel) writeLoop() {
	ticker := time.NewTicker(gpuDesktopTunnelPingPeriod)
	defer ticker.Stop()
	for {
		select {
		case payload := <-t.send:
			_ = t.conn.SetDeadline(time.Now().Add(gpuDesktopTunnelWriteWait))
			if err := t.conn.WriteMessage(gws.OpcodeBinary, payload); err != nil {
				t.close()
				return
			}
		case <-ticker.C:
			_ = t.conn.SetDeadline(time.Now().Add(gpuDesktopTunnelWriteWait))
			if err := t.conn.WritePing(nil); err != nil {
				t.close()
				return
			}
		case <-t.closed:
			return
		}
	}
}

func (t *gpuDesktopTunnel) openStream() (*gpuDesktopStream, error) {
	t.mu.Lock()
	select {
	case <-t.closed:
		t.mu.Unlock()
		return nil, errors.New("gpu desktop tunnel closed")
	default:
	}
	t.nextID++
	stream := &gpuDesktopStream{
		id:     t.nextID,
		data:   make(chan []byte, 128),
		closed: make(chan struct{}),
	}
	t.streams[stream.id] = stream
	t.mu.Unlock()

	body, _ := json.Marshal(gpuDesktopTunnelOpen{StreamID: stream.id})
	if !t.sendFrame(gpuDesktopTunnelFrameOpen, stream.id, body) {
		t.closeStream(stream.id)
		return nil, errors.New("gpu desktop tunnel send failed")
	}
	return stream, nil
}

func (t *gpuDesktopTunnel) dispatchData(streamID uint64, data []byte) {
	t.mu.Lock()
	stream := t.streams[streamID]
	t.mu.Unlock()
	if stream == nil {
		return
	}
	payload := append([]byte(nil), data...)
	select {
	case stream.data <- payload:
	case <-stream.closed:
	}
}

func (t *gpuDesktopTunnel) closeStream(streamID uint64) {
	t.mu.Lock()
	stream := t.streams[streamID]
	if stream != nil {
		delete(t.streams, streamID)
	}
	t.mu.Unlock()
	if stream != nil {
		stream.once.Do(func() {
			close(stream.closed)
			close(stream.data)
		})
	}
}

func (t *gpuDesktopTunnel) sendData(streamID uint64, data []byte) bool {
	for len(data) > 0 {
		n := len(data)
		if n > gpuDesktopTunnelChunkSize {
			n = gpuDesktopTunnelChunkSize
		}
		if !t.sendFrame(gpuDesktopTunnelFrameData, streamID, data[:n]) {
			return false
		}
		data = data[n:]
	}
	return true
}

func (t *gpuDesktopTunnel) sendClose(streamID uint64) {
	_ = t.sendFrame(gpuDesktopTunnelFrameClose, streamID, nil)
	t.closeStream(streamID)
}

func (t *gpuDesktopTunnel) sendFrame(frameType byte, streamID uint64, body []byte) bool {
	frame := make([]byte, 9+len(body))
	frame[0] = frameType
	binary.BigEndian.PutUint64(frame[1:9], streamID)
	copy(frame[9:], body)
	select {
	case t.send <- frame:
		return true
	case <-t.closed:
		return false
	}
}

func (t *gpuDesktopTunnel) close() {
	t.once.Do(func() {
		close(t.closed)
		_ = t.conn.WriteClose(1000, []byte("gpu desktop tunnel closed"))
		t.mu.Lock()
		for id, stream := range t.streams {
			delete(t.streams, id)
			stream.once.Do(func() {
				close(stream.closed)
				close(stream.data)
			})
		}
		t.mu.Unlock()
	})
}

func (s *Server) HandleGPUDesktopProxy(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuth(w, r) {
		return
	}
	deviceID, ok := gpuDesktopDeviceFromPath(r.URL.Path)
	if !ok {
		http.Error(w, "device is required", http.StatusBadRequest)
		return
	}
	client := s.clientByID(deviceID)
	if client == nil {
		http.Error(w, "device is not connected", http.StatusNotFound)
		return
	}
	if client.Password != "" && passwordFingerprint(r.URL.Query().Get("password")) != passwordFingerprint(client.Password) {
		http.Error(w, "wrong device password", http.StatusUnauthorized)
		return
	}
	tunnel := s.gpuDesktopTunnel(deviceID)
	if tunnel == nil {
		http.Error(w, "device gpu desktop tunnel is not connected", http.StatusBadGateway)
		return
	}
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		proxyGPUDesktopWebSocket(w, r, tunnel, deviceID)
		return
	}
	proxyGPUDesktopHTTP(w, r, tunnel, deviceID)
}

func gpuDesktopDeviceFromPath(path string) (string, bool) {
	path = strings.TrimPrefix(path, gpuDesktopProxyPrefix)
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		return "", false
	}
	deviceID, _, _ := strings.Cut(path, "/")
	deviceID = strings.TrimSpace(deviceID)
	return deviceID, deviceID != ""
}

func proxyGPUDesktopHTTP(w http.ResponseWriter, r *http.Request, tunnel *gpuDesktopTunnel, deviceID string) {
	stream, err := tunnel.openStream()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer tunnel.sendClose(stream.id)

	rawReq, targetPath, err := buildGPUDesktopRawRequest(r, deviceID, false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !tunnel.sendData(stream.id, rawReq) {
		http.Error(w, "gpu desktop tunnel send failed", http.StatusBadGateway)
		return
	}

	var rawResp bytes.Buffer
	timer := time.NewTimer(gpuDesktopTunnelOpenWait)
	defer timer.Stop()
	for {
		select {
		case data, ok := <-stream.data:
			if !ok {
				writeGPUDesktopHTTPResponse(w, r, &rawResp, deviceID, targetPath)
				return
			}
			_, _ = rawResp.Write(data)
		case <-timer.C:
			http.Error(w, "gpu desktop tunnel response timeout", http.StatusGatewayTimeout)
			return
		case <-r.Context().Done():
			return
		}
	}
}

func proxyGPUDesktopWebSocket(w http.ResponseWriter, r *http.Request, tunnel *gpuDesktopTunnel, deviceID string) {
	stream, err := tunnel.openStream()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer tunnel.sendClose(stream.id)

	rawReq, _, err := buildGPUDesktopRawRequest(r, deviceID, true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "websocket hijack unsupported", http.StatusInternalServerError)
		return
	}
	clientConn, clientBuf, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer clientConn.Close()

	if !tunnel.sendData(stream.id, rawReq) {
		return
	}
	go pipeGPUDesktopClientToTunnel(clientConn, clientBuf, tunnel, stream.id)
	for data := range stream.data {
		if _, err = clientConn.Write(data); err != nil {
			return
		}
	}
}

func pipeGPUDesktopClientToTunnel(conn io.Reader, reader *bufio.ReadWriter, tunnel *gpuDesktopTunnel, streamID uint64) {
	defer tunnel.sendClose(streamID)
	if reader != nil && reader.Reader.Buffered() > 0 {
		buffered, _ := io.ReadAll(reader.Reader)
		if len(buffered) > 0 && !tunnel.sendData(streamID, buffered) {
			return
		}
	}
	buf := make([]byte, gpuDesktopTunnelChunkSize)
	for {
		n, err := conn.Read(buf)
		if n > 0 && !tunnel.sendData(streamID, buf[:n]) {
			return
		}
		if err != nil {
			return
		}
	}
}

func buildGPUDesktopRawRequest(r *http.Request, deviceID string, websocketUpgrade bool) ([]byte, string, error) {
	targetPath := strings.TrimPrefix(r.URL.Path, gpuDesktopProxyPrefix+"/"+deviceID)
	if targetPath == "" {
		targetPath = "/"
	}
	if !strings.HasPrefix(targetPath, "/") {
		targetPath = "/" + targetPath
	}
	if r.URL.RawQuery != "" {
		targetPath += "?" + r.URL.RawQuery
	}

	var out bytes.Buffer
	fmt.Fprintf(&out, "%s %s HTTP/1.1\r\n", r.Method, targetPath)
	out.WriteString("Host: 127.0.0.1:1701\r\n")
	for key, values := range r.Header {
		if strings.EqualFold(key, "Host") {
			continue
		}
		if !websocketUpgrade && strings.EqualFold(key, "Accept-Encoding") {
			continue
		}
		if !websocketUpgrade && strings.EqualFold(key, "Connection") {
			continue
		}
		for _, value := range values {
			fmt.Fprintf(&out, "%s: %s\r\n", key, value)
		}
	}
	if !websocketUpgrade {
		out.WriteString("Connection: close\r\n")
	}
	out.WriteString("\r\n")
	if r.Body != nil {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, "", err
		}
		_, _ = out.Write(body)
	}
	return out.Bytes(), targetPath, nil
}

func writeGPUDesktopHTTPResponse(w http.ResponseWriter, r *http.Request, rawResp *bytes.Buffer, deviceID, targetPath string) {
	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(rawResp.Bytes())), r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	prefix := gpuDesktopProxyPrefix + "/" + deviceID
	body = rewriteGPUDesktopBody(resp.Header.Get("Content-Type"), targetPath, r.URL.RawQuery, prefix, body)
	for key, values := range resp.Header {
		if strings.EqualFold(key, "Content-Length") {
			continue
		}
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

func rewriteGPUDesktopBody(contentType, targetPath, rawQuery, prefix string, body []byte) []byte {
	querySuffix := ""
	if rawQuery != "" {
		querySuffix = "?" + rawQuery
	}
	if strings.Contains(contentType, "text/html") {
		body = bytes.ReplaceAll(body, []byte(`href="style.css"`), []byte(fmt.Sprintf(`href="%s/style.css%s"`, prefix, querySuffix)))
		body = bytes.ReplaceAll(body, []byte(`src="lib.js"`), []byte(fmt.Sprintf(`src="%s/lib.js%s"`, prefix, querySuffix)))
		return body
	}
	if strings.Contains(contentType, "javascript") || strings.HasSuffix(targetPath, "/lib.js") {
		return bytes.ReplaceAll(body, []byte(`"/ws"`), []byte(fmt.Sprintf(`"%s/ws%s"`, prefix, querySuffix)))
	}
	return body
}

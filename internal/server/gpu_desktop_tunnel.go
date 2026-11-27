package server

import (
	"bufio"
	"bytes"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lxzan/gws"
	"rdev/internal/protocol"
	tframe "rdev/internal/transport"
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
	gpuDesktopTunnelSendQueue  = 256
	gpuDesktopProxyPrefix      = "/gpu-desktop"

	// A substream buffers at most this much device output. The device read
	// loop never waits for a browser: overflow fails that stream only.
	gpuDesktopStreamQueueBytes = 8 << 20
	// Bound on each browser socket write so a stalled tab cannot pin the
	// proxy goroutine that owns this substream.
	gpuDesktopBrowserWriteWait = 10 * time.Second
	// Request bodies are bounded and read with a deadline instead of being
	// buffered whole.
	gpuDesktopRequestBodyLimit = 32 << 20
	// Only HTML/JavaScript needs rewriting, so only those responses are
	// buffered, and never past this bound.
	gpuDesktopRewriteBodyLimit = 8 << 20
	// Cap on one response header line (the bufio reader size for ReadResponse).
	gpuDesktopResponseHeaderSize = 64 << 10
)

var (
	errGPUDesktopReadTimeout     = errors.New("gpu desktop stream read timeout")
	errGPUDesktopRequestTooLarge = errors.New("gpu desktop request body too large")
)

type gpuDesktopTunnelOpen struct {
	StreamID uint64 `json:"streamId"`
}

type gpuDesktopStream struct {
	dataMu   sync.Mutex
	id       uint64
	data     chan []byte
	buffered int
	closed   chan struct{}
	once     sync.Once
}

// release returns a consumed chunk's budget to the queue.
func (st *gpuDesktopStream) release(data []byte) {
	st.dataMu.Lock()
	st.buffered -= len(data)
	st.dataMu.Unlock()
}

type gpuDesktopTunnel struct {
	deviceID string
	conn     *gws.Conn
	stream   net.Conn
	streamMu sync.Mutex
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
	client   *ClientConn
	proof    string
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
	identity := strings.TrimSpace(r.URL.Query().Get("deviceIdentity"))
	proof := strings.TrimSpace(r.URL.Query().Get("reconnectProof"))
	if identity != client.DeviceIdentity || !s.validateReconnectProof(identity, proof) {
		http.Error(w, "invalid device reconnect proof", http.StatusUnauthorized)
		return
	}

	handler := &gpuDesktopTunnelHandler{srv: s, deviceID: deviceID, client: client, proof: proof}
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
	if !h.srv.registerGPUDesktopTunnel(tunnel, h.client, h.proof) {
		tunnel.close()
		return
	}
	h.proof = ""
	h.tunnel = tunnel
	go tunnel.writeLoop()
}

func (h *gpuDesktopTunnelHandler) OnMessage(socket *gws.Conn, message *gws.Message) {
	defer message.Close()
	if h.tunnel == nil || message.Opcode != gws.OpcodeBinary {
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
		send:     make(chan []byte, gpuDesktopTunnelSendQueue),
		streams:  make(map[uint64]*gpuDesktopStream),
		closed:   make(chan struct{}),
	}
}

func newGPUDesktopStreamTunnel(deviceID string, conn net.Conn) *gpuDesktopTunnel {
	return &gpuDesktopTunnel{
		deviceID: deviceID,
		stream:   conn,
		send:     make(chan []byte, gpuDesktopTunnelSendQueue),
		streams:  make(map[uint64]*gpuDesktopStream),
		closed:   make(chan struct{}),
	}
}

// Admission is serialized with revocation and control-connection replacement.
// Authentication before a WebSocket upgrade alone cannot prevent late publish.
func (s *Server) registerGPUDesktopTunnel(tunnel *gpuDesktopTunnel, client *ClientConn, proof string) bool {
	reg := s.identityRegistry()
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if client == nil || tunnel.deviceID != client.ID || s.loadIdentityLocked(reg) != nil {
		return false
	}
	record, ok := reg.records[client.DeviceIdentity]
	if !ok || record.Revoked || record.ClientID != client.ID || subtle.ConstantTimeCompare([]byte(record.ProofHash), []byte(hashIdentityProof(proof))) != 1 {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.clients[client.ID] != client {
		return false
	}
	s.gpuDesktopMu.Lock()
	old := s.gpuDesktopTunnels[tunnel.deviceID]
	s.gpuDesktopTunnels[tunnel.deviceID] = tunnel
	s.gpuDesktopMu.Unlock()
	if old != nil {
		old.close()
	}
	log.Printf("gpu desktop tunnel registered: device=%s", tunnel.deviceID)
	return true
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
			if err := t.writePayload(payload); err != nil {
				t.close()
				return
			}
		case <-ticker.C:
			if err := t.writePing(); err != nil {
				t.close()
				return
			}
		case <-t.closed:
			return
		}
	}
}

func (t *gpuDesktopTunnel) writePayload(payload []byte) error {
	if t.conn != nil {
		_ = t.conn.SetWriteDeadline(time.Now().Add(gpuDesktopTunnelWriteWait))
		return t.conn.WriteMessage(gws.OpcodeBinary, payload)
	}
	t.streamMu.Lock()
	defer t.streamMu.Unlock()
	_ = t.stream.SetWriteDeadline(time.Now().Add(gpuDesktopTunnelWriteWait))
	return tframe.WriteFrame(t.stream, tframe.KindBinary, payload)
}

func (t *gpuDesktopTunnel) writePing() error {
	if t.conn != nil {
		_ = t.conn.SetWriteDeadline(time.Now().Add(gpuDesktopTunnelWriteWait))
		return t.conn.WritePing(nil)
	}
	t.streamMu.Lock()
	defer t.streamMu.Unlock()
	_ = t.stream.SetWriteDeadline(time.Now().Add(gpuDesktopTunnelWriteWait))
	return tframe.WriteFrame(t.stream, tframe.KindPing, []byte("gpu"))
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

// dispatchData hands device output to one substream without ever blocking the
// shared device read loop. A browser that stops reading loses that substream,
// not the whole tunnel.
func (t *gpuDesktopTunnel) dispatchData(streamID uint64, data []byte) {
	t.mu.Lock()
	stream := t.streams[streamID]
	t.mu.Unlock()
	if stream == nil || len(data) == 0 {
		return
	}
	stream.dataMu.Lock()
	select {
	case <-stream.closed:
		stream.dataMu.Unlock()
		return
	default:
	}
	if stream.buffered+len(data) > gpuDesktopStreamQueueBytes {
		stream.dataMu.Unlock()
		t.failStream(stream, "output queue limit exceeded")
		return
	}
	payload := append([]byte(nil), data...)
	stream.buffered += len(payload)
	select {
	case stream.data <- payload:
		stream.dataMu.Unlock()
	case <-stream.closed:
		stream.buffered -= len(payload)
		stream.dataMu.Unlock()
	case <-t.closed:
		stream.buffered -= len(payload)
		stream.dataMu.Unlock()
	default:
		// Frame-count cap reached before the byte cap.
		stream.buffered -= len(payload)
		stream.dataMu.Unlock()
		t.failStream(stream, "output queue full")
	}
}

// failStream ends one substream, wakes its proxy handler through the closed
// data channel, and tells the device to stop producing. Other substreams on
// the same device keep running.
func (t *gpuDesktopTunnel) failStream(stream *gpuDesktopStream, reason string) {
	log.Printf("gpu desktop stream closed: device=%s stream=%d reason=%s", t.deviceID, stream.id, reason)
	t.sendClose(stream.id)
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
			stream.dataMu.Lock()
			close(stream.data)
			stream.dataMu.Unlock()
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
	default:
		log.Printf("gpu desktop tunnel send queue full: device=%s stream=%d frame=%d bytes=%d", t.deviceID, streamID, frameType, len(body))
		return false
	case <-t.closed:
		return false
	}
}

func (t *gpuDesktopTunnel) close() {
	t.once.Do(func() {
		close(t.closed)
		if t.conn != nil {
			_ = t.conn.WriteClose(1000, []byte("gpu desktop tunnel closed"))
		}
		if t.stream != nil {
			_ = t.stream.Close()
		}
		t.mu.Lock()
		for id, stream := range t.streams {
			delete(t.streams, id)
			stream.once.Do(func() {
				close(stream.closed)
				stream.dataMu.Lock()
				close(stream.data)
				stream.dataMu.Unlock()
			})
		}
		t.mu.Unlock()
	})
}

func (s *Server) handleGPUDesktopStreamTunnel(conn net.Conn, msg *protocol.Message) {
	deviceID := strings.TrimSpace(msg.ClientID)
	if deviceID == "" {
		deviceID = strings.TrimSpace(msg.RequestID)
	}
	if deviceID == "" {
		_ = conn.Close()
		return
	}
	client := s.clientByID(deviceID)
	if client == nil {
		_ = conn.Close()
		return
	}
	if msg.DeviceIdentity != client.DeviceIdentity || !s.validateReconnectProof(msg.DeviceIdentity, msg.ReconnectProof) {
		_ = conn.Close()
		return
	}
	tunnel := newGPUDesktopStreamTunnel(deviceID, conn)
	if !s.registerGPUDesktopTunnel(tunnel, client, msg.ReconnectProof) {
		tunnel.close()
		return
	}
	defer func() {
		s.unregisterGPUDesktopTunnel(tunnel)
		tunnel.close()
	}()
	go tunnel.writeLoop()
	_ = conn.SetReadDeadline(time.Now().Add(gpuDesktopTunnelReadWait))
	for {
		frame, err := tframe.ReadFrame(conn)
		if err != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(gpuDesktopTunnelReadWait))
		switch frame.Kind {
		case tframe.KindBinary:
			if len(frame.Payload) < 9 {
				continue
			}
			frameType := frame.Payload[0]
			streamID := binary.BigEndian.Uint64(frame.Payload[1:9])
			body := frame.Payload[9:]
			switch frameType {
			case gpuDesktopTunnelFrameData:
				tunnel.dispatchData(streamID, body)
			case gpuDesktopTunnelFrameClose:
				tunnel.closeStream(streamID)
			}
		case tframe.KindPing:
			tunnel.streamMu.Lock()
			_ = tframe.WriteFrame(conn, tframe.KindPong, frame.Payload)
			tunnel.streamMu.Unlock()
		case tframe.KindClose:
			return
		}
	}
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
	if client.Password != "" && !s.deviceAuthBypassed(r) && passwordFingerprint(r.URL.Query().Get("password")) != passwordFingerprint(client.Password) {
		http.Error(w, "wrong device password", http.StatusUnauthorized)
		return
	}
	if gpuDesktopIsServerFrontendPath(r.URL.Path, deviceID) {
		serveGPUDesktopFrontend(w, r, deviceID)
		return
	}
	tunnel := s.gpuDesktopTunnel(deviceID)
	if tunnel == nil {
		http.Error(w, "remote desktop transport is not connected", http.StatusBadGateway)
		return
	}
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		s.proxyGPUDesktopWebSocket(w, r, tunnel, deviceID)
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

func gpuDesktopIsServerFrontendPath(path, deviceID string) bool {
	targetPath := strings.TrimPrefix(path, gpuDesktopProxyPrefix+"/"+deviceID)
	return targetPath == "" || targetPath == "/" || targetPath == "/index.html"
}

func serveGPUDesktopFrontend(w http.ResponseWriter, r *http.Request, deviceID string) {
	query := r.URL.Query()
	query.Set("device", deviceID)
	http.Redirect(w, r, "/remote-desktop?"+query.Encode(), http.StatusFound)
}

// gpuDesktopStreamReader adapts one substream to io.Reader for the proxied
// HTTP response. Chunks are released to the substream budget as they are
// consumed, and an idle upstream fails the read instead of pinning this proxy
// goroutine until the client gives up.
type gpuDesktopStreamReader struct {
	stream *gpuDesktopStream
	tunnel *gpuDesktopTunnel
	idle   time.Duration
	chunk  []byte
}

func (r *gpuDesktopStreamReader) Read(p []byte) (int, error) {
	for len(r.chunk) == 0 {
		if err := r.fill(); err != nil {
			return 0, err
		}
	}
	n := copy(p, r.chunk)
	r.chunk = r.chunk[n:]
	return n, nil
}

func (r *gpuDesktopStreamReader) fill() error {
	select {
	case data, ok := <-r.stream.data:
		if !ok {
			return io.EOF
		}
		r.stream.release(data)
		r.chunk = data
		return nil
	default:
	}
	timer := time.NewTimer(r.idle)
	defer timer.Stop()
	select {
	case data, ok := <-r.stream.data:
		if !ok {
			return io.EOF
		}
		r.stream.release(data)
		r.chunk = data
		return nil
	case <-r.tunnel.closed:
		return io.ErrUnexpectedEOF
	case <-timer.C:
		return errGPUDesktopReadTimeout
	}
}

// gpuDesktopNeedsRewrite reports whether the body must be buffered so the
// device-side asset URLs can be pointed at the proxy prefix.
func gpuDesktopNeedsRewrite(contentType, targetPath string) bool {
	return strings.Contains(contentType, "text/html") ||
		strings.Contains(contentType, "javascript") ||
		strings.HasSuffix(targetPath, "/lib.js")
}

func writeGPUDesktopProxyError(w http.ResponseWriter, err error, what string) {
	switch {
	case errors.Is(err, errGPUDesktopReadTimeout):
		http.Error(w, "gpu desktop tunnel response timeout", http.StatusGatewayTimeout)
	case errors.Is(err, io.EOF):
		http.Error(w, "gpu desktop tunnel closed before "+what, http.StatusBadGateway)
	default:
		http.Error(w, "gpu desktop "+what+" failed: "+err.Error(), http.StatusBadGateway)
	}
}

func proxyGPUDesktopHTTP(w http.ResponseWriter, r *http.Request, tunnel *gpuDesktopTunnel, deviceID string) {
	stream, err := tunnel.openStream()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer tunnel.sendClose(stream.id)

	// The request body is read before anything is sent, so a slow client can
	// only hold this one stream; the size limit keeps it bounded.
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(gpuDesktopTunnelOpenWait))
	rawReq, targetPath, err := buildGPUDesktopRawRequest(r, deviceID, false)
	_ = controller.SetReadDeadline(time.Time{})
	if err != nil {
		if errors.Is(err, errGPUDesktopRequestTooLarge) {
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !tunnel.sendData(stream.id, rawReq) {
		http.Error(w, "gpu desktop tunnel send failed", http.StatusBadGateway)
		return
	}

	// Headers are parsed as they arrive; the body is streamed unless the
	// content needs rewriting, which is bounded and fails loudly past the cap.
	reader := &gpuDesktopStreamReader{stream: stream, tunnel: tunnel, idle: gpuDesktopTunnelReadWait}
	resp, err := http.ReadResponse(bufio.NewReaderSize(reader, gpuDesktopResponseHeaderSize), r)
	if err != nil {
		writeGPUDesktopProxyError(w, err, "response headers")
		return
	}
	defer resp.Body.Close()

	for key, values := range resp.Header {
		if strings.EqualFold(key, "Content-Length") || strings.EqualFold(key, "Transfer-Encoding") || strings.EqualFold(key, "Connection") {
			continue
		}
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	contentType := resp.Header.Get("Content-Type")
	if gpuDesktopNeedsRewrite(contentType, targetPath) {
		body, err := io.ReadAll(io.LimitReader(resp.Body, gpuDesktopRewriteBodyLimit+1))
		if err != nil {
			writeGPUDesktopProxyError(w, err, "response body")
			return
		}
		if len(body) > gpuDesktopRewriteBodyLimit {
			http.Error(w, "gpu desktop response too large to rewrite", http.StatusBadGateway)
			return
		}
		body = rewriteGPUDesktopBody(contentType, targetPath, r.URL.RawQuery, gpuDesktopProxyPrefix+"/"+deviceID, body)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
		return
	}

	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		// Headers are gone, so a truncated upstream can only be reported by
		// aborting the connection; ending quietly would look like a complete
		// response with fewer bytes.
		if r.Context().Err() == nil {
			log.Printf("gpu desktop response truncated: device=%s path=%s err=%v", deviceID, targetPath, err)
		}
		panic(http.ErrAbortHandler)
	}
}

// writeGPUDesktopBrowser bounds one browser socket write so a stalled tab
// cannot pin the proxy goroutine that owns the substream.
func writeGPUDesktopBrowser(conn net.Conn, data []byte) error {
	if err := conn.SetWriteDeadline(time.Now().Add(gpuDesktopBrowserWriteWait)); err != nil {
		return err
	}
	_, err := conn.Write(data)
	return err
}

func (s *Server) proxyGPUDesktopWebSocket(w http.ResponseWriter, r *http.Request, tunnel *gpuDesktopTunnel, deviceID string) {
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
	go s.pipeGPUDesktopClientToTunnel(clientBuf.Reader, tunnel, stream.id, deviceID)
	for {
		data, ok := <-stream.data
		if !ok {
			return
		}
		stream.release(data)
		if err := writeGPUDesktopBrowser(clientConn, data); err != nil {
			log.Printf("gpu desktop browser write failed: device=%s stream=%d bytes=%d err=%v", deviceID, stream.id, len(data), err)
			return
		}
	}
}

// Preserve native websocket bytes, including masking. Inspect only browser-to-
// device messages so human input drains Agent input before entering the tunnel.
func (s *Server) pipeGPUDesktopClientToTunnel(reader io.Reader, tunnel *gpuDesktopTunnel, streamID uint64, deviceID string) {
	defer tunnel.sendClose(streamID)
	for {
		frame, manual, err := readGPUDesktopClientFrame(reader)
		if err != nil {
			return
		}
		var release func()
		if manual {
			release = s.beginDesktopManualInput(deviceID)
		}
		sent := tunnel.sendData(streamID, frame)
		if release != nil {
			release()
		}
		if !sent {
			return
		}
	}
}

func readGPUDesktopClientFrame(reader io.Reader) ([]byte, bool, error) {
	var header [14]byte
	if _, err := io.ReadFull(reader, header[:2]); err != nil {
		return nil, false, err
	}
	n := 2
	size := uint64(header[1] & 127)
	if size == 126 {
		if _, err := io.ReadFull(reader, header[2:4]); err != nil {
			return nil, false, err
		}
		size, n = uint64(binary.BigEndian.Uint16(header[2:4])), 4
	} else if size == 127 {
		if _, err := io.ReadFull(reader, header[2:10]); err != nil {
			return nil, false, err
		}
		size, n = binary.BigEndian.Uint64(header[2:10]), 10
	}
	if size > 16<<20 || header[1]&128 == 0 {
		return nil, false, fmt.Errorf("invalid GPU browser websocket frame")
	}
	if _, err := io.ReadFull(reader, header[n:n+4]); err != nil {
		return nil, false, err
	}
	n += 4
	frame := make([]byte, n+int(size))
	copy(frame, header[:n])
	if _, err := io.ReadFull(reader, frame[n:]); err != nil {
		return nil, false, err
	}
	opcode := header[0] & 15
	if opcode >= 8 {
		return frame, false, nil
	}
	// Fragmented/compressed data may contain input not visible in this frame.
	if opcode != 1 || header[0]&0xf0 != 0x80 {
		return frame, true, nil
	}
	payload := make([]byte, int(size))
	for i := range payload {
		payload[i] = frame[n+i] ^ header[n-4+i%4]
	}
	var command string
	if json.Unmarshal(payload, &command) == nil {
		return frame, command == "ReleaseKeyboard", nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(payload, &fields) != nil {
		return frame, true, nil
	}
	for _, key := range []string{"PointerEvent", "KeyboardEvent", "WheelEvent", "TextInputEvent", "Cua", "AutoJs"} {
		if fields[key] != nil {
			return frame, true, nil
		}
	}
	return frame, false, nil
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
		body, err := io.ReadAll(io.LimitReader(r.Body, gpuDesktopRequestBodyLimit+1))
		if err != nil {
			return nil, "", err
		}
		if len(body) > gpuDesktopRequestBodyLimit {
			return nil, "", fmt.Errorf("%w (limit %d bytes)", errGPUDesktopRequestTooLarge, gpuDesktopRequestBodyLimit)
		}
		_, _ = out.Write(body)
	}
	return out.Bytes(), targetPath, nil
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

package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/lxzan/gws"
	"rdev/internal/protocol"
)

// Batch WebSocket protocol (optimized):
//
//	Browser → Server:  Text frame = JSON {op:"exec"|"upload", devices, command, path, data, mode}
//	Server → Browser:  Binary frame = exec output [1 byte:0x01] [1 byte:devIdLen] [devId] [raw output]
//	                   Text frame  = JSON {op:"exec_exit"|"upload_result"|"error", deviceId, ...}

type batchMsg struct {
	Op       string   `json:"op"`
	Devices  []string `json:"devices,omitempty"`
	Command  string   `json:"command,omitempty"`
	Path     string   `json:"path,omitempty"`
	Data     string   `json:"data,omitempty"` // base64 for upload
	Mode     int32    `json:"mode,omitempty"`
	DeviceID string   `json:"deviceId,omitempty"`
	Password string   `json:"password,omitempty"`

	// Response fields (text frames)
	Code    int    `json:"code"`
	Success bool   `json:"success,omitempty"`
	Message string `json:"message,omitempty"`
}

// Batch binary output frame: [1 byte: 0x01] [1 byte: devIdLen] [devId] [raw output]
const batchBinOutput byte = 0x01

type batchWSHandler struct {
	gws.BuiltinEventHandler
	srv *Server
}

type batchUploadMeta struct {
	Devices []string `json:"devices"`
	Path    string   `json:"path"`
	Mode    int32    `json:"mode"`
}

type batchSocket struct {
	conn       *gws.Conn
	mu         sync.Mutex
	authMu     sync.RWMutex
	authorized map[string]string
	authFails  map[string]int
	uploads    map[string]*batchUpload
}

type batchUpload struct {
	devices []string
	path    string
	mode    int32
	jobs    map[string]chan uploadChunk
	ackCh   chan string
}

type uploadChunk struct {
	data []byte
	ack  chan struct{}
}

func (s *batchSocket) WriteText(msg batchMsg) {
	data, _ := json.Marshal(msg)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conn.WriteMessage(gws.OpcodeText, data)
}

func (s *batchSocket) WriteOutput(deviceID string, output []byte) {
	frame := protocol.EncodeBinFrame(batchBinOutput, deviceID, output)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conn.WriteMessage(gws.OpcodeBinary, frame)
}

func (s *batchSocket) sendUploadAck(uploadID string) {
	frame := protocol.EncodeBinFrame(protocol.BinFileAck, uploadID, nil)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conn.WriteMessage(gws.OpcodeBinary, frame)
}

func (h *batchWSHandler) OnOpen(socket *gws.Conn) {
	socket.Session().Store("batchSocket", &batchSocket{
		conn:       socket,
		authorized: make(map[string]string),
		authFails:  make(map[string]int),
		uploads:    make(map[string]*batchUpload),
	})
}
func (h *batchWSHandler) OnClose(socket *gws.Conn, err error) {}

func (h *batchWSHandler) OnMessage(socket *gws.Conn, message *gws.Message) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC in batch OnMessage: %v", r)
		}
	}()
	defer message.Close()

	bsRaw, _ := socket.Session().Load("batchSocket")
	if bsRaw == nil {
		return
	}
	bs := bsRaw.(*batchSocket)

	if message.Opcode == gws.OpcodeBinary {
		go h.handleBinaryMessage(bs, message.Bytes())
		return
	}

	// Only text frames for commands
	if message.Opcode != gws.OpcodeText {
		return
	}

	var bmsg batchMsg
	if err := json.Unmarshal(message.Bytes(), &bmsg); err != nil {
		return
	}

	switch bmsg.Op {
	case "auth":
		h.handleBatchAuth(bs, bmsg)
	case "exec":
		go h.handleBatchExec(bs, bmsg)
	case "upload":
		go h.handleBatchUpload(bs, bmsg)
	}
}

func (h *batchWSHandler) handleBatchAuth(socket *batchSocket, msg batchMsg) {
	if msg.DeviceID == "" {
		socket.WriteText(batchMsg{Op: "auth_fail", Message: "missing device"})
		return
	}
	h.srv.mu.RLock()
	client, ok := h.srv.clients[msg.DeviceID]
	h.srv.mu.RUnlock()
	if !ok {
		socket.WriteText(batchMsg{Op: "auth_fail", DeviceID: msg.DeviceID, Message: "device not connected"})
		return
	}
	if client.Password == "" || constantTimeEqual(client.Password, msg.Password) {
		socket.authMu.Lock()
		socket.authorized[msg.DeviceID] = passwordFingerprint(client.Password)
		delete(socket.authFails, msg.DeviceID)
		socket.authMu.Unlock()
		socket.WriteText(batchMsg{Op: "auth_ok", DeviceID: msg.DeviceID})
		return
	}
	socket.authMu.Lock()
	socket.authFails[msg.DeviceID]++
	failures := socket.authFails[msg.DeviceID]
	socket.authMu.Unlock()
	if failures > 3 {
		time.Sleep(time.Duration(failures-3) * 300 * time.Millisecond)
	}
	socket.WriteText(batchMsg{Op: "auth_fail", DeviceID: msg.DeviceID, Message: "wrong password"})
}

func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func passwordFingerprint(password string) string {
	sum := sha256.Sum256([]byte(password))
	return hex.EncodeToString(sum[:])
}

func (h *batchWSHandler) handleBinaryMessage(socket *batchSocket, raw []byte) {
	typ, id, payload, err := protocol.DecodeBinFrame(raw)
	if err != nil {
		socket.WriteText(batchMsg{Op: "error", Message: "invalid binary upload frame"})
		return
	}
	switch typ {
	case protocol.BinFilePut:
		path, mode, data, err := protocol.DecodeBinFilePut(payload)
		if err != nil {
			socket.WriteText(batchMsg{Op: "error", Message: "invalid binary upload payload"})
			return
		}
		meta, ok := decodeBatchUploadMeta(socket, path)
		if !ok {
			return
		}
		if meta.Mode != 0 {
			mode = meta.Mode
		}
		h.handleBatchUploadBytes(socket, meta.Devices, meta.Path, mode, data)
	case protocol.BinFileStart:
		path, mode, _, err := protocol.DecodeBinFilePut(payload)
		if err != nil {
			socket.WriteText(batchMsg{Op: "error", Message: "invalid binary upload start"})
			return
		}
		meta, ok := decodeBatchUploadMeta(socket, path)
		if !ok {
			return
		}
		if meta.Mode != 0 {
			mode = meta.Mode
		}
		h.startStreamUpload(socket, id, meta.Devices, meta.Path, mode)
	case protocol.BinFileChunk:
		socket.mu.Lock()
		u := socket.uploads[id]
		socket.mu.Unlock()
		if u == nil {
			socket.WriteText(batchMsg{Op: "error", Message: "upload stream not found"})
			return
		}
		for _, ch := range u.jobs {
			buf := make([]byte, len(payload))
			copy(buf, payload)
			ack := make(chan struct{})
			ch <- uploadChunk{data: buf, ack: ack}
			select {
			case <-ack:
			case <-time.After(30 * time.Second):
				socket.WriteText(batchMsg{Op: "error", Message: "upload chunk timeout"})
				return
			}
		}
		socket.sendUploadAck(id)
	case protocol.BinFileEnd:
		socket.mu.Lock()
		u := socket.uploads[id]
		delete(socket.uploads, id)
		socket.mu.Unlock()
		if u == nil {
			return
		}
		for _, ch := range u.jobs {
			close(ch)
		}
	}
}

func decodeBatchUploadMeta(socket *batchSocket, raw string) (batchUploadMeta, bool) {
	var meta batchUploadMeta
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		socket.WriteText(batchMsg{Op: "error", Message: "invalid upload metadata"})
		return meta, false
	}
	if meta.Path == "" || len(meta.Devices) == 0 {
		socket.WriteText(batchMsg{Op: "error", Message: "missing path or devices"})
		return meta, false
	}
	return meta, true
}

func (h *batchWSHandler) handleBatchExec(socket *batchSocket, bmsg batchMsg) {
	if bmsg.Command == "" || len(bmsg.Devices) == 0 {
		socket.WriteText(batchMsg{Op: "error", Message: "missing command or devices"})
		return
	}
	devices := h.authorizedDevices(socket, bmsg.Devices)
	if len(devices) == 0 {
		return
	}

	var wg sync.WaitGroup
	limit := h.srv.BatchConcurrency
	if limit <= 0 {
		limit = runtime.GOMAXPROCS(0) * 8
	}
	sem := make(chan struct{}, limit)
	for _, deviceID := range devices {
		h.srv.mu.RLock()
		client, ok := h.srv.clients[deviceID]
		h.srv.mu.RUnlock()
		if !ok {
			socket.WriteText(batchMsg{Op: "error", DeviceID: deviceID, Message: "device not connected"})
			continue
		}

		wg.Add(1)
		go func(deviceID string, client *ClientConn) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			h.execOnDevice(socket, client, deviceID, bmsg.Command)
		}(deviceID, client)
	}
	wg.Wait()
}

func (h *batchWSHandler) execOnDevice(socket *batchSocket, client *ClientConn, deviceID, command string) {
	sessionID := generateID()

	proxySess := &ProxySession{
		ID:       sessionID,
		ClientID: deviceID,
		WriteCh:  make(chan []byte, 8192),
		StderrCh: make(chan []byte, 2048),
		CloseCh:  make(chan struct{}, 1),
		Done:     make(chan struct{}),
		exitDone: make(chan struct{}),
		CloseSSH: func() {},
		ExitSSH:  func(code int) {},
	}
	proxySess.SetSessionMeta(false, "", command, "", 0, 0)
	if !h.srv.RegisterSession(proxySess, client) {
		socket.WriteText(batchMsg{Op: "error", DeviceID: deviceID, Message: "too many active sessions"})
		return
	}

	if err := client.Send(&protocol.Message{
		Type:      protocol.MsgNewSession,
		ClientID:  deviceID,
		SessionID: sessionID,
		Command:   command,
		Pty:       false,
	}); err != nil {
		socket.WriteText(batchMsg{Op: "error", DeviceID: deviceID, Message: fmt.Sprintf("failed to reach device: %v", err)})
		h.srv.removeSession(sessionID)
		return
	}

	go func() {
		defer func() {
			h.srv.removeSession(sessionID)
			client.mu.Lock()
			delete(client.Sessions, sessionID)
			client.mu.Unlock()
		}()

		for {
			select {
			case data, ok := <-proxySess.WriteCh:
				if !ok {
					continue
				}
				socket.WriteOutput(deviceID, data)

			case data, ok := <-proxySess.StderrCh:
				if !ok {
					continue
				}
				socket.WriteOutput(deviceID, data)

			case <-proxySess.CloseCh:
				proxySess.NotifyObserversClose()
				proxySess.CloseOutput()
				socket.WriteText(batchMsg{
					Op:       "exec_exit",
					DeviceID: deviceID,
					Code:     proxySess.WaitExitCode(500 * time.Millisecond),
				})
				return
			}
		}
	}()
}

func (h *batchWSHandler) handleBatchUpload(socket *batchSocket, bmsg batchMsg) {
	if bmsg.Path == "" || bmsg.Data == "" || len(bmsg.Devices) == 0 {
		socket.WriteText(batchMsg{Op: "error", Message: "missing path, data or devices"})
		return
	}
	devices := h.authorizedDevices(socket, bmsg.Devices)
	if len(devices) == 0 {
		return
	}

	rawData, err := protocol.DecodeData(bmsg.Data)
	if err != nil {
		socket.WriteText(batchMsg{Op: "error", Message: "invalid base64 data"})
		return
	}
	h.handleBatchUploadBytes(socket, devices, bmsg.Path, bmsg.Mode, rawData)
}

func (h *batchWSHandler) handleBatchUploadBytes(socket *batchSocket, devices []string, path string, mode int32, rawData []byte) {
	devices = h.authorizedDevices(socket, devices)
	if len(devices) == 0 {
		return
	}
	var wg sync.WaitGroup
	limit := h.srv.BatchConcurrency
	if limit <= 0 {
		limit = runtime.GOMAXPROCS(0) * 8
	}
	sem := make(chan struct{}, limit)
	for _, deviceID := range devices {
		h.srv.mu.RLock()
		client, ok := h.srv.clients[deviceID]
		h.srv.mu.RUnlock()
		if !ok {
			socket.WriteText(batchMsg{Op: "error", DeviceID: deviceID, Message: "device not connected"})
			continue
		}

		wg.Add(1)
		go func(deviceID string, client *ClientConn) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			h.uploadToDevice(socket, client, deviceID, path, rawData, mode)
		}(deviceID, client)
	}
	wg.Wait()
}

func (h *batchWSHandler) uploadToDevice(socket *batchSocket, client *ClientConn, deviceID, path string, rawData []byte, mode int32) {
	filePutID := generateID()
	resultCh := make(chan *protocol.Message, 1)
	h.srv.RegisterFileResult(filePutID, resultCh)
	defer h.srv.unregisterFileResult(filePutID)

	// Send small/legacy uploads as a single binary frame.
	if err := client.SendFilePut(filePutID, path, mode, rawData); err != nil {
		socket.WriteText(batchMsg{Op: "error", DeviceID: deviceID, Message: "failed to reach device"})
		return
	}

	h.waitUploadResult(socket, resultCh, deviceID, path)
}

func (h *batchWSHandler) startStreamUpload(socket *batchSocket, uploadID string, devices []string, path string, mode int32) {
	devices = h.authorizedDevices(socket, devices)
	if len(devices) == 0 {
		return
	}
	limit := h.srv.BatchConcurrency
	if limit <= 0 {
		limit = runtime.GOMAXPROCS(0) * 8
	}
	sem := make(chan struct{}, limit)
	u := &batchUpload{devices: devices, path: path, mode: mode, jobs: make(map[string]chan uploadChunk)}

	for _, deviceID := range devices {
		h.srv.mu.RLock()
		client, ok := h.srv.clients[deviceID]
		h.srv.mu.RUnlock()
		if !ok {
			socket.WriteText(batchMsg{Op: "error", DeviceID: deviceID, Message: "device not connected"})
			continue
		}
		ch := make(chan uploadChunk, 8)
		u.jobs[deviceID] = ch
		go func(deviceID string, client *ClientConn, chunks <-chan uploadChunk) {
			sem <- struct{}{}
			defer func() { <-sem }()
			h.streamUploadToDevice(socket, client, deviceID, path, mode, chunks)
		}(deviceID, client, ch)
	}
	if len(u.jobs) == 0 {
		return
	}
	socket.mu.Lock()
	socket.uploads[uploadID] = u
	socket.mu.Unlock()
}

func (h *batchWSHandler) streamUploadToDevice(socket *batchSocket, client *ClientConn, deviceID, path string, mode int32, chunks <-chan uploadChunk) {
	filePutID := generateID()
	resultCh := make(chan *protocol.Message, 1)
	h.srv.RegisterFileResult(filePutID, resultCh)
	defer h.srv.unregisterFileResult(filePutID)

	if err := client.SendFileStart(filePutID, path, mode); err != nil {
		socket.WriteText(batchMsg{Op: "error", DeviceID: deviceID, Message: "failed to start upload"})
		return
	}
	for chunk := range chunks {
		if err := client.SendFileChunk(filePutID, chunk.data); err != nil {
			socket.WriteText(batchMsg{Op: "error", DeviceID: deviceID, Message: "failed to send upload chunk"})
			close(chunk.ack)
			return
		}
		close(chunk.ack)
	}
	if err := client.SendFileEnd(filePutID); err != nil {
		socket.WriteText(batchMsg{Op: "error", DeviceID: deviceID, Message: "failed to finish upload"})
		return
	}

	h.waitUploadResult(socket, resultCh, deviceID, path)
}

func (h *batchWSHandler) waitUploadResult(socket *batchSocket, resultCh <-chan *protocol.Message, deviceID, path string) {
	select {
	case result := <-resultCh:
		socket.WriteText(batchMsg{
			Op:       "upload_result",
			DeviceID: deviceID,
			Path:     path,
			Success:  result.Success,
			Message:  result.Error,
		})
	case <-time.After(30 * time.Second):
		socket.WriteText(batchMsg{
			Op:       "upload_result",
			DeviceID: deviceID,
			Path:     path,
			Success:  false,
			Message:  "timeout",
		})
	}
}

func (h *batchWSHandler) authorizedDevices(socket *batchSocket, devices []string) []string {
	allowed := make([]string, 0, len(devices))
	for _, deviceID := range devices {
		h.srv.mu.RLock()
		client, ok := h.srv.clients[deviceID]
		h.srv.mu.RUnlock()
		if !ok {
			socket.WriteText(batchMsg{Op: "error", DeviceID: deviceID, Message: "device not connected"})
			continue
		}
		if h.isDeviceAuthorized(socket, client) {
			allowed = append(allowed, deviceID)
			continue
		}
		socket.WriteText(batchMsg{Op: "auth_required", DeviceID: deviceID, Message: "device password required"})
	}
	return allowed
}

func (h *batchWSHandler) isDeviceAuthorized(socket *batchSocket, client *ClientConn) bool {
	if client.Password == "" {
		return true
	}
	socket.authMu.RLock()
	fingerprint, ok := socket.authorized[client.ID]
	socket.authMu.RUnlock()
	return ok && constantTimeEqual(fingerprint, passwordFingerprint(client.Password))
}

// HandleFileUpload handles HTTP file upload, returns base64-encoded content
func (s *Server) HandleFileUpload(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuth(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 100*1024*1024)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("read error: %v", err), http.StatusBadRequest)
		return
	}

	filename := r.URL.Query().Get("name")
	if filename == "" {
		filename = "upload"
	}

	type uploadResponse struct {
		Size int64  `json:"size"`
		Data string `json:"data"`
		Name string `json:"name"`
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(uploadResponse{
		Size: int64(len(data)),
		Data: protocol.EncodeData(data),
		Name: filename,
	})
}

// HandleBatchDevicesAPI returns device metadata for the Batch page.
// This endpoint is intentionally not protected by the admin token; Batch actions
// enforce each target device's own password before executing or writing files.
func (s *Server) HandleBatchDevicesAPI(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	type deviceInfo struct {
		ID          string `json:"id"`
		ConnectedAt string `json:"connectedAt"`
		HasPassword bool   `json:"hasPassword"`
	}

	devices := make([]deviceInfo, 0, len(s.clients))
	for _, c := range s.clients {
		devices = append(devices, deviceInfo{
			ID:          c.ID,
			ConnectedAt: c.ConnectedAt.Format(time.RFC3339),
			HasPassword: c.Password != "",
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(devices)
}

// HandleBatchWS handles browser batch WebSocket connections
func (s *Server) HandleBatchWS(w http.ResponseWriter, r *http.Request) {
	upgrader := gws.NewUpgrader(&batchWSHandler{srv: s}, &gws.ServerOption{
		ReadMaxPayloadSize: 100 * 1024 * 1024,
		ParallelGolimit:    runtime.GOMAXPROCS(0),
		PermessageDeflate: gws.PermessageDeflate{
			Enabled:               true,
			ServerContextTakeover: true,
			ClientContextTakeover: true,
			Threshold:             256,
		},
	})

	socket, err := upgrader.Upgrade(w, r)
	if err != nil {
		log.Printf("batch ws upgrade error: %v", err)
		return
	}
	socket.ReadLoop()
}

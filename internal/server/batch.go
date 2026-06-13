package server

import (
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
	Op      string   `json:"op"`
	Devices []string `json:"devices,omitempty"`
	Command string   `json:"command,omitempty"`
	Path    string   `json:"path,omitempty"`
	Data    string   `json:"data,omitempty"` // base64 for upload
	Mode    int32    `json:"mode,omitempty"`

	// Response fields (text frames)
	DeviceID string `json:"deviceId,omitempty"`
	Code     int    `json:"code"`
	Success  bool   `json:"success,omitempty"`
	Message  string `json:"message,omitempty"`
}

// Batch binary output frame: [1 byte: 0x01] [1 byte: devIdLen] [devId] [raw output]
const batchBinOutput byte = 0x01

type batchWSHandler struct {
	gws.BuiltinEventHandler
	srv *Server
}

func (h *batchWSHandler) OnOpen(socket *gws.Conn)  {}
func (h *batchWSHandler) OnClose(socket *gws.Conn, err error) {}

func (h *batchWSHandler) OnMessage(socket *gws.Conn, message *gws.Message) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC in batch OnMessage: %v", r)
		}
	}()
	defer message.Close()

	// Only text frames for commands
	if message.Opcode != gws.OpcodeText {
		return
	}

	var bmsg batchMsg
	if err := json.Unmarshal(message.Bytes(), &bmsg); err != nil {
		return
	}

	switch bmsg.Op {
	case "exec":
		h.handleBatchExec(socket, bmsg)
	case "upload":
		h.handleBatchUpload(socket, bmsg)
	}
}

func (h *batchWSHandler) sendBatchText(socket *gws.Conn, msg batchMsg) {
	data, _ := json.Marshal(msg)
	socket.WriteMessage(gws.OpcodeText, data)
}

func (h *batchWSHandler) sendBatchOutput(socket *gws.Conn, deviceID string, output []byte) {
	frame := protocol.EncodeBinFrame(batchBinOutput, deviceID, output)
	socket.WriteMessage(gws.OpcodeBinary, frame)
}

func (h *batchWSHandler) handleBatchExec(socket *gws.Conn, bmsg batchMsg) {
	if bmsg.Command == "" || len(bmsg.Devices) == 0 {
		h.sendBatchText(socket, batchMsg{Op: "error", Message: "missing command or devices"})
		return
	}

	var wg sync.WaitGroup
	for _, deviceID := range bmsg.Devices {
		h.srv.mu.RLock()
		client, ok := h.srv.clients[deviceID]
		h.srv.mu.RUnlock()
		if !ok {
			h.sendBatchText(socket, batchMsg{Op: "error", DeviceID: deviceID, Message: "device not connected"})
			continue
		}

		wg.Add(1)
		go func(deviceID string, client *ClientConn) {
			defer wg.Done()
			h.execOnDevice(socket, client, deviceID, bmsg.Command)
		}(deviceID, client)
	}
	wg.Wait()
}

func (h *batchWSHandler) execOnDevice(socket *gws.Conn, client *ClientConn, deviceID, command string) {
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
	h.srv.RegisterSession(proxySess, client)

	if err := client.Send(&protocol.Message{
		Type:      protocol.MsgNewSession,
		ClientID:  deviceID,
		SessionID: sessionID,
		Command:   command,
		Pty:       false,
	}); err != nil {
		h.sendBatchText(socket, batchMsg{Op: "error", DeviceID: deviceID, Message: fmt.Sprintf("failed to reach device: %v", err)})
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
					h.sendBatchText(socket, batchMsg{
						Op:       "exec_exit",
						DeviceID: deviceID,
						Code:     proxySess.WaitExitCode(500*time.Millisecond),
					})
					return
				}
				h.sendBatchOutput(socket, deviceID, data)

			case data, ok := <-proxySess.StderrCh:
				if !ok {
					return
				}
				h.sendBatchOutput(socket, deviceID, data)

			case <-proxySess.CloseCh:
				close(proxySess.WriteCh)
				close(proxySess.StderrCh)
				h.sendBatchText(socket, batchMsg{
					Op:       "exec_exit",
					DeviceID: deviceID,
					Code:     proxySess.WaitExitCode(500*time.Millisecond),
				})
				return
			}
		}
	}()
}

func (h *batchWSHandler) handleBatchUpload(socket *gws.Conn, bmsg batchMsg) {
	if bmsg.Path == "" || bmsg.Data == "" || len(bmsg.Devices) == 0 {
		h.sendBatchText(socket, batchMsg{Op: "error", Message: "missing path, data or devices"})
		return
	}

	if _, err := protocol.DecodeData(bmsg.Data); err != nil {
		h.sendBatchText(socket, batchMsg{Op: "error", Message: "invalid base64 data"})
		return
	}

	var wg sync.WaitGroup
	for _, deviceID := range bmsg.Devices {
		h.srv.mu.RLock()
		client, ok := h.srv.clients[deviceID]
		h.srv.mu.RUnlock()
		if !ok {
			h.sendBatchText(socket, batchMsg{Op: "error", DeviceID: deviceID, Message: "device not connected"})
			continue
		}

		wg.Add(1)
		go func(deviceID string, client *ClientConn) {
			defer wg.Done()
			h.uploadToDevice(socket, client, deviceID, bmsg.Path, bmsg.Data, bmsg.Mode)
		}(deviceID, client)
	}
	wg.Wait()
}

func (h *batchWSHandler) uploadToDevice(socket *gws.Conn, client *ClientConn, deviceID, path, data string, mode int32) {
	filePutID := generateID()
	resultCh := make(chan *protocol.Message, 1)
	h.srv.RegisterFileResult(filePutID, resultCh)
	defer h.srv.unregisterFileResult(filePutID)

	// Decode base64 to raw bytes for binary frame
	rawData, err := protocol.DecodeData(data)
	if err != nil {
		h.sendBatchText(socket, batchMsg{Op: "error", DeviceID: deviceID, Message: "base64 decode error"})
		return
	}

	// Send file to device via binary frame
	if err := client.SendFilePut(filePutID, path, mode, rawData); err != nil {
		h.sendBatchText(socket, batchMsg{Op: "error", DeviceID: deviceID, Message: "failed to reach device"})
		return
	}

	select {
	case result := <-resultCh:
		h.sendBatchText(socket, batchMsg{
			Op:       "upload_result",
			DeviceID: deviceID,
			Path:     path,
			Success:  result.Success,
			Message:  result.Error,
		})
	case <-time.After(30 * time.Second):
		h.sendBatchText(socket, batchMsg{
			Op:       "upload_result",
			DeviceID: deviceID,
			Path:     path,
			Success:  false,
			Message:  "timeout",
		})
	}
}

// HandleFileUpload handles HTTP file upload, returns base64-encoded content
func (s *Server) HandleFileUpload(w http.ResponseWriter, r *http.Request) {
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

// HandleBatchWS handles browser batch WebSocket connections
func (s *Server) HandleBatchWS(w http.ResponseWriter, r *http.Request) {
	upgrader := gws.NewUpgrader(&batchWSHandler{srv: s}, &gws.ServerOption{
		ReadMaxPayloadSize: 100 * 1024 * 1024,
		ParallelGolimit:    runtime.GOMAXPROCS(0),
		PermessageDeflate: gws.PermessageDeflate{
			Enabled:               true,
			ServerContextTakeover:  true,
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

package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/lxzan/gws"
	"rdev/internal/protocol"
)

// Batch WebSocket protocol between browser and server:
//
//	Browser → Server:  {"op":"exec",    "devices":["a","b"], "command":"uname -a"}
//	Browser → Server:  {"op":"upload",  "devices":["a","b"], "path":"/tmp/f.txt", "data":"<base64>", "mode":420}
//	Server → Browser:  {"op":"exec_output", "deviceId":"a", "sessionId":"...", "data":"<base64>"}
//	Server → Browser:  {"op":"exec_exit",   "deviceId":"a", "sessionId":"...", "code":0}
//	Server → Browser:  {"op":"upload_result","deviceId":"a", "path":"/tmp/f.txt", "success":true}
//	Server → Browser:  {"op":"error",       "deviceId":"a", "message":"..."}

type batchMsg struct {
	Op      string   `json:"op"`                // "exec", "upload", "exec_output", "exec_exit", "upload_result", "error"
	Devices []string `json:"devices,omitempty"` // for exec/upload
	Command string   `json:"command,omitempty"` // for exec
	Path    string   `json:"path,omitempty"`    // for upload
	Data    string   `json:"data,omitempty"`    // base64 for upload
	Mode    int32    `json:"mode,omitempty"`    // file mode for upload

	// Response fields
	DeviceID  string `json:"deviceId,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
	Code      int    `json:"code,omitempty"`
	Success   bool   `json:"success,omitempty"`
	Message   string `json:"message,omitempty"`
}

// batchWSHandler implements gws.Event for browser batch connections
type batchWSHandler struct {
	gws.BuiltinEventHandler
	srv *Server
}

func (h *batchWSHandler) OnOpen(socket *gws.Conn) {
	// nothing to do
}

func (h *batchWSHandler) OnClose(socket *gws.Conn, err error) {
	// cleanup is handled per-batch-operation
}

func (h *batchWSHandler) OnMessage(socket *gws.Conn, message *gws.Message) {
	defer message.Close()

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

func (h *batchWSHandler) sendBatchMsg(socket *gws.Conn, msg batchMsg) {
	data, _ := json.Marshal(msg)
	socket.WriteMessage(gws.OpcodeText, data)
}

// handleBatchExec runs a command on multiple devices simultaneously
func (h *batchWSHandler) handleBatchExec(socket *gws.Conn, bmsg batchMsg) {
	if bmsg.Command == "" || len(bmsg.Devices) == 0 {
		h.sendBatchMsg(socket, batchMsg{Op: "error", Message: "missing command or devices"})
		return
	}

	var wg sync.WaitGroup
	for _, deviceID := range bmsg.Devices {
		h.srv.mu.RLock()
		client, ok := h.srv.clients[deviceID]
		h.srv.mu.RUnlock()
		if !ok {
			h.sendBatchMsg(socket, batchMsg{Op: "error", DeviceID: deviceID, Message: "device not connected"})
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
		WriteCh:  make(chan []byte, 4096),
		StderrCh: make(chan []byte, 256),
		CloseCh:  make(chan struct{}, 1),
		Done:     make(chan struct{}),
		CloseSSH: func() {},
		ExitSSH:  func(code int) {},
	}
	h.srv.RegisterSession(proxySess, client)

	// Send new exec session to device
	if err := client.Send(&protocol.Message{
		Type:      protocol.MsgNewSession,
		ClientID:  deviceID,
		SessionID: sessionID,
		Command:   command,
		Pty:       false,
	}); err != nil {
		h.sendBatchMsg(socket, batchMsg{Op: "error", DeviceID: deviceID, Message: "failed to reach device"})
		h.srv.removeSession(sessionID)
		return
	}

	// The command runs via `shell -c "command"` which will exit on its own.
	// No need to send MsgStdinClose for exec commands.

	// Read output and forward to browser
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
					h.sendBatchMsg(socket, batchMsg{
						Op:        "exec_exit",
						DeviceID:  deviceID,
						SessionID: sessionID,
						Code:      proxySess.GetExitCode(),
					})
					return
				}
				h.sendBatchMsg(socket, batchMsg{
					Op:        "exec_output",
					DeviceID:  deviceID,
					SessionID: sessionID,
					Data:      protocol.EncodeData(data),
				})

			case data, ok := <-proxySess.StderrCh:
				if !ok {
					return
				}
				h.sendBatchMsg(socket, batchMsg{
					Op:        "exec_output",
					DeviceID:  deviceID,
					SessionID: sessionID,
					Data:      protocol.EncodeData(data),
				})

			case <-proxySess.CloseCh:
				close(proxySess.WriteCh)
				close(proxySess.StderrCh)
				h.sendBatchMsg(socket, batchMsg{
					Op:        "exec_exit",
					DeviceID:  deviceID,
					SessionID: sessionID,
					Code:      proxySess.GetExitCode(),
				})
				return
			}
		}
	}()
}

// handleBatchUpload sends a file to multiple devices simultaneously
func (h *batchWSHandler) handleBatchUpload(socket *gws.Conn, bmsg batchMsg) {
	if bmsg.Path == "" || bmsg.Data == "" || len(bmsg.Devices) == 0 {
		h.sendBatchMsg(socket, batchMsg{Op: "error", Message: "missing path, data or devices"})
		return
	}

	// Verify the data is valid base64
	if _, err := protocol.DecodeData(bmsg.Data); err != nil {
		h.sendBatchMsg(socket, batchMsg{Op: "error", Message: "invalid base64 data"})
		return
	}

	var wg sync.WaitGroup
	for _, deviceID := range bmsg.Devices {
		h.srv.mu.RLock()
		client, ok := h.srv.clients[deviceID]
		h.srv.mu.RUnlock()
		if !ok {
			h.sendBatchMsg(socket, batchMsg{Op: "error", DeviceID: deviceID, Message: "device not connected"})
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

	// Send file to device
	if err := client.Send(&protocol.Message{
		Type:      protocol.MsgFilePut,
		SessionID: filePutID,
		FilePath:  path,
		Data:      data,
		FileMode:  mode,
	}); err != nil {
		h.sendBatchMsg(socket, batchMsg{Op: "error", DeviceID: deviceID, Message: "failed to reach device"})
		return
	}

	// Wait for result with timeout
	select {
	case result := <-resultCh:
		h.sendBatchMsg(socket, batchMsg{
			Op:       "upload_result",
			DeviceID: deviceID,
			Path:     path,
			Success:  result.Success,
			Message:  result.Error,
		})
	case <-time.After(30 * time.Second):
		h.sendBatchMsg(socket, batchMsg{
			Op:       "upload_result",
			DeviceID: deviceID,
			Path:     path,
			Success:  false,
			Message:  "timeout",
		})
	}
}

// File upload via HTTP (for large files that don't fit in a single WS message)
type uploadResponse struct {
	Size int64  `json:"size"`
	Data string `json:"data"` // base64
	Name string `json:"name"`
}

// HandleFileUpload handles HTTP file upload, returns base64-encoded content
func (s *Server) HandleFileUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	// Max 100MB
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
		ReadMaxPayloadSize: 100 * 1024 * 1024, // 100MB for file uploads
	})

	socket, err := upgrader.Upgrade(w, r)
	if err != nil {
		log.Printf("batch ws upgrade error: %v", err)
		return
	}
	socket.ReadLoop()
}

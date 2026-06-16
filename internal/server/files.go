package server

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/lxzan/gws"
	"rdev/internal/protocol"
)

type fileMsg struct {
	Op         string               `json:"op"`
	DeviceID   string               `json:"deviceId,omitempty"`
	RequestID  string               `json:"requestId,omitempty"`
	TaskID     string               `json:"taskId,omitempty"`
	Path       string               `json:"path,omitempty"`
	ParentPath string               `json:"parentPath,omitempty"`
	Name       string               `json:"name,omitempty"`
	Size       int64                `json:"size,omitempty"`
	Offset     int64                `json:"offset,omitempty"`
	ModTime    string               `json:"modTime,omitempty"`
	Password   string               `json:"password,omitempty"`
	Success    bool                 `json:"success,omitempty"`
	Message    string               `json:"message,omitempty"`
	Error      string               `json:"error,omitempty"`
	Entries    []protocol.FileEntry `json:"entries,omitempty"`
	Parent     string               `json:"parent,omitempty"`
	Home       string               `json:"home,omitempty"`
	Truncated  bool                 `json:"truncated,omitempty"`
}

type fileSocket struct {
	conn       *gws.Conn
	srv        *Server
	mu         sync.Mutex
	authMu     sync.RWMutex
	authorized map[string]string
	authFails  map[string]int
}

type fileTaskRoute struct {
	socket   *fileSocket
	deviceID string
}

type filesWSHandler struct {
	gws.BuiltinEventHandler
	srv *Server
}

func (s *fileSocket) writeText(msg fileMsg) {
	data, _ := json.Marshal(msg)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conn.WriteMessage(gws.OpcodeText, data)
}

func (s *fileSocket) writeBinary(raw []byte) {
	data := make([]byte, len(raw))
	copy(data, raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conn.WriteMessage(gws.OpcodeBinary, data)
}

func (h *filesWSHandler) OnOpen(socket *gws.Conn) {
	socket.Session().Store("fileSocket", &fileSocket{
		conn:       socket,
		srv:        h.srv,
		authorized: make(map[string]string),
		authFails:  make(map[string]int),
	})
}

func (h *filesWSHandler) OnClose(socket *gws.Conn, err error) {
	fsRaw, _ := socket.Session().Load("fileSocket")
	if fsRaw == nil {
		return
	}
	fs := fsRaw.(*fileSocket)
	h.srv.fileMu.Lock()
	for id, owner := range h.srv.fileRequests {
		if owner == fs {
			delete(h.srv.fileRequests, id)
		}
	}
	for id, route := range h.srv.fileTasks {
		if route.socket == fs {
			if client, ok := h.srv.GetClient(route.deviceID); ok {
				client.Send(&protocol.Message{Type: protocol.MsgFileTransferCancel, TaskID: id})
				client.SendBinaryOffset(protocol.BinFileTransferCancel, id, 0, nil)
			}
			delete(h.srv.fileTasks, id)
		}
	}
	h.srv.fileMu.Unlock()
}

func (h *filesWSHandler) OnMessage(socket *gws.Conn, message *gws.Message) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC in files OnMessage: %v", r)
		}
	}()
	defer message.Close()

	fsRaw, _ := socket.Session().Load("fileSocket")
	if fsRaw == nil {
		return
	}
	fs := fsRaw.(*fileSocket)

	if message.Opcode == gws.OpcodeBinary {
		h.handleBinary(fs, message.Bytes())
		return
	}
	if message.Opcode != gws.OpcodeText {
		return
	}
	var msg fileMsg
	if err := json.Unmarshal(message.Bytes(), &msg); err != nil {
		return
	}
	switch msg.Op {
	case "auth":
		h.handleAuth(fs, msg)
	case "list":
		h.handleList(fs, msg)
	case "upload_start":
		h.handleUploadStart(fs, msg)
	case "upload_end":
		h.forwardTaskControl(fs, msg, protocol.MsgFileUploadEnd)
	case "download_start":
		h.handleDownloadStart(fs, msg)
	case "cancel":
		h.handleCancel(fs, msg)
	}
}

func (h *filesWSHandler) handleAuth(socket *fileSocket, msg fileMsg) {
	if msg.DeviceID == "" {
		socket.writeText(fileMsg{Op: "auth_fail", Message: "missing device"})
		return
	}
	client, ok := h.srv.GetClient(msg.DeviceID)
	if !ok {
		socket.writeText(fileMsg{Op: "auth_fail", DeviceID: msg.DeviceID, Message: "device not connected"})
		return
	}
	if client.Password == "" || constantTimeEqual(client.Password, msg.Password) {
		socket.authMu.Lock()
		socket.authorized[msg.DeviceID] = passwordFingerprint(client.Password)
		delete(socket.authFails, msg.DeviceID)
		socket.authMu.Unlock()
		socket.writeText(fileMsg{Op: "auth_ok", DeviceID: msg.DeviceID})
		return
	}
	socket.authMu.Lock()
	socket.authFails[msg.DeviceID]++
	failures := socket.authFails[msg.DeviceID]
	socket.authMu.Unlock()
	if failures > 3 {
		time.Sleep(time.Duration(failures-3) * 300 * time.Millisecond)
	}
	socket.writeText(fileMsg{Op: "auth_fail", DeviceID: msg.DeviceID, Message: "wrong password"})
}

func (h *filesWSHandler) isAuthorized(socket *fileSocket, client *ClientConn) bool {
	if client.Password == "" {
		return true
	}
	socket.authMu.RLock()
	fp := socket.authorized[client.ID]
	socket.authMu.RUnlock()
	return fp != "" && fp == passwordFingerprint(client.Password)
}

func (h *filesWSHandler) clientFor(socket *fileSocket, deviceID string) (*ClientConn, bool) {
	client, ok := h.srv.GetClient(deviceID)
	if !ok {
		socket.writeText(fileMsg{Op: "error", DeviceID: deviceID, Message: "device not connected"})
		return nil, false
	}
	if !h.isAuthorized(socket, client) {
		socket.writeText(fileMsg{Op: "auth_required", DeviceID: deviceID, Message: "device password required"})
		return nil, false
	}
	return client, true
}

func (h *filesWSHandler) handleList(socket *fileSocket, msg fileMsg) {
	if msg.RequestID == "" {
		msg.RequestID = generateID()
	}
	client, ok := h.clientFor(socket, msg.DeviceID)
	if !ok {
		return
	}
	h.srv.fileMu.Lock()
	h.srv.fileRequests[msg.RequestID] = socket
	h.srv.fileMu.Unlock()
	if err := client.Send(&protocol.Message{Type: protocol.MsgFileListRequest, RequestID: msg.RequestID, Path: msg.Path}); err != nil {
		h.srv.removeFileRequest(msg.RequestID)
		socket.writeText(fileMsg{Op: "error", DeviceID: msg.DeviceID, RequestID: msg.RequestID, Message: "failed to reach device"})
	}
}

func (h *filesWSHandler) handleUploadStart(socket *fileSocket, msg fileMsg) {
	if msg.TaskID == "" {
		msg.TaskID = generateID()
	}
	client, ok := h.clientFor(socket, msg.DeviceID)
	if !ok {
		return
	}
	h.srv.registerFileTask(msg.TaskID, socket, msg.DeviceID)
	err := client.Send(&protocol.Message{
		Type:       protocol.MsgFileUploadStart,
		TaskID:     msg.TaskID,
		Path:       msg.Path,
		ParentPath: msg.ParentPath,
		Name:       msg.Name,
		Size:       msg.Size,
		ModTime:    msg.ModTime,
	})
	if err != nil {
		h.srv.removeFileTask(msg.TaskID)
		socket.writeText(fileMsg{Op: "error", DeviceID: msg.DeviceID, TaskID: msg.TaskID, Message: "failed to reach device"})
	}
}

func (h *filesWSHandler) handleDownloadStart(socket *fileSocket, msg fileMsg) {
	if msg.TaskID == "" {
		msg.TaskID = generateID()
	}
	client, ok := h.clientFor(socket, msg.DeviceID)
	if !ok {
		return
	}
	h.srv.registerFileTask(msg.TaskID, socket, msg.DeviceID)
	if err := client.Send(&protocol.Message{Type: protocol.MsgFileDownloadStart, TaskID: msg.TaskID, Path: msg.Path, Offset: msg.Offset}); err != nil {
		h.srv.removeFileTask(msg.TaskID)
		socket.writeText(fileMsg{Op: "error", DeviceID: msg.DeviceID, TaskID: msg.TaskID, Message: "failed to reach device"})
	}
}

func (h *filesWSHandler) forwardTaskControl(socket *fileSocket, msg fileMsg, typ protocol.MessageType) {
	route := h.srv.getFileTask(msg.TaskID)
	if route == nil || route.socket != socket {
		socket.writeText(fileMsg{Op: "error", TaskID: msg.TaskID, Message: "task not found"})
		return
	}
	client, ok := h.srv.GetClient(route.deviceID)
	if !ok {
		socket.writeText(fileMsg{Op: "error", TaskID: msg.TaskID, Message: "device not connected"})
		return
	}
	client.Send(&protocol.Message{Type: typ, TaskID: msg.TaskID, Path: msg.Path, Size: msg.Size, Offset: msg.Offset})
}

func (h *filesWSHandler) handleCancel(socket *fileSocket, msg fileMsg) {
	route := h.srv.getFileTask(msg.TaskID)
	if route == nil || route.socket != socket {
		return
	}
	if client, ok := h.srv.GetClient(route.deviceID); ok {
		client.Send(&protocol.Message{Type: protocol.MsgFileTransferCancel, TaskID: msg.TaskID})
		client.SendBinaryOffset(protocol.BinFileTransferCancel, msg.TaskID, msg.Offset, nil)
	}
	h.srv.removeFileTask(msg.TaskID)
	socket.writeText(fileMsg{Op: "canceled", TaskID: msg.TaskID})
}

func (h *filesWSHandler) handleBinary(socket *fileSocket, raw []byte) {
	typ, taskID, offset, payload, err := protocol.DecodeBinFrameOffset(raw)
	if err != nil {
		socket.writeText(fileMsg{Op: "error", Message: "invalid binary frame"})
		return
	}
	route := h.srv.getFileTask(taskID)
	if route == nil || route.socket != socket {
		socket.writeText(fileMsg{Op: "error", TaskID: taskID, Message: "task not found"})
		return
	}
	client, ok := h.srv.GetClient(route.deviceID)
	if !ok {
		socket.writeText(fileMsg{Op: "error", TaskID: taskID, Message: "device not connected"})
		return
	}
	switch typ {
	case protocol.BinFileUploadChunk:
		client.SendBinaryOffset(protocol.BinFileUploadChunk, taskID, offset, payload)
	case protocol.BinFileTransferCancel:
		client.SendBinaryOffset(protocol.BinFileTransferCancel, taskID, offset, nil)
		h.srv.removeFileTask(taskID)
	}
}

func (s *Server) registerFileTask(taskID string, socket *fileSocket, deviceID string) {
	s.fileMu.Lock()
	s.fileTasks[taskID] = &fileTaskRoute{socket: socket, deviceID: deviceID}
	s.fileMu.Unlock()
}

func (s *Server) getFileTask(taskID string) *fileTaskRoute {
	s.fileMu.RLock()
	defer s.fileMu.RUnlock()
	return s.fileTasks[taskID]
}

func (s *Server) removeFileTask(taskID string) {
	s.fileMu.Lock()
	delete(s.fileTasks, taskID)
	s.fileMu.Unlock()
}

func (s *Server) removeFileRequest(requestID string) {
	s.fileMu.Lock()
	delete(s.fileRequests, requestID)
	s.fileMu.Unlock()
}

func (s *Server) handleFileManagerMessage(msg *protocol.Message) {
	switch msg.Type {
	case protocol.MsgFileListResult:
		s.fileMu.Lock()
		socket := s.fileRequests[msg.RequestID]
		delete(s.fileRequests, msg.RequestID)
		s.fileMu.Unlock()
		if socket != nil {
			socket.writeText(fileMsg{
				Op:        "list_result",
				RequestID: msg.RequestID,
				Path:      msg.Path,
				Parent:    msg.ParentPath,
				Home:      msg.HomePath,
				Entries:   msg.FileEntries,
				Truncated: msg.Truncated,
				Error:     msg.Error,
				Message:   msg.Error,
			})
		}
	case protocol.MsgFileUploadReady:
		if route := s.getFileTask(msg.TaskID); route != nil {
			route.socket.writeText(fileMsg{Op: "upload_ready", TaskID: msg.TaskID, Path: msg.Path, Offset: msg.Offset, Size: msg.Size})
		}
	case protocol.MsgFileDownloadStart:
		if route := s.getFileTask(msg.TaskID); route != nil {
			route.socket.writeText(fileMsg{Op: "download_start", TaskID: msg.TaskID, Path: msg.Path, Name: msg.Name, Size: msg.Size, Offset: msg.Offset, ModTime: msg.ModTime})
		}
	case protocol.MsgFileTransferEnd:
		if route := s.getFileTask(msg.TaskID); route != nil {
			route.socket.writeText(fileMsg{Op: "transfer_end", TaskID: msg.TaskID, Path: msg.Path, Size: msg.Size, Offset: msg.Offset, Success: msg.Success})
			s.removeFileTask(msg.TaskID)
		}
	case protocol.MsgFileTransferError:
		if route := s.getFileTask(msg.TaskID); route != nil {
			route.socket.writeText(fileMsg{Op: "transfer_error", TaskID: msg.TaskID, Path: msg.Path, Error: msg.Error, Message: msg.Error})
			s.removeFileTask(msg.TaskID)
		}
	}
}

func (s *Server) handleFileManagerBinary(raw []byte) bool {
	typ, taskID, _, _, err := protocol.DecodeBinFrameOffset(raw)
	if err != nil {
		return false
	}
	switch typ {
	case protocol.BinFileUploadAck, protocol.BinFileDownloadChunk, protocol.BinFileTransferEnd, protocol.BinFileTransferCancel:
		route := s.getFileTask(taskID)
		if route == nil {
			return true
		}
		route.socket.writeBinary(raw)
		if typ == protocol.BinFileTransferEnd || typ == protocol.BinFileTransferCancel {
			s.removeFileTask(taskID)
		}
		return true
	default:
		return false
	}
}

// HandleFilesWS handles browser file manager WebSocket connections.
func (s *Server) HandleFilesWS(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuth(w, r) {
		return
	}
	upgrader := gws.NewUpgrader(&filesWSHandler{srv: s}, &gws.ServerOption{
		ReadMaxPayloadSize: 16 * 1024 * 1024,
		ParallelGolimit:    1,
		PermessageDeflate: gws.PermessageDeflate{
			Enabled:               true,
			ServerContextTakeover: true,
			ClientContextTakeover: true,
			Threshold:             256,
		},
	})
	socket, err := upgrader.Upgrade(w, r)
	if err != nil {
		log.Printf("files ws upgrade error: %v", err)
		return
	}
	socket.ReadLoop()
}

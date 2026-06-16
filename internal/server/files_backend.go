package server

import (
	"context"
	"io"
	"path"
	"time"

	"rdev/internal/protocol"
)

func (h *filesWSHandler) handleBackendList(socket *fileSocket, backend fileBackend, msg fileMsg) {
	if msg.RequestID == "" {
		msg.RequestID = generateID()
	}
	entries, parent, home, truncated, err := backend.List(context.Background(), msg.Path)
	if err != nil {
		socket.writeText(fileMsg{Op: "list_result", RequestID: msg.RequestID, Path: msg.Path, Error: err.Error(), Message: err.Error()})
		return
	}
	socket.writeText(fileMsg{Op: "list_result", RequestID: msg.RequestID, Path: msg.Path, Parent: parent, Home: home, Entries: entries, Truncated: truncated})
}

func (h *filesWSHandler) handleRelayUploadStart(socket *fileSocket, backend fileBackend, msg fileMsg) {
	if msg.TaskID == "" {
		msg.TaskID = generateID()
	}
	client, ok := h.clientFor(socket, msg.DeviceID)
	if !ok {
		return
	}
	name := msg.Name
	if name == "" {
		name = path.Base(msg.Path)
	}
	if name == "." || name == "/" || name == "" {
		name = "upload.bin"
	}
	target := msg.Path
	if target == "" {
		target = path.Join(msg.ParentPath, name)
	}
	uploader, ok := backend.(transferUploadBackend)
	if !ok {
		socket.writeText(fileMsg{Op: "transfer_error", DeviceID: msg.DeviceID, TaskID: msg.TaskID, Path: target, Error: "backend does not support relay upload", Message: "backend does not support relay upload"})
		return
	}
	upload, err := uploader.OpenTransferUpload(context.Background(), msg.TaskID, name, msg.Size, msg.ModTime)
	if err != nil {
		socket.writeText(fileMsg{Op: "transfer_error", DeviceID: msg.DeviceID, TaskID: msg.TaskID, Path: target, Error: err.Error(), Message: err.Error()})
		return
	}
	h.srv.registerRelayFileTask(msg.TaskID, socket, client.ID, backend, upload, target)
	socket.writeText(fileMsg{Op: "upload_ready", TaskID: msg.TaskID, Path: target, Offset: upload.Offset(), Size: msg.Size})
}

func (h *filesWSHandler) handleBackendUploadStart(socket *fileSocket, backend fileBackend, msg fileMsg) {
	if msg.TaskID == "" {
		msg.TaskID = generateID()
	}
	upload, err := backend.OpenUpload(context.Background(), msg.Path, msg.ParentPath, msg.Name, msg.Size, msg.ModTime)
	if err != nil {
		socket.writeText(fileMsg{Op: "transfer_error", DeviceID: msg.DeviceID, TaskID: msg.TaskID, Path: msg.Path, Error: err.Error(), Message: err.Error()})
		return
	}
	h.srv.registerBackendFileTask(msg.TaskID, socket, backend, upload, nil)
	socket.writeText(fileMsg{Op: "upload_ready", TaskID: msg.TaskID, Path: upload.Path(), Offset: upload.Offset(), Size: msg.Size})
}

func (h *filesWSHandler) handleBackendUploadEnd(socket *fileSocket, route *fileTaskRoute, msg fileMsg) {
	if route.upload == nil {
		socket.writeText(fileMsg{Op: "transfer_error", TaskID: msg.TaskID, Error: "upload not found", Message: "upload not found"})
		h.srv.removeFileTask(msg.TaskID)
		return
	}
	size, err := route.upload.Commit(context.Background())
	if err != nil {
		socket.writeText(fileMsg{Op: "transfer_error", TaskID: msg.TaskID, Path: route.upload.Path(), Error: err.Error(), Message: err.Error()})
		h.srv.removeFileTask(msg.TaskID)
		return
	}
	if route.targetPath != "" {
		_, url, err := route.backend.DownloadURL(context.Background(), route.upload.Path())
		if err != nil {
			socket.writeText(fileMsg{Op: "transfer_error", TaskID: msg.TaskID, Path: route.targetPath, Error: err.Error(), Message: err.Error()})
			h.srv.removeFileTask(msg.TaskID)
			return
		}
		client, ok := h.srv.GetClient(route.deviceID)
		if !ok {
			socket.writeText(fileMsg{Op: "transfer_error", TaskID: msg.TaskID, Path: route.targetPath, Error: "device not connected", Message: "device not connected"})
			h.srv.removeFileTask(msg.TaskID)
			return
		}
		h.srv.promoteRelayFileTask(msg.TaskID)
		if err := client.Send(&protocol.Message{Type: protocol.MsgFileUploadStart, TaskID: msg.TaskID, Path: route.targetPath, Name: path.Base(route.targetPath), Size: size, DownloadURL: url}); err != nil {
			socket.writeText(fileMsg{Op: "transfer_error", TaskID: msg.TaskID, Path: route.targetPath, Error: "failed to reach device", Message: "failed to reach device"})
			h.srv.removeFileTask(msg.TaskID)
			return
		}
		return
	}
	socket.writeText(fileMsg{Op: "transfer_end", TaskID: msg.TaskID, Path: route.upload.Path(), Size: size, Offset: size, Success: true})
	h.srv.removeFileTask(msg.TaskID)
}

func (h *filesWSHandler) handleBackendDownloadStart(socket *fileSocket, backend fileBackend, msg fileMsg) {
	if msg.TaskID == "" {
		msg.TaskID = generateID()
	}
	info, url, err := backend.DownloadURL(context.Background(), msg.Path)
	if err != nil {
		socket.writeText(fileMsg{Op: "transfer_error", TaskID: msg.TaskID, Path: msg.Path, Error: err.Error(), Message: err.Error()})
		return
	}
	socket.writeText(fileMsg{Op: "download_start", TaskID: msg.TaskID, Path: info.Path, Name: info.Name, Size: info.Size, Offset: msg.Offset, ModTime: info.ModTime.Format(time.RFC3339), DownloadURL: url})
	socket.writeText(fileMsg{Op: "transfer_end", TaskID: msg.TaskID, Path: info.Path, Size: info.Size, Offset: info.Size, Success: true})
}

func (h *filesWSHandler) handleBackendBinary(socket *fileSocket, route *fileTaskRoute, typ byte, taskID string, offset int64, payload []byte) {
	switch typ {
	case protocol.BinFileUploadChunk:
		if route.upload == nil {
			socket.writeText(fileMsg{Op: "transfer_error", TaskID: taskID, Error: "upload not found", Message: "upload not found"})
			h.srv.removeFileTask(taskID)
			return
		}
		if offset != route.upload.Offset() {
			route.upload.Cancel()
			socket.writeText(fileMsg{Op: "transfer_error", TaskID: taskID, Path: route.upload.Path(), Error: "unexpected upload offset", Message: "unexpected upload offset"})
			h.srv.removeFileTask(taskID)
			return
		}
		if _, err := route.upload.WriteAt(payload, offset); err != nil {
			route.upload.Cancel()
			socket.writeText(fileMsg{Op: "transfer_error", TaskID: taskID, Path: route.upload.Path(), Error: err.Error(), Message: err.Error()})
			h.srv.removeFileTask(taskID)
			return
		}
		socket.writeBinary(protocol.EncodeBinFrameOffset(protocol.BinFileUploadAck, taskID, route.upload.Offset(), nil))
	case protocol.BinFileTransferCancel:
		if route.cancel != nil {
			route.cancel()
		}
		if route.upload != nil {
			route.upload.Cancel()
		}
		h.srv.removeFileTask(taskID)
		socket.writeBinary(protocol.EncodeBinFrameOffset(protocol.BinFileTransferCancel, taskID, offset, nil))
	}
}

type backendDownloadWriter struct {
	socket *fileSocket
	taskID string
	offset int64
}

func (w *backendDownloadWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	w.socket.writeBinary(protocol.EncodeBinFrameOffset(protocol.BinFileDownloadChunk, w.taskID, w.offset, p))
	w.offset += int64(len(p))
	return len(p), nil
}

var _ io.Writer = (*backendDownloadWriter)(nil)

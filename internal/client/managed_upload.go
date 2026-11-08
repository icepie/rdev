package client

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"rdev/internal/protocol"
)

type managedUpload struct {
	mu                          sync.Mutex // file handle, offset and terminal state
	id, path, pathKey, partPath string
	file                        *os.File
	size, offset                int64
	closed                      bool
}

func (c *Client) managedUploadFor(id string) *managedUpload {
	c.uploadMu.Lock()
	defer c.uploadMu.Unlock()
	return c.uploads[id]
}
func (c *Client) snapshotUploads() []*managedUpload {
	c.uploadMu.Lock()
	defer c.uploadMu.Unlock()
	out := make([]*managedUpload, 0, len(c.uploads))
	for _, up := range c.uploads {
		out = append(out, up)
	}
	return out
}

// Caller holds up.mu. Keep destination ownership through close/rename, and
// remove only this generation. No global client/map lock spans file I/O.
func (c *Client) finishManagedUpload(up *managedUpload) {
	up.closed = true
	if up.file != nil {
		up.file.Close()
		up.file = nil
	}
	c.uploadMu.Lock()
	if c.uploads[up.id] == up {
		delete(c.uploads, up.id)
	}
	if c.uploadPaths[up.pathKey] == up {
		delete(c.uploadPaths, up.pathKey)
	}
	c.uploadMu.Unlock()
}
func (c *Client) cancelManagedUpload(up *managedUpload) {
	up.mu.Lock()
	defer up.mu.Unlock()
	c.finishManagedUpload(up)
}

func (c *Client) handleManagedUploadStart(msg *protocol.Message) {
	if msg.TaskID == "" {
		return
	}
	c.mu.Lock()
	transport := c.transport
	c.mu.Unlock()
	if transport == nil {
		return
	}
	target := msg.Path
	if target == "" {
		target = safeJoinFile(msg.ParentPath, msg.Name)
	}
	fail := func(err error) { c.sendFileTransferError(msg.TaskID, target, err.Error()) }
	if target == "" {
		fail(fmt.Errorf("missing target path"))
		return
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		fail(err)
		return
	}
	if err = os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
		fail(err)
		return
	}
	// Resolve directory aliases so two paths through a symlink cannot share the
	// same .rdevpart while appearing to be distinct destinations.
	dir, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		fail(err)
		return
	}
	target = filepath.Join(dir, filepath.Base(abs))
	key := target
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	up := &managedUpload{id: msg.TaskID, path: target, pathKey: key, partPath: target + ".rdevpart", size: msg.Size}
	up.mu.Lock()
	defer up.mu.Unlock()
	// Disconnect snapshots uploads while holding c.mu. Reserve before it, or
	// reject a delayed start after its originating transport has disappeared.
	c.mu.Lock()
	if c.transport != transport {
		c.mu.Unlock()
		return
	}
	c.uploadMu.Lock()
	if c.uploads[msg.TaskID] != nil || c.uploadPaths[key] != nil {
		c.uploadMu.Unlock()
		c.mu.Unlock()
		fail(fmt.Errorf("upload already active for this task or destination"))
		return
	}
	c.uploads[msg.TaskID] = up
	c.uploadPaths[key] = up
	c.uploadMu.Unlock()
	c.mu.Unlock()
	ready := false
	defer func() {
		if !ready {
			c.finishManagedUpload(up)
		}
	}()
	up.file, err = os.OpenFile(up.partPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		fail(err)
		return
	}
	st, err := up.file.Stat()
	if err != nil {
		fail(err)
		return
	}
	up.offset = st.Size()
	if msg.Size >= 0 && up.offset > msg.Size {
		if err = up.file.Truncate(0); err != nil {
			fail(err)
			return
		}
		up.offset = 0
	}
	if _, err = up.file.Seek(up.offset, io.SeekStart); err != nil {
		fail(err)
		return
	}
	if err = c.send(&protocol.Message{Type: protocol.MsgFileUploadReady, TaskID: up.id, Path: up.path, Offset: up.offset, Size: up.size}); err != nil {
		return
	}
	ready = true
}

func (c *Client) handleManagedUploadChunk(id string, offset int64, data []byte) {
	up := c.managedUploadFor(id)
	if up == nil {
		c.sendFileTransferError(id, "", "upload not found")
		return
	}
	up.mu.Lock()
	if up.closed {
		up.mu.Unlock()
		return
	}
	var err error
	if offset != up.offset {
		err = fmt.Errorf("unexpected offset %d, want %d", offset, up.offset)
	} else {
		var n int
		n, err = up.file.Write(data)
		up.offset += int64(n)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
	}
	next := up.offset
	if err != nil {
		c.finishManagedUpload(up)
	}
	up.mu.Unlock()
	if err != nil {
		c.sendFileTransferError(id, up.path, err.Error())
		return
	}
	c.sendBinaryOffset(protocol.BinFileUploadAck, id, next, nil)
}

func (c *Client) handleManagedUploadEnd(msg *protocol.Message) {
	up := c.managedUploadFor(msg.TaskID)
	if up == nil {
		c.sendFileTransferError(msg.TaskID, msg.Path, "upload not found")
		return
	}
	up.mu.Lock()
	if up.closed {
		up.mu.Unlock()
		return
	}
	err := up.file.Sync()
	if closeErr := up.file.Close(); err == nil {
		err = closeErr
	}
	up.file = nil
	if err == nil && up.size >= 0 && up.offset != up.size {
		err = fmt.Errorf("size mismatch: wrote %d of %d", up.offset, up.size)
	}
	if err == nil {
		err = os.Rename(up.partPath, up.path)
	}
	size := up.offset
	c.finishManagedUpload(up)
	up.mu.Unlock()
	if err != nil {
		c.sendFileTransferError(msg.TaskID, up.path, err.Error())
		return
	}
	c.send(&protocol.Message{Type: protocol.MsgFileTransferEnd, TaskID: msg.TaskID, Path: up.path, Size: size, Success: true})
}

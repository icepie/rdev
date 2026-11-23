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
	sha256                      string
	file                        *os.File
	size, offset                int64
	closed                      bool
	cancel                      chan struct{}
	cancelOnce                  sync.Once
	rtcCancel                   <-chan struct{}
	rtc                         *fileRTCSession
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
	up.cancelOnce.Do(func() {
		if up.cancel != nil {
			close(up.cancel)
		}
	})
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
	if msg.Size < 0 || !validFileSHA256(msg.SHA256) {
		fail(fmt.Errorf("invalid upload size or SHA-256"))
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
	up := &managedUpload{id: msg.TaskID, path: target, pathKey: key, partPath: target + ".rdevpart", size: msg.Size, sha256: msg.SHA256, rtc: c.fileRTCFor(msg.TaskID), cancel: make(chan struct{})}
	if up.rtc != nil {
		up.rtcCancel = up.rtc.done
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	// Disconnect snapshots uploads while holding c.mu. Reserve before it, or
	// reject a delayed start after its originating transport has disappeared.
	c.mu.Lock()
	if c.transport != transport || (msg.RTCType == "offer" && (up.rtc == nil || up.rtc.closed.Load() || up.rtc.transport != transport)) {
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
	if err = bindUploadPartial(up, msg.Restart); err != nil {
		fail(err)
		return
	}
	prefix, err := filePrefixSHA256Cancelable(up.file, up.offset, up.cancel, up.rtcCancel)
	if err != nil {
		fail(err)
		return
	}
	if _, err = up.file.Seek(up.offset, io.SeekStart); err != nil {
		fail(err)
		return
	}
	if err = c.send(&protocol.Message{Type: protocol.MsgFileUploadReady, TaskID: up.id, Path: up.path, Offset: up.offset, Size: up.size, SHA256: up.sha256, PrefixSHA256: prefix}); err != nil {
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
	} else if int64(len(data)) > up.size-up.offset {
		err = fmt.Errorf("upload chunk exceeds declared size")
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
	if err := c.sendFileBinary(up.rtc, protocol.BinFileUploadAck, id, next, nil); err != nil && up.rtc != nil {
		up.rtc.close(true)
	}
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
	before, err := up.file.Stat()
	if err == nil && (up.offset != up.size || before.Size() != up.size) {
		err = fmt.Errorf("size mismatch: wrote %d of %d", up.offset, up.size)
	}
	if err == nil {
		var actual string
		actual, err = filePrefixSHA256Cancelable(up.file, up.size, up.cancel, up.rtcCancel)
		if err == nil && actual != up.sha256 {
			err = fmt.Errorf("upload SHA-256 mismatch; destination unchanged")
		}
	}
	if err == nil {
		var after os.FileInfo
		after, err = up.file.Stat()
		if err == nil && !sameDownloadVersion(before, after) {
			err = fmt.Errorf("upload partial changed during verification")
		}
	}
	if err == nil {
		err = up.file.Sync()
	}
	if closeErr := up.file.Close(); err == nil {
		err = closeErr
	}
	up.file = nil
	if err == nil {
		select {
		case <-up.cancel:
			err = fmt.Errorf("upload canceled")
		case <-up.rtcCancel:
			err = fmt.Errorf("upload canceled")
		default:
		}
	}
	if err == nil {
		err = os.Rename(up.partPath, up.path)
	}
	// Failed final verification must not make a corrupt prefix sticky on retry.
	_ = os.Remove(up.partPath + ".meta")
	c.fileListings.invalidate()
	size := up.offset
	c.finishManagedUpload(up)
	up.mu.Unlock()
	if err != nil {
		c.sendFileTransferError(msg.TaskID, up.path, err.Error())
		return
	}
	if up.rtc != nil {
		up.rtc.completed()
	}
	c.send(&protocol.Message{Type: protocol.MsgFileTransferEnd, TaskID: msg.TaskID, Path: up.path, Size: size, SHA256: up.sha256, Success: true})
}

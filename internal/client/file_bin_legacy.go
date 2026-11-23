package client

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"rdev/internal/protocol"
)

type fileStream struct {
	path string
	file *os.File
	mode os.FileMode
}

func (c *Client) sendFileResult(id, path string, success bool, errText string) {
	c.send(&protocol.Message{Type: protocol.MsgFileResult, SessionID: id, FilePath: path, Success: success, Error: errText})
}

func (c *Client) handleBinFileStart(id string, payload []byte) {
	path, mode, _, err := protocol.DecodeBinFilePut(payload)
	if err != nil {
		c.sendFileResult(id, "", false, fmt.Sprintf("decode error: %v", err))
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		c.sendFileResult(id, path, false, fmt.Sprintf("mkdir error: %v", err))
		return
	}
	fm := os.FileMode(0644)
	if mode > 0 {
		fm = os.FileMode(mode)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fm)
	if err != nil {
		c.sendFileResult(id, path, false, err.Error())
		return
	}
	c.mu.Lock()
	if old := c.fileStreams[id]; old != nil {
		old.file.Close()
	}
	c.fileStreams[id] = &fileStream{path: path, file: f, mode: fm}
	c.mu.Unlock()
}

func (c *Client) handleBinFileChunk(id string, data []byte) {
	c.mu.Lock()
	fs := c.fileStreams[id]
	c.mu.Unlock()
	if fs == nil {
		c.sendFileResult(id, "", false, "file stream not found")
		return
	}
	if _, err := fs.file.Write(data); err != nil {
		fs.file.Close()
		c.mu.Lock()
		delete(c.fileStreams, id)
		c.mu.Unlock()
		c.sendFileResult(id, fs.path, false, err.Error())
	}
}

func (c *Client) handleBinFileEnd(id string) {
	c.mu.Lock()
	fs := c.fileStreams[id]
	delete(c.fileStreams, id)
	c.mu.Unlock()
	if fs == nil {
		c.sendFileResult(id, "", false, "file stream not found")
		return
	}
	if err := fs.file.Close(); err != nil {
		c.sendFileResult(id, fs.path, false, err.Error())
		return
	}
	c.sendFileResult(id, fs.path, true, "")
}

func (c *Client) handleBinFilePut(id string, payload []byte) {
	path, mode, fileData, err := protocol.DecodeBinFilePut(payload)
	if err != nil {
		c.sendFileResult(id, "", false, fmt.Sprintf("decode error: %v", err))
		return
	}

	log.Printf("file_put: writing %s (%d bytes)", path, len(fileData))

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		c.sendFileResult(id, path, false, fmt.Sprintf("mkdir error: %v", err))
		return
	}

	fm := os.FileMode(0644)
	if mode > 0 {
		fm = os.FileMode(mode)
	}
	if err := os.WriteFile(path, fileData, fm); err != nil {
		c.sendFileResult(id, path, false, err.Error())
		return
	}

	log.Printf("file_put: wrote %d bytes to %s", len(fileData), path)
	c.sendFileResult(id, path, true, "")
}

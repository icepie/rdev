package client

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"rdev/internal/protocol"
)

// Only immutable fingerprints are cached: every range opens its own descriptor
// and checks file identity, size and modification time before and after I/O.
// Capacity and expiry also bound transfers abandoned without a cancel frame.
type downloadFingerprint struct {
	info os.FileInfo
	sha256 string
	used time.Time
}
type downloadFingerprintCache struct {
	mu sync.Mutex
	entries map[string]downloadFingerprint
}

func sameDownloadVersion(a, b os.FileInfo) bool {
	aSec, aNano := fileVersionChangeTime(a)
	bSec, bNano := fileVersionChangeTime(b)
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime()) && a.Mode() == b.Mode() && aSec == bSec && aNano == bNano
}

func (cache *downloadFingerprintCache) fingerprint(key string, file *os.File, info os.FileInfo, cancel <-chan struct{}) (string, error) {
	now := time.Now()
	cache.mu.Lock()
	for path, entry := range cache.entries {
		if now.Sub(entry.used) > 5*time.Minute { delete(cache.entries, path) }
	}
	if entry, ok := cache.entries[key]; ok && sameDownloadVersion(entry.info, info) {
		entry.used = now
		cache.entries[key] = entry
		cache.mu.Unlock()
		return entry.sha256, nil
	}
	cache.mu.Unlock()
	digest, err := filePrefixSHA256Cancelable(file, info.Size(), cancel, nil)
	if err != nil { return "", err }
	after, err := file.Stat()
	if err != nil { return "", err }
	if !sameDownloadVersion(info, after) { return "", fmt.Errorf("file changed while computing download SHA-256") }
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.entries == nil { cache.entries = make(map[string]downloadFingerprint) }
	if len(cache.entries) >= 128 {
		var oldest string
		var oldestTime time.Time
		for path, entry := range cache.entries {
			if oldest == "" || entry.used.Before(oldestTime) { oldest, oldestTime = path, entry.used }
		}
		delete(cache.entries, oldest)
	}
	cache.entries[key] = downloadFingerprint{info: after, sha256: digest, used: now}
	return digest, nil
}
func (c *Client) handleManagedDownloadStart(msg *protocol.Message) {
	taskID := msg.TaskID
	rtc := c.fileRTCFor(taskID)
	if msg.RTCType == "offer" && (rtc == nil || rtc.closed.Load()) {
		return
	}
	path := msg.Path
	if taskID == "" || path == "" {
		return
	}
	c.mu.Lock()
	transport := c.transport
	c.mu.Unlock()
	if transport == nil { return }
	var f *os.File
	var err error
	if msg.DownloadRoot != "" {
		f, err = os.OpenInRoot(msg.DownloadRoot, path)
	} else {
		f, err = os.Open(path)
	}
	if err != nil {
		c.sendFileTransferError(taskID, path, err.Error())
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		c.sendFileTransferError(taskID, path, err.Error())
		return
	}
	if !st.Mode().IsRegular() {
		c.sendFileTransferError(taskID, path, "cannot download non-regular file")
		return
	}
	offset := msg.Offset
	if msg.Length < 0 || (msg.Length > 0 && (offset < 0 || offset > st.Size())) {
		c.sendFileTransferError(taskID, path, "invalid download range")
		return
	}
	if offset < 0 || offset > st.Size() {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		c.sendFileTransferError(taskID, path, err.Error())
		return
	}
	c.mu.Lock()
	if c.transport != transport || (rtc != nil && (rtc.closed.Load() || c.transport != rtc.transport)) {
		c.mu.Unlock()
		return
	}
	cancel := make(chan struct{})
	if old := c.downloads[taskID]; old != nil {
		close(old)
	}
	c.downloads[taskID] = cancel
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.downloads[taskID] == cancel {
			delete(c.downloads, taskID)
		}
		c.mu.Unlock()
	}()
	digest, err := c.downloadFingerprints.fingerprint(msg.DownloadRoot + "\x00" + path, f, st, cancel)
	if err != nil {
		c.sendFileTransferError(taskID, path, err.Error())
		return
	}
	if msg.SHA256 != "" && msg.SHA256 != digest {
		c.sendFileTransferError(taskID, path, "download source SHA-256 changed; restart required")
		return
	}
	select { case <-cancel: return; default: }
	checkVersion := func() bool {
		current, err := f.Stat()
		if err != nil || !sameDownloadVersion(st, current) {
			c.sendFileTransferError(taskID, path, "file changed during download")
			return false
		}
		return true
	}
	rangeAck := rtc != nil && rtc.rangeAck && msg.Length > 0
	length := st.Size() - offset
	if msg.Length > 0 {
		length = min(length, msg.Length)
	}
	readLength := length
	if rangeAck {
		readLength = st.Size() - offset
	}
	if err := c.send(&protocol.Message{Type: protocol.MsgFileDownloadStart, TaskID: taskID, Path: path, Name: filepath.Base(path), DownloadRoot: msg.DownloadRoot, Size: st.Size(), SHA256: digest, Offset: offset, Length: length, ModTime: st.ModTime().Format(time.RFC3339)}); err != nil {
		return
	}
	var reader io.Reader = f
	if msg.Length > 0 && !rangeAck {
		reader = io.LimitReader(f, length)
	}
	chunkSize := int64(512 * 1024)
	if rtc != nil {
		chunkSize = fileRTCChunkSize
	}
	buf := make([]byte, max(1, min(chunkSize, readLength)))
	cur := offset
	segmentEnd := offset + length
	endOffset := offset + readLength
	for {
		select {
		case <-cancel:
			return
		default:
		}
		readBuf := buf
		if rangeAck && int64(len(readBuf)) > segmentEnd-cur {
			readBuf = readBuf[:segmentEnd-cur]
		}
		n, readErr := reader.Read(readBuf)
		if n > 0 {
			select {
			case <-cancel:
				return
			default:
			}
			if err := c.sendFileBinary(rtc, protocol.BinFileDownloadChunk, taskID, cur, readBuf[:n]); err != nil {
				return
			}
			cur += int64(n)
		}
		if rangeAck && cur == segmentEnd {
			if !checkVersion() { return }
			if cur < st.Size() {
				if err := rtc.expectRangeAck(cur); err != nil {
					return
				}
			}
			if err := c.sendFileBinary(rtc, protocol.BinFileTransferEnd, taskID, cur, nil); err != nil {
				return
			}
			if cur == st.Size() {
				break
			}
			if err := rtc.waitRangeAck(cur); err != nil {
				return
			}
			segmentEnd = cur + min(msg.Length, endOffset-cur)
			continue
		}
		if readErr == io.EOF {
			if cur-offset != readLength {
				c.sendFileTransferError(taskID, path, "file changed during range download")
				return
			}
			if !checkVersion() { return }
			if !rangeAck {
				if err := c.sendFileBinary(rtc, protocol.BinFileTransferEnd, taskID, cur, nil); err != nil {
					return
				}
			}
			break
		}
		if readErr != nil {
			c.sendFileTransferError(taskID, path, readErr.Error())
			return
		}
	}
	if rtc != nil {
		rtc.completed()
	}
	c.send(&protocol.Message{Type: protocol.MsgFileTransferEnd, TaskID: taskID, Path: path, Size: st.Size(), SHA256: digest, Offset: cur, Success: true})
}

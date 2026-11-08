package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"rdev/internal/protocol"
)

const mediaChunkSize = 512 * 1024

type mediaGrant struct {
	device, path, password string
	expires                time.Time
}

func videoMIME(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".ogv":
		return "video/ogg"
	case ".mov":
		return "video/quicktime"
	}
	return ""
}

func isHTMLPreviewPath(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".html" || ext == ".htm"
}

// Header sandbox applies to top-level documents as well as iframes. Never
// rely on an HTML-authored meta policy to isolate a new browser tab.
const htmlPreviewPolicy = "sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; media-src data: blob:; font-src data:; connect-src 'none'; form-action 'none'; base-uri 'none'"

// Called only after the owning Agent/Files socket has authorized this device.
// The bearer grants one file, expires, and is invalidated by password changes.
func (s *Server) grantFilePreview(client *ClientConn, path string) (string, error) {
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return "", err
	}
	now := time.Now()
	s.mediaMu.Lock()
	defer s.mediaMu.Unlock()
	if s.mediaGrants == nil {
		s.mediaGrants = make(map[string]mediaGrant)
	}
	for key, g := range s.mediaGrants {
		if g.device == client.ID && g.path == path && g.password == passwordFingerprint(client.Password) && g.expires.Sub(now) > 5*time.Minute {
			return "/api/media/" + key, nil
		}
		if !now.Before(g.expires) {
			delete(s.mediaGrants, key)
		}
	}
	if len(s.mediaGrants) >= 1024 {
		return "", errors.New("too many active media previews")
	}
	key := hex.EncodeToString(token)
	s.mediaGrants[key] = mediaGrant{client.ID, path, passwordFingerprint(client.Password), now.Add(time.Hour)}
	return "/api/media/" + key, nil
}

// ServeContent implements Range/HEAD/If-Range without buffering entire media.
// Each device read is bounded; a slow HTTP reader cannot block device routing.
func (s *Server) HandleMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", 405)
		return
	}
	if !requireOrigin(w, r) {
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/api/media/")
	s.mediaMu.Lock()
	grant, ok := s.mediaGrants[key]
	if s.mediaSlots == nil {
		s.mediaSlots = make(chan struct{}, 16)
	}
	slots := s.mediaSlots
	s.mediaMu.Unlock()
	if !ok || !time.Now().Before(grant.expires) {
		http.NotFound(w, r)
		return
	}
	client, ok := s.GetClient(grant.device)
	if !ok {
		http.Error(w, "device offline", 503)
		return
	}
	if passwordFingerprint(client.Password) != grant.password {
		http.NotFound(w, r)
		return
	}
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	default:
		http.Error(w, "too many media requests", 503)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if isHTMLPreviewPath(grant.path) {
		reader := &readFileTool{srv: s, client: client}
		content, err := reader.download(ctx, grant.path, agentFileReadCap)
		if err != nil {
			http.Error(w, "cannot read HTML preview: "+err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Security-Policy", htmlPreviewPolicy)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		if r.Method != http.MethodHead {
			_, _ = w.Write(content)
		}
		return
	}
	_, meta, err := s.readMediaRange(ctx, client, grant.path, 0, 1)
	if err != nil {
		http.Error(w, "cannot read media: "+err.Error(), 502)
		return
	}
	modTime, _ := time.Parse(time.RFC3339, meta.ModTime)
	reader := &mediaReader{srv: s, client: client, ctx: ctx, path: grant.path, size: meta.Size, modTime: meta.ModTime}
	w.Header().Set("Content-Type", videoMIME(grant.path))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.ServeContent(w, r, filepath.Base(grant.path), modTime, reader)
}

type mediaReader struct {
	srv          *Server
	client       *ClientConn
	ctx          context.Context
	path         string
	size, offset int64
	modTime      string
	pending      []byte
}

func (r *mediaReader) Seek(offset int64, whence int) (int64, error) {
	var base int64
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		base = r.offset
	case io.SeekEnd:
		base = r.size
	default:
		return 0, errors.New("invalid seek")
	}
	next := base + offset
	if next < 0 || (offset > 0 && next < base) {
		return 0, errors.New("invalid offset")
	}
	r.offset = next
	r.pending = nil
	return next, nil
}
func (r *mediaReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.offset >= r.size {
		return 0, io.EOF
	}
	if len(r.pending) == 0 {
		data, meta, err := r.srv.readMediaRange(r.ctx, r.client, r.path, r.offset, min(mediaChunkSize, r.size-r.offset))
		if err != nil {
			return 0, err
		}
		if meta.Size != r.size || meta.ModTime != r.modTime {
			return 0, errors.New("media changed during playback")
		}
		r.pending = data
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	r.offset += int64(n)
	if n == 0 {
		return 0, io.ErrUnexpectedEOF
	}
	return n, nil
}

func (s *Server) readMediaRange(ctx context.Context, client *ClientConn, path string, offset, length int64) ([]byte, *protocol.Message, error) {
	if offset < 0 || length <= 0 || length > mediaChunkSize {
		return nil, nil, errors.New("invalid media range")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	id := generateID()
	task := s.registerBufferedFileTask(id, 2)
	defer s.removeAgentFileTask(id)
	defer func() { _ = client.Send(&protocol.Message{Type: protocol.MsgFileTransferCancel, TaskID: id}) }()
	if err := client.Send(&protocol.Message{Type: protocol.MsgFileDownloadStart, TaskID: id, Path: path, Offset: offset, Length: length}); err != nil {
		return nil, nil, err
	}
	var meta *protocol.Message
	var chunks []agentChunk
	var received int64
	ended := false
	for {
		// Separate event/chunk queues may deliver completion before its chunks.
		if meta != nil && ended && received == meta.Length {
			var data bytes.Buffer
			cursor := offset
			for _, chunk := range chunks {
				if chunk.offset != cursor {
					return nil, nil, errors.New("non-contiguous media range")
				}
				data.Write(chunk.payload)
				cursor += int64(len(chunk.payload))
			}
			return data.Bytes(), meta, nil
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case chunk := <-task.chunks:
			received += int64(len(chunk.payload))
			if received > length {
				return nil, nil, errors.New("device exceeded media range")
			}
			chunks = append(chunks, chunk)
		case msg := <-task.events:
			switch msg.Type {
			case protocol.MsgFileDownloadStart:
				if meta != nil || msg.Size < 0 || msg.Offset != offset || offset > msg.Size || msg.Length != min(length, msg.Size-offset) {
					return nil, nil, errors.New("device does not support bounded media reads; update client")
				}
				meta = msg
			case protocol.MsgFileTransferError:
				return nil, nil, fmt.Errorf("device read failed: %s", msg.Error)
			case protocol.MsgFileTransferEnd:
				if !msg.Success || meta == nil || msg.Offset != offset+meta.Length {
					return nil, nil, errors.New("incomplete media range")
				}
				ended = true
			}
		}
	}
}

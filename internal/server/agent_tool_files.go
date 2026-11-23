package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"rdev/internal/protocol"
)

// --- list_dir ---

type listDirTool struct {
	srv    *Server
	client *ClientConn
}

func (t *listDirTool) Name() string { return "list_dir" }

func (t *listDirTool) Description() string {
	return "List entries of a directory on the remote device: mode (d/-), size, mod time and name per entry."
}

func (t *listDirTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path":  {"type": "string", "description": "Directory path on the device (empty for home)"},
			"limit": {"type": "integer", "description": "Max entries to return (default 200)"}
		},
		"required": ["path"]
	}`)
}

// Dangerous marks list_dir as read-only (no approval needed).
func (t *listDirTool) Dangerous() bool { return false }

func (t *listDirTool) Summary(raw json.RawMessage) string {
	return toolPathArg(raw)
}

func (t *listDirTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Path  string `json:"path"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %v", err)
	}
	if args.Limit <= 0 {
		args.Limit = 200
	}
	if args.Limit > 1000 {
		args.Limit = 1000
	}

	requestID := generateID()
	resultCh := t.srv.registerAgentFileReq(requestID)
	defer t.srv.removeAgentFileReq(requestID)

	if err := t.client.Send(&protocol.Message{
		Type:      protocol.MsgFileListRequest,
		RequestID: requestID,
		Path:      args.Path,
		Size:      int64(args.Limit),
	}); err != nil {
		return "", fmt.Errorf("failed to reach device: %v", err)
	}

	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case msg := <-resultCh:
		if msg.Error != "" {
			return "", fmt.Errorf("list %q: %s", args.Path, msg.Error)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "path: %s\nparent: %s\nhome: %s\n", msg.Path, msg.ParentPath, msg.HomePath)
		for _, e := range msg.FileEntries {
			mode := "-"
			if e.IsDir {
				mode = "d"
			}
			fmt.Fprintf(&b, "%s %10d %s %s\n", mode, e.Size, e.ModTime, e.Name)
		}
		if msg.Truncated {
			fmt.Fprintf(&b, "[truncated at limit %d; refine path or raise limit]\n", args.Limit)
		}
		return b.String(), nil
	case <-ctx.Done():
		return "", ctx.Err()
	case <-timer.C:
		return "", fmt.Errorf("list %q: timed out waiting for device", args.Path)
	}
}

// --- read_file ---

const (
	readFileDefaultMax = 256 * 1024
	readFileHardCap    = 1024 * 1024
)

type readFileTool struct {
	srv    *Server
	client *ClientConn
	cwd    string // thread working directory for resolving relative paths
}

// resolve maps a possibly relative path onto the thread working directory.
func (t *readFileTool) resolve(path string) string {
	return resolveDevicePath(clientOS(t.client), t.cwd, path)
}

func (t *readFileTool) Name() string { return "read_file" }

func (t *readFileTool) Description() string {
	return "Read a file from the remote device. Long files are truncated by lines (default 2000); use offset/limit to page through. Set anchors=true to prefix every line with an N#hash anchor for the edit tool's line_edits mode. Files larger than max_bytes are refused; recognized raster images return a bounded JPEG preview; other binary content is returned base64-encoded."
}

func (t *readFileTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path":      {"type": "string", "description": "File path on the device"},
			"max_bytes": {"type": "integer", "description": "Max file size to read (default 262144, cap 1048576)"},
			"offset":    {"type": "integer", "description": "First line to return, 1-based (default 1)"},
			"limit":     {"type": "integer", "description": "Max lines to return (default 2000)"},
			"anchors":   {"type": "boolean", "description": "Prefix every line with an N#hash anchor for edit line_edits"}
		},
		"required": ["path"]
	}`)
}

// Dangerous marks read_file as read-only (no approval needed).
func (t *readFileTool) Dangerous() bool { return false }

func (t *readFileTool) Summary(raw json.RawMessage) string {
	return toolPathArg(raw)
}

// readFileDefaultLines caps how many lines one read returns without an
// explicit limit, so a 10k-line file doesn't dump wholesale into context.
const readFileDefaultLines = 2000

func (t *readFileTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Path     string `json:"path"`
		MaxBytes int64  `json:"max_bytes"`
		Offset   int    `json:"offset"`
		Limit    int    `json:"limit"`
		Anchors  bool   `json:"anchors"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %v", err)
	}
	if args.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	args.Path = t.resolve(args.Path)
	maxBytes := args.MaxBytes
	if maxBytes <= 0 {
		maxBytes = readFileDefaultMax
	}
	if maxBytes > readFileHardCap {
		maxBytes = readFileHardCap
	}
	if args.Offset < 0 {
		return "", fmt.Errorf("offset must be >= 1")
	}
	if args.Limit < 0 {
		return "", fmt.Errorf("limit must be >= 0")
	}

	data, err := t.download(ctx, args.Path, maxBytes)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) || bytes.IndexByte(sampleBytes(data), 0) >= 0 {
		if preview := boundedAgentImagePreview(data, 8*1024); preview != "" {
			return "[image preview]\n" + preview, nil
		}
		return fmt.Sprintf("[binary file, %d bytes, base64-encoded]\n%s", len(data), base64.StdEncoding.EncodeToString(data)), nil
	}
	return formatReadLines(string(data), args.Offset, args.Limit, args.Anchors), nil
}

// formatReadLines applies offset/limit paging and optional hashline anchors
// to downloaded text. A truncated read reports the kept window and the
// remaining line count so the model can page further.
func formatReadLines(content string, offset, limit int, anchors bool) string {
	lines := splitLines(content)
	total := len(lines)
	if offset <= 0 {
		offset = 1
	}
	if offset > total {
		return fmt.Sprintf("[file has %d lines; offset %d is past the end]", total, offset)
	}
	if limit <= 0 {
		limit = readFileDefaultLines
	}
	end := offset + limit - 1
	if end > total {
		end = total
	}
	window := lines[offset-1 : end]

	var b strings.Builder
	if anchors {
		for i, line := range window {
			fmt.Fprintf(&b, "%s|%s\n", formatAnchor(offset+i, line), line)
		}
	} else {
		for _, line := range window {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	if offset > 1 || end < total {
		fmt.Fprintf(&b, "[lines %d-%d of %d", offset, end, total)
		if end < total {
			fmt.Fprintf(&b, "; %d more — continue with offset=%d", total-end, end+1)
		}
		b.WriteString("]\n")
	}
	return b.String()
}

// download fetches a whole device file via the file-transfer protocol,
// enforcing maxBytes both up front (metadata) and during transfer.
func (t *readFileTool) download(ctx context.Context, path string, maxBytes int64) ([]byte, error) {
	taskID := generateID()
	task := t.srv.registerAgentFileTask(taskID)
	defer t.srv.removeAgentFileTask(taskID)

	cancelTransfer := func() {
		_ = t.client.Send(&protocol.Message{Type: protocol.MsgFileTransferCancel, TaskID: taskID})
		_ = t.client.SendBinaryOffset(protocol.BinFileTransferCancel, taskID, 0, nil)
	}

	if err := t.client.Send(&protocol.Message{Type: protocol.MsgFileDownloadStart, TaskID: taskID, Path: path}); err != nil {
		return nil, fmt.Errorf("failed to reach device: %v", err)
	}

	timer := time.NewTimer(60 * time.Second)
	defer timer.Stop()

	// Wait for the download_start metadata echo (or an error).
	var size int64
	var digest string
	for {
		select {
		case msg := <-task.events:
			switch msg.Type {
			case protocol.MsgFileDownloadStart:
				size = msg.Size
				digest = msg.SHA256
				if size < 0 || len(digest) != sha256.Size*2 {
					cancelTransfer()
					return nil, fmt.Errorf("read %q: invalid download integrity metadata", path)
				}
				if size > maxBytes {
					cancelTransfer()
					return nil, fmt.Errorf("file %q is %d bytes, exceeds max_bytes %d; use shell (e.g. head/tail/dd) to read parts of it", path, size, maxBytes)
				}
			case protocol.MsgFileTransferError:
				return nil, fmt.Errorf("read %q: %s", path, msg.Error)
			default:
				continue
			}
		case <-ctx.Done():
			cancelTransfer()
			return nil, ctx.Err()
		case <-timer.C:
			cancelTransfer()
			return nil, fmt.Errorf("read %q: timed out waiting for device", path)
		}
		break
	}

	// Collect chunks until the text transfer_end arrives.
	parts := make(map[int64][]byte)
	var received int64
	acceptChunk := func(c agentChunk) error {
		if len(c.payload) == 0 || c.offset < 0 || int64(len(c.payload)) > size-c.offset || c.offset > size || received > maxBytes-int64(len(c.payload)) {
			return fmt.Errorf("read %q: chunk exceeds download bounds", path)
		}
		if _, exists := parts[c.offset]; exists {
			return fmt.Errorf("read %q: duplicate download offset", path)
		}
		parts[c.offset] = c.payload
		received += int64(len(c.payload))
		return nil
	}
	for {
		select {
		case c := <-task.chunks:
			if err := acceptChunk(c); err != nil {
				cancelTransfer()
				return nil, err
			}
		case msg := <-task.events:
			switch msg.Type {
			case protocol.MsgFileTransferEnd:
				if !msg.Success || msg.SHA256 != digest || msg.Size != size {
					return nil, fmt.Errorf("read %q: transfer completion integrity failed", path)
				}
				// Chunks and the end event arrive on separate channels fed by
				// the same read loop, so every chunk was enqueued before the
				// end — but select may deliver the end first. Drain any
				// pending chunks before assembling.
				for drained := false; !drained; {
					select {
					case c := <-task.chunks:
						if err := acceptChunk(c); err != nil {
							cancelTransfer()
							return nil, err
						}
					default:
						drained = true
					}
				}
				offsets := make([]int64, 0, len(parts))
				for off := range parts {
					offsets = append(offsets, off)
				}
				sort.Slice(offsets, func(i, j int) bool { return offsets[i] < offsets[j] })
				var data []byte
				for _, off := range offsets {
					if int64(len(data)) != off {
						return nil, fmt.Errorf("read %q: missing chunk at offset %d", path, off)
					}
					data = append(data, parts[off]...)
				}
				if int64(len(data)) != size {
					return nil, fmt.Errorf("read %q: got %d bytes, expected %d", path, len(data), size)
				}
				if fmt.Sprintf("%x", sha256.Sum256(data)) != digest {
					return nil, fmt.Errorf("read %q: download SHA-256 mismatch", path)
				}
				return data, nil
			case protocol.MsgFileTransferError:
				return nil, fmt.Errorf("read %q: %s", path, msg.Error)
			}
		case <-ctx.Done():
			cancelTransfer()
			return nil, ctx.Err()
		case <-timer.C:
			cancelTransfer()
			return nil, fmt.Errorf("read %q: timed out during transfer", path)
		}
	}
}

func sampleBytes(data []byte) []byte {
	if len(data) > 8192 {
		return data[:8192]
	}
	return data
}

// --- write_file ---

const writeFileChunkSize = 256 * 1024

type writeFileTool struct {
	srv    *Server
	client *ClientConn
	cwd    string // thread working directory for resolving relative paths
}

// resolve maps a possibly relative path onto the thread working directory.
func (t *writeFileTool) resolve(path string) string {
	return resolveDevicePath(clientOS(t.client), t.cwd, path)
}

func (t *writeFileTool) Name() string { return "write_file" }

func (t *writeFileTool) Description() string {
	return "Write content to a file on the remote device (creates or truncates it). Content is sent as-is; keep it under a few hundred KB — use shell for larger transfers."
}

func (t *writeFileTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path":    {"type": "string", "description": "File path on the device"},
			"content": {"type": "string", "description": "Full file content to write"},
			"mode":    {"description": "Optional permission mode, octal string (e.g. \"0644\") or number"}
		},
		"required": ["path", "content"]
	}`)
}

// Dangerous marks write_file as requiring user approval.
func (t *writeFileTool) Dangerous() bool { return true }

func (t *writeFileTool) Summary(raw json.RawMessage) string {
	return toolPathArg(raw)
}

func (t *writeFileTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		Mode    any    `json:"mode"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %v", err)
	}
	if args.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	args.Path = t.resolve(args.Path)

	var mode int32
	switch v := args.Mode.(type) {
	case string:
		if v != "" {
			n, err := strconv.ParseUint(v, 8, 32)
			if err != nil {
				return "", fmt.Errorf("invalid mode %q: use octal like \"0644\"", v)
			}
			mode = int32(n)
		}
	case float64:
		mode = int32(v)
	}

	release, err := t.srv.agentFileMutations.Acquire(ctx, t.client.ID, clientOS(t.client), args.Path)
	if err != nil {
		return "", err
	}
	defer release()
	return t.upload(ctx, args.Path, []byte(args.Content), mode)
}

// upload streams content to a device file via the file-transfer protocol
// (mode 0 = device default / keep existing permissions).
func (t *writeFileTool) upload(ctx context.Context, path string, content []byte, mode int32) (string, error) {
	taskID := generateID()
	task := t.srv.registerAgentFileTask(taskID)
	defer t.srv.removeAgentFileTask(taskID)

	cancelTransfer := func() {
		_ = t.client.Send(&protocol.Message{Type: protocol.MsgFileTransferCancel, TaskID: taskID})
		_ = t.client.SendBinaryOffset(protocol.BinFileTransferCancel, taskID, 0, nil)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(content))

	if err := t.client.Send(&protocol.Message{
		Type:     protocol.MsgFileUploadStart,
		TaskID:   taskID,
		Path:     path,
		Size:     int64(len(content)),
		SHA256:   digest,
		FileMode: mode,
	}); err != nil {
		return "", fmt.Errorf("failed to reach device: %v", err)
	}

	timer := time.NewTimer(60 * time.Second)
	defer timer.Stop()

	// Wait for the device to accept the upload (resume offset, normally 0).
	var offset int64
	for {
		select {
		case msg := <-task.events:
			switch msg.Type {
			case protocol.MsgFileUploadReady:
				offset = msg.Offset
				if offset < 0 || offset > int64(len(content)) {
					cancelTransfer()
					return "", fmt.Errorf("write %q: device returned invalid resume offset %d", path, offset)
				}
				prefix := fmt.Sprintf("%x", sha256.Sum256(content[:offset]))
				if msg.SHA256 != digest || msg.PrefixSHA256 != prefix || msg.Size != int64(len(content)) {
					cancelTransfer()
					return "", fmt.Errorf("write %q: upload identity or prefix SHA-256 mismatch", path)
				}
			case protocol.MsgFileTransferError:
				return "", fmt.Errorf("write %q: %s", path, msg.Error)
			default:
				continue
			}
		case <-ctx.Done():
			cancelTransfer()
			return "", ctx.Err()
		case <-timer.C:
			cancelTransfer()
			return "", fmt.Errorf("write %q: timed out waiting for device", path)
		}
		break
	}

	// Stream chunks (upload acks are routed away and ignored).
	for off := offset; off < int64(len(content)); {
		end := off + writeFileChunkSize
		if end > int64(len(content)) {
			end = int64(len(content))
		}
		if err := t.client.SendBinaryOffset(protocol.BinFileUploadChunk, taskID, off, content[off:end]); err != nil {
			cancelTransfer()
			return "", fmt.Errorf("write %q: failed to send chunk at offset %d: %v", path, off, err)
		}
		off = end
		select {
		case <-ctx.Done():
			cancelTransfer()
			return "", ctx.Err()
		case <-timer.C:
			cancelTransfer()
			return "", fmt.Errorf("write %q: timed out during upload", path)
		default:
		}
	}

	// Finalize: the device only flushes/closes/renames the .rdevpart file on
	// MsgFileUploadEnd (see client.handleManagedUploadEnd); chunks alone deadlock it.
	if err := t.client.Send(&protocol.Message{
		Type:   protocol.MsgFileUploadEnd,
		TaskID: taskID,
		Path:   path,
		Size:   int64(len(content)),
	}); err != nil {
		cancelTransfer()
		return "", fmt.Errorf("write %q: failed to finalize upload: %v", path, err)
	}

	// Wait for completion.
	for {
		select {
		case msg := <-task.events:
			switch msg.Type {
			case protocol.MsgFileTransferEnd:
				if !msg.Success || msg.SHA256 != digest || msg.Size != int64(len(content)) {
					return "", fmt.Errorf("write %q: transfer completion integrity failed", path)
				}
				if offset > 0 {
					return fmt.Sprintf("wrote %s (%d bytes new, resumed at offset %d)", path, int64(len(content))-offset, offset), nil
				}
				return fmt.Sprintf("wrote %d bytes to %s", len(content), path), nil
			case protocol.MsgFileTransferError:
				return "", fmt.Errorf("write %q: %s", path, msg.Error)
			}
		case <-ctx.Done():
			cancelTransfer()
			return "", ctx.Err()
		case <-timer.C:
			cancelTransfer()
			return "", fmt.Errorf("write %q: timed out waiting for completion", path)
		}
	}
}

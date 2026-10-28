package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"rdev/internal/agent"
	"rdev/internal/protocol"
)

// --- Agent file-op routing glue ---
//
// Agent tools run file operations on a device through the same protocol the
// browser file manager uses, but results must come back to a Go channel, not
// a browser socket. Agent requests/tasks live in their own maps; the device
// message dispatch (handleFileManagerMessage / handleFileManagerBinary in
// files.go) consults them first via routeAgentFileMsg/routeAgentFileBinary.

type agentChunk struct {
	offset  int64
	payload []byte
}

type agentFileTask struct {
	events chan *protocol.Message // text control: ready/start/end/error
	chunks chan agentChunk        // binary download chunks
}

func (s *Server) registerAgentFileReq(requestID string) chan *protocol.Message {
	ch := make(chan *protocol.Message, 1)
	s.agentFileMu.Lock()
	s.agentFileReqs[requestID] = ch
	s.agentFileMu.Unlock()
	return ch
}

func (s *Server) removeAgentFileReq(requestID string) {
	s.agentFileMu.Lock()
	delete(s.agentFileReqs, requestID)
	s.agentFileMu.Unlock()
}

func (s *Server) registerAgentFileTask(taskID string) *agentFileTask {
	task := &agentFileTask{
		events: make(chan *protocol.Message, 8),
		chunks: make(chan agentChunk, 64),
	}
	s.agentFileMu.Lock()
	s.agentFileTasks[taskID] = task
	s.agentFileMu.Unlock()
	return task
}

func (s *Server) removeAgentFileTask(taskID string) {
	s.agentFileMu.Lock()
	delete(s.agentFileTasks, taskID)
	s.agentFileMu.Unlock()
}

// routeAgentFileMsg consumes device text replies destined for an agent tool.
// Returns true when the message was claimed (caller must not process further).
func (s *Server) routeAgentFileMsg(msg *protocol.Message) bool {
	switch msg.Type {
	case protocol.MsgFileListResult:
		if msg.RequestID == "" {
			return false
		}
		s.agentFileMu.Lock()
		ch := s.agentFileReqs[msg.RequestID]
		s.agentFileMu.Unlock()
		if ch == nil {
			return false
		}
		select {
		case ch <- msg:
		default:
			log.Printf("agent file request %s result dropped (channel full)", msg.RequestID)
		}
		return true
	case protocol.MsgFileUploadReady, protocol.MsgFileDownloadStart, protocol.MsgFileTransferEnd, protocol.MsgFileTransferError:
		if msg.TaskID == "" {
			return false
		}
		s.agentFileMu.Lock()
		task := s.agentFileTasks[msg.TaskID]
		s.agentFileMu.Unlock()
		if task == nil {
			return false
		}
		select {
		case task.events <- msg:
		default:
			log.Printf("agent file task %s event dropped (type %s, channel full)", msg.TaskID, msg.Type)
		}
		return true
	}
	return false
}

// routeAgentFileBinary consumes device binary frames destined for an agent
// file task. Upload acks and binary end/cancel frames for agent tasks are
// drained and ignored; download chunks are forwarded to the task.
func (s *Server) routeAgentFileBinary(raw []byte) bool {
	typ, taskID, offset, payload, err := protocol.DecodeBinFrameOffset(raw)
	if err != nil {
		return false
	}
	switch typ {
	case protocol.BinFileDownloadChunk, protocol.BinFileUploadAck, protocol.BinFileTransferEnd, protocol.BinFileTransferCancel:
	default:
		return false
	}
	s.agentFileMu.Lock()
	task := s.agentFileTasks[taskID]
	s.agentFileMu.Unlock()
	if task == nil {
		return false
	}
	if typ == protocol.BinFileDownloadChunk {
		data := make([]byte, len(payload))
		copy(data, payload)
		select {
		case task.chunks <- agentChunk{offset: offset, payload: data}:
		default:
			log.Printf("agent file task %s chunk dropped (offset %d, channel full)", taskID, offset)
		}
	}
	return true
}

// newDeviceTools builds the tool set bound to one device connection and one
// thread working directory ("" = device default).
func newDeviceTools(srv *Server, client *ClientConn, cwd string) []agent.Tool {
	shell := &shellTool{srv: srv, client: client, Cwd: cwd}
	reader := &readFileTool{srv: srv, client: client, cwd: cwd}
	writer := &writeFileTool{srv: srv, client: client, cwd: cwd}
	return []agent.Tool{
		shell,
		&listDirTool{srv: srv, client: client},
		reader,
		writer,
		&editTool{reader: reader, writer: writer},
		&grepTool{shell: shell},
		&globTool{shell: shell},
		&todoWriteTool{},
		&webFetchTool{},
		&webSearchTool{},
	}
}

// resolveDevicePath joins a relative tool path onto the thread's working
// directory using the device OS's path separator. Absolute paths and an
// empty cwd pass through unchanged.
func resolveDevicePath(goos, cwd, path string) string {
	if cwd == "" || path == "" {
		return path
	}
	if goos == "windows" {
		// Drive-absolute (C:\x, C:/x), rooted (\x, /x) and UNC (\\srv\x).
		if strings.HasPrefix(path, `\`) || strings.HasPrefix(path, "/") {
			return path
		}
		if len(path) >= 3 && path[1] == ':' && (path[2] == '\\' || path[2] == '/') &&
			(path[0] >= 'a' && path[0] <= 'z' || path[0] >= 'A' && path[0] <= 'Z') {
			return path
		}
		if strings.HasSuffix(cwd, `\`) || strings.HasSuffix(cwd, "/") {
			return cwd + path
		}
		return cwd + `\` + path
	}
	if strings.HasPrefix(path, "/") {
		return path
	}
	if strings.HasSuffix(cwd, "/") {
		return cwd + path
	}
	return cwd + "/" + path
}

// cappedBuffer keeps the last cap bytes written to it.
type cappedBuffer struct {
	buf       []byte
	cap       int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) {
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.cap {
		b.buf = append([]byte(nil), b.buf[len(b.buf)-b.cap:]...)
		b.truncated = true
	}
}

func (b *cappedBuffer) String() string {
	if b.truncated {
		return fmt.Sprintf("[truncated, last %d bytes shown]\n%s", b.cap, b.buf)
	}
	return string(b.buf)
}

// --- shell ---

type shellTool struct {
	srv    *Server
	client *ClientConn
	// Cwd is the thread working directory sent with the exec session ("" =
	// device process default).
	Cwd string
}

func (t *shellTool) Name() string { return "shell" }

func (t *shellTool) Description() string {
	return "Execute a shell command on the remote device and return its exit code, stdout and stderr. Runs without a TTY via the device shell."
}

func (t *shellTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"command": {"type": "string", "description": "Shell command to execute on the device"},
			"timeout": {"type": "integer", "description": "Max runtime in seconds (default 120, max 600)"}
		},
		"required": ["command"]
	}`)
}

func (t *shellTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %v", err)
	}
	if strings.TrimSpace(args.Command) == "" {
		return "", fmt.Errorf("command is required")
	}
	return t.runShell(ctx, args.Command, args.Timeout)
}

// Dangerous marks shell as requiring user approval.
func (t *shellTool) Dangerous() bool { return true }

func (t *shellTool) Summary(raw json.RawMessage) string {
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return ""
	}
	line, _, _ := strings.Cut(args.Command, "\n")
	return truncateRunes(strings.TrimSpace(line), 80)
}

// runShell executes one command through the device shell session channel.
// timeout is in seconds; <=0 means 120, >600 is clamped to 600.
func (t *shellTool) runShell(ctx context.Context, command string, timeout int) (string, error) {
	if timeout <= 0 {
		timeout = 120
	}
	if timeout > 600 {
		timeout = 600
	}

	sessionID := generateID()
	sess := &ProxySession{
		ID:       sessionID,
		ClientID: t.client.ID,
		WriteCh:  make(chan []byte, 8192),
		StderrCh: make(chan []byte, 2048),
		CloseCh:  make(chan struct{}, 1),
		Done:     make(chan struct{}),
		exitDone: make(chan struct{}),
		CloseSSH: func() {},
		ExitSSH:  func(code int) {},
	}
	sess.SetSessionMeta(false, "", command, "", 0, 0)
	if !t.srv.RegisterSession(sess, t.client) {
		return "", fmt.Errorf("too many active sessions on device")
	}
	defer func() {
		t.srv.removeSession(sessionID)
		t.client.mu.Lock()
		delete(t.client.Sessions, sessionID)
		t.client.mu.Unlock()
	}()

	if err := t.client.Send(&protocol.Message{
		Type:      protocol.MsgNewSession,
		ClientID:  t.client.ID,
		SessionID: sessionID,
		Command:   command,
		Cwd:       t.Cwd,
		Pty:       false,
	}); err != nil {
		return "", fmt.Errorf("failed to reach device: %v", err)
	}

	stdout := &cappedBuffer{cap: 48 * 1024}
	stderr := &cappedBuffer{cap: 16 * 1024}
	timer := time.NewTimer(time.Duration(timeout) * time.Second)
	defer timer.Stop()

	format := func(codeLine string) string {
		return fmt.Sprintf("%s\n<stdout>\n%s\n</stdout>\n<stderr>\n%s\n</stderr>", codeLine, stdout, stderr)
	}

	for {
		select {
		case data, ok := <-sess.WriteCh:
			if !ok {
				continue
			}
			stdout.Write(data)
		case data, ok := <-sess.StderrCh:
			if !ok {
				continue
			}
			stderr.Write(data)
		case <-sess.CloseCh:
			sess.NotifyObserversClose()
			sess.CloseOutput()
			code := sess.WaitExitCode(500 * time.Millisecond)
			if code < 0 {
				return format("exit code: unknown"), nil
			}
			return format(fmt.Sprintf("exit code: %d", code)), nil
		case <-ctx.Done():
			_ = t.client.Send(&protocol.Message{Type: protocol.MsgClose, SessionID: sessionID})
			select {
			case <-sess.CloseCh:
				sess.NotifyObserversClose()
				sess.CloseOutput()
			case <-time.After(2 * time.Second):
			}
			return "", fmt.Errorf("canceled; partial output:\n%s", format("exit code: unknown (canceled)"))
		case <-timer.C:
			_ = t.client.Send(&protocol.Message{Type: protocol.MsgClose, SessionID: sessionID})
			select {
			case <-sess.CloseCh:
				sess.NotifyObserversClose()
				sess.CloseOutput()
			case <-time.After(2 * time.Second):
			}
			return "", fmt.Errorf("timed out after %ds; partial output:\n%s", timeout, format("exit code: unknown (timed out)"))
		}
	}
}

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
	return "Read a file from the remote device. Long files are truncated by lines (default 2000); use offset/limit to page through. Set anchors=true to prefix every line with an N#hash anchor for the edit tool's line_edits mode. Files larger than max_bytes are refused; binary content is returned base64-encoded."
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
	for {
		select {
		case msg := <-task.events:
			switch msg.Type {
			case protocol.MsgFileDownloadStart:
				size = msg.Size
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
	for {
		select {
		case c := <-task.chunks:
			parts[c.offset] = c.payload
			received += int64(len(c.payload))
			if received > maxBytes {
				cancelTransfer()
				return nil, fmt.Errorf("file %q grew past max_bytes %d during transfer", path, maxBytes)
			}
		case msg := <-task.events:
			switch msg.Type {
			case protocol.MsgFileTransferEnd:
				if !msg.Success {
					return nil, fmt.Errorf("read %q: transfer failed on device", path)
				}
				// Chunks and the end event arrive on separate channels fed by
				// the same read loop, so every chunk was enqueued before the
				// end — but select may deliver the end first. Drain any
				// pending chunks before assembling.
				for drained := false; !drained; {
					select {
					case c := <-task.chunks:
						parts[c.offset] = c.payload
						received += int64(len(c.payload))
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
				if size > 0 && int64(len(data)) != size {
					return nil, fmt.Errorf("read %q: got %d bytes, expected %d", path, len(data), size)
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

	if err := t.client.Send(&protocol.Message{
		Type:     protocol.MsgFileUploadStart,
		TaskID:   taskID,
		Path:     path,
		Size:     int64(len(content)),
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
				if !msg.Success {
					return "", fmt.Errorf("write %q: transfer failed on device", path)
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

// --- shared argument helpers ---

// toolPathArg extracts the common "path" argument for one-line summaries.
func toolPathArg(raw json.RawMessage) string {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return ""
	}
	return args.Path
}

// toolPatternArg extracts the common "pattern" argument for one-line summaries.
func toolPatternArg(raw json.RawMessage) string {
	var args struct {
		Pattern string `json:"pattern"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return ""
	}
	return args.Pattern
}

// truncateRunes cuts s to at most max runes, appending an ellipsis when cut.
func truncateRunes(s string, max int) string {
	n := 0
	for i := range s {
		if n == max {
			return s[:i] + "…"
		}
		n++
	}
	return s
}

// shellQuote wraps s in POSIX single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// --- edit ---

// editTool replaces exact text in a device file (read → validate → replace →
// write back) and keeps the unified diff of the last successful change.
type editTool struct {
	reader *readFileTool
	writer *writeFileTool

	mu       sync.Mutex
	lastDiff string
}

func (t *editTool) Name() string { return "edit" }

func (t *editTool) Description() string {
	return "Edit a file on the remote device. Two modes: (1) old_string/new_string — replace exact text (a single occurrence unless replace_all is set); (2) line_edits — hashline patches referencing N#hash anchors from read_file anchors=true; each patch replaces lines start..end with new_string, and stale anchors are rejected before anything is written. Prefer this over write_file when modifying an existing file."
}

func (t *editTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path":        {"type": "string", "description": "File path on the device"},
			"old_string":  {"type": "string", "description": "Exact text to replace (mode 1)"},
			"new_string":  {"type": "string", "description": "Replacement text (mode 1)"},
			"replace_all": {"type": "boolean", "description": "Replace every occurrence instead of requiring a unique match (mode 1)"},
			"line_edits":  {"type": "array", "description": "Hashline patches (mode 2), anchors from read_file anchors=true", "items": {"type": "object", "properties": {"start": {"type": "string", "description": "Anchor of first line to replace, N#hash"}, "end": {"type": "string", "description": "Anchor of last line to replace (default: same as start)"}, "new_string": {"type": "string", "description": "Replacement lines; empty deletes the range"}}, "required": ["start", "new_string"]}}
		},
		"required": ["path"]
	}`)
}

// Dangerous marks edit as requiring user approval.
func (t *editTool) Dangerous() bool { return true }

func (t *editTool) Summary(raw json.RawMessage) string {
	return toolPathArg(raw)
}

// LastDiff returns the unified diff of the last successful edit ("" when the
// last call failed or changed nothing).
func (t *editTool) LastDiff() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastDiff
}

func (t *editTool) setLastDiff(diff string) {
	t.mu.Lock()
	t.lastDiff = diff
	t.mu.Unlock()
}

func (t *editTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Path       string `json:"path"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
		LineEdits  []lineEditArg `json:"line_edits"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %v", err)
	}
	if args.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	if len(args.LineEdits) > 0 && args.OldString != "" {
		return "", fmt.Errorf("pass either old_string or line_edits, not both")
	}
	if len(args.LineEdits) == 0 && args.OldString == "" {
		return "", fmt.Errorf("old_string or line_edits is required")
	}
	args.Path = t.reader.resolve(args.Path)

	data, err := t.reader.download(ctx, args.Path, readFileHardCap)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) || bytes.IndexByte(sampleBytes(data), 0) >= 0 {
		return "", fmt.Errorf("edit %q: binary files are not supported", args.Path)
	}

	oldContent := string(data)
	var newContent string
	var n int
	if len(args.LineEdits) > 0 {
		newContent, n, err = applyHashlineEdits(oldContent, args.LineEdits)
	} else {
		newContent, n, err = applyEdit(oldContent, args.OldString, args.NewString, args.ReplaceAll)
	}
	if err != nil {
		return "", fmt.Errorf("edit %q: %v", args.Path, err)
	}
	if newContent == oldContent {
		t.setLastDiff("")
		return fmt.Sprintf("%s unchanged (replacement is identical)", args.Path), nil
	}

	diff := unifiedDiff(oldContent, newContent, args.Path)
	if _, err := t.writer.upload(ctx, args.Path, []byte(newContent), 0); err != nil {
		return "", err
	}
	t.setLastDiff(diff)

	plural := "s"
	if n == 1 {
		plural = ""
	}
	return fmt.Sprintf("edited %s (%d replacement%s)", args.Path, n, plural), nil
}

// applyEdit validates and performs the textual replacement: old_string must
// occur at least once, and exactly once unless replaceAll is set.
func applyEdit(content, oldString, newString string, replaceAll bool) (string, int, error) {
	if oldString == "" {
		return "", 0, fmt.Errorf("old_string must not be empty")
	}
	count := strings.Count(content, oldString)
	if count == 0 {
		return "", 0, fmt.Errorf("old_string not found in file")
	}
	if count > 1 && !replaceAll {
		return "", 0, fmt.Errorf("old_string matches %d times; add more context or set replace_all", count)
	}
	n := 1
	if replaceAll {
		n = count
	}
	return strings.Replace(content, oldString, newString, n), n, nil
}

// --- grep ---

// grepTool searches file contents on the device through the shell channel
// (grep on unix, findstr on windows).
type grepTool struct {
	shell *shellTool
}

func (t *grepTool) Name() string { return "grep" }

func (t *grepTool) Description() string {
	return "Search file contents on the remote device for an extended regex pattern, recursively. Returns matching lines with file paths and line numbers."
}

func (t *grepTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern": {"type": "string", "description": "Extended regular expression to search for"},
			"path":    {"type": "string", "description": "File or directory to search (default: current directory)"},
			"include": {"type": "string", "description": "Optional file name glob to limit matches (e.g. \"*.go\"); unix only"}
		},
		"required": ["pattern"]
	}`)
}

// Dangerous marks grep as read-only (no approval needed).
func (t *grepTool) Dangerous() bool { return false }

func (t *grepTool) Summary(raw json.RawMessage) string {
	return toolPatternArg(raw)
}

func (t *grepTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
		Include string `json:"include"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %v", err)
	}
	if args.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	if args.Path == "" {
		args.Path = "."
	}

	var cmd string
	if t.shell.client.OS == "windows" {
		cmd = fmt.Sprintf(`findstr /s /n /c:"%s" %s\*`, args.Pattern, args.Path)
	} else {
		var b strings.Builder
		b.WriteString("grep -rnE")
		if args.Include != "" {
			b.WriteString(" --include=" + shellQuote(args.Include))
		}
		b.WriteString(" -- ")
		b.WriteString(shellQuote(args.Pattern))
		b.WriteString(" ")
		b.WriteString(shellQuote(args.Path))
		cmd = b.String()
	}
	return t.shell.runShell(ctx, cmd, 120)
}

// --- glob ---

// globTool finds files by name pattern on the device through the shell
// channel (find on unix, dir on windows).
type globTool struct {
	shell *shellTool
}

func (t *globTool) Name() string { return "glob" }

func (t *globTool) Description() string {
	return "Find files on the remote device by name pattern, recursively from a starting directory."
}

func (t *globTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern": {"type": "string", "description": "File name pattern (e.g. \"*.go\")"},
			"path":    {"type": "string", "description": "Directory to search from (default: current directory)"}
		},
		"required": ["pattern"]
	}`)
}

// Dangerous marks glob as read-only (no approval needed).
func (t *globTool) Dangerous() bool { return false }

func (t *globTool) Summary(raw json.RawMessage) string {
	return toolPatternArg(raw)
}

func (t *globTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %v", err)
	}
	if args.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	if args.Path == "" {
		args.Path = "."
	}

	var cmd string
	if t.shell.client.OS == "windows" {
		cmd = fmt.Sprintf(`dir /s /b %s\%s`, args.Path, args.Pattern)
	} else {
		cmd = fmt.Sprintf("find %s -name %s", shellQuote(args.Path), shellQuote(args.Pattern))
	}
	return t.shell.runShell(ctx, cmd, 120)
}

// --- todo_write ---

// todoItem is one checklist entry maintained by the todo_write tool.
type todoItem struct {
	Content string `json:"content"`
	Status  string `json:"status"` // "pending" | "in_progress" | "completed"
}

// todoWriteTool records the model's plan/progress checklist. It never
// touches the device; the server mirrors the list to attached browsers.
type todoWriteTool struct{}

func (t *todoWriteTool) Name() string { return "todo_write" }

func (t *todoWriteTool) Description() string {
	return "Record and update a checklist of tasks for multi-step work. Replaces the whole list each call; every item needs a content and a status (pending, in_progress or completed)."
}

func (t *todoWriteTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"items": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"content": {"type": "string", "description": "Task description"},
						"status":   {"type": "string", "enum": ["pending", "in_progress", "completed"]}
					},
					"required": ["content", "status"]
				}
			}
		},
		"required": ["items"]
	}`)
}

// Dangerous marks todo_write as a no-op on the device (no approval needed).
func (t *todoWriteTool) Dangerous() bool { return false }

func (t *todoWriteTool) Summary(raw json.RawMessage) string {
	var args struct {
		Items []todoItem `json:"items"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return ""
	}
	return fmt.Sprintf("%d items", len(args.Items))
}

func (t *todoWriteTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Items []todoItem `json:"items"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %v", err)
	}
	for i, item := range args.Items {
		if strings.TrimSpace(item.Content) == "" {
			return "", fmt.Errorf("item %d: content is required", i)
		}
		switch item.Status {
		case "pending", "in_progress", "completed":
		default:
			return "", fmt.Errorf("item %d: invalid status %q (pending, in_progress or completed)", i, item.Status)
		}
	}
	return fmt.Sprintf("todo list updated: %d items", len(args.Items)), nil
}

// --- unified diff (self-contained line LCS) ---

// diffOpcode is one matching/edit block between two line lists, in the style
// of Python difflib: tag is 'e' (equal), 'd' (delete), 'i' (insert) or 'r'
// (replace); the block covers a[i1:i2] and b[j1:j2].
type diffOpcode struct {
	tag            byte
	i1, i2, j1, j2 int
}

// diffMaxCells caps the LCS dynamic-programming table; larger inputs fall
// back to a single replace block (still a correct diff, just coarser).
const diffMaxCells = 4 << 20

// diffOpcodes computes the edit script between line lists a and b via LCS
// dynamic programming.
func diffOpcodes(a, b []string) []diffOpcode {
	if (len(a)+1)*(len(b)+1) > diffMaxCells {
		switch {
		case len(a) == 0 && len(b) == 0:
			return nil
		case len(a) == 0:
			return []diffOpcode{{tag: 'i', j2: len(b)}}
		case len(b) == 0:
			return []diffOpcode{{tag: 'd', i2: len(a)}}
		default:
			return []diffOpcode{{tag: 'r', i2: len(a), j2: len(b)}}
		}
	}

	// dp[i][j] = LCS length of a[i:] and b[j:], filled bottom-up.
	w := len(b) + 1
	dp := make([]uint32, (len(a)+1)*w)
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i*w+j] = dp[(i+1)*w+j+1] + 1
			} else {
				dp[i*w+j] = max(dp[(i+1)*w+j], dp[i*w+j+1])
			}
		}
	}

	var ops []diffOpcode
	push := func(tag byte, i1, i2, j1, j2 int) {
		if i1 == i2 && j1 == j2 {
			return
		}
		if n := len(ops); n > 0 && ops[n-1].tag == tag {
			ops[n-1].i2 = i2
			ops[n-1].j2 = j2
			return
		}
		ops = append(ops, diffOpcode{tag: tag, i1: i1, i2: i2, j1: j1, j2: j2})
	}
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			push('e', i, i+1, j, j+1)
			i++
			j++
		case dp[(i+1)*w+j] >= dp[i*w+j+1]:
			push('d', i, i+1, j, j)
			i++
		default:
			push('i', i, i, j, j+1)
			j++
		}
	}
	for i < len(a) {
		push('d', i, i+1, j, j)
		i++
	}
	for j < len(b) {
		push('i', i, i, j, j+1)
		j++
	}
	return ops
}

// diffContextLines is the number of unchanged lines shown around each hunk.
const diffContextLines = 3

// groupDiffOpcodes splits an opcode list into hunks: each hunk contains
// change blocks plus up to n lines of surrounding context, merging changes
// separated by at most 2n equal lines.
func groupDiffOpcodes(ops []diffOpcode, n int) [][]diffOpcode {
	var groups [][]diffOpcode
	var group []diffOpcode
	for _, op := range ops {
		if op.tag != 'e' {
			group = append(group, op)
			continue
		}
		size := op.i2 - op.i1
		if len(group) == 0 {
			// Leading context: keep only the last n lines.
			if size > n {
				op.i1 = op.i2 - n
				op.j1 = op.j2 - n
			}
			group = append(group, op)
			continue
		}
		if size > 2*n {
			// Gap too large: close this hunk with n trailing lines and open
			// a new one with n leading lines.
			group = append(group, diffOpcode{tag: 'e', i1: op.i1, i2: op.i1 + n, j1: op.j1, j2: op.j1 + n})
			groups = append(groups, group)
			group = []diffOpcode{{tag: 'e', i1: op.i2 - n, i2: op.i2, j1: op.j2 - n, j2: op.j2}}
			continue
		}
		group = append(group, op)
	}
	if len(group) > 0 {
		for _, op := range group {
			if op.tag != 'e' {
				groups = append(groups, group)
				break
			}
		}
	}
	return groups
}

// splitDiffLines splits text into lines keeping the newline terminators (the
// last line may lack one). Empty text yields no lines.
func splitDiffLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.SplitAfter(text, "\n")
	// SplitAfter leaves a phantom empty element after a trailing newline.
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// writeDiffLine writes one diff body line (" "/"-"/"+" prefix), adding the
// missing-newline marker when the source line lacks its terminator.
func writeDiffLine(b *strings.Builder, prefix byte, line string) {
	b.WriteByte(prefix)
	if strings.HasSuffix(line, "\n") {
		b.WriteString(line)
		return
	}
	b.WriteString(line)
	b.WriteString("\n\\ No newline at end of file\n")
}

// unifiedDiff renders oldText→newText as a unified diff with ---/+++ file
// headers and @@ hunks with 3 lines of context. "" when identical.
func unifiedDiff(oldText, newText, path string) string {
	if oldText == newText {
		return ""
	}
	a := splitDiffLines(oldText)
	b := splitDiffLines(newText)

	var out strings.Builder
	display := strings.TrimPrefix(path, "/")
	fmt.Fprintf(&out, "--- a/%s\n+++ b/%s\n", display, display)
	for _, hunk := range groupDiffOpcodes(diffOpcodes(a, b), diffContextLines) {
		oldStart, oldEnd := hunk[0].i1, hunk[len(hunk)-1].i2
		newStart, newEnd := hunk[0].j1, hunk[len(hunk)-1].j2
		oldCount, newCount := oldEnd-oldStart, newEnd-newStart
		// Unified format numbers a zero-length range by the line before it.
		oldLine, newLine := oldStart+1, newStart+1
		if oldCount == 0 {
			oldLine = oldStart
		}
		if newCount == 0 {
			newLine = newStart
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", oldLine, oldCount, newLine, newCount)
		for _, op := range hunk {
			switch op.tag {
			case 'e':
				for _, l := range a[op.i1:op.i2] {
					writeDiffLine(&out, ' ', l)
				}
			case 'd':
				for _, l := range a[op.i1:op.i2] {
					writeDiffLine(&out, '-', l)
				}
			case 'i':
				for _, l := range b[op.j1:op.j2] {
					writeDiffLine(&out, '+', l)
				}
			case 'r':
				for _, l := range a[op.i1:op.i2] {
					writeDiffLine(&out, '-', l)
				}
				for _, l := range b[op.j1:op.j2] {
					writeDiffLine(&out, '+', l)
				}
			}
		}
	}
	return out.String()
}

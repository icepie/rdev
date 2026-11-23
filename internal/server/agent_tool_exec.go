package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"rdev/internal/protocol"
)

// cappedBuffer keeps the last cap bytes written to it.
type cappedBuffer struct {
	onWrite   func(string)
	buf       []byte
	start     int // oldest byte once the buffer is full
	cap       int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) {
	if b.onWrite != nil {
		b.onWrite(string(p))
	}
	if len(p) == 0 {
		return
	}
	if b.cap <= 0 {
		b.truncated = true
		return
	}
	if b.buf == nil {
		b.buf = make([]byte, 0, b.cap)
	}
	if len(p) >= b.cap {
		b.truncated = b.truncated || len(b.buf) > 0 || len(p) > b.cap
		b.buf = b.buf[:b.cap]
		copy(b.buf, p[len(p)-b.cap:])
		b.start = 0
		return
	}
	if len(b.buf) < b.cap {
		n := min(b.cap-len(b.buf), len(p))
		b.buf = append(b.buf, p[:n]...)
		p = p[n:]
	}
	if len(p) > 0 {
		n := copy(b.buf[b.start:], p)
		copy(b.buf, p[n:])
		b.start = (b.start + len(p)) % b.cap
		b.truncated = true
	}
}

func (b *cappedBuffer) String() string {
	if !b.truncated {
		return string(b.buf)
	}
	var out strings.Builder
	out.Grow(len(b.buf) + 48)
	out.WriteString("[truncated, last ")
	out.WriteString(strconv.Itoa(b.cap))
	out.WriteString(" bytes shown]\n")
	out.Write(b.buf[b.start:])
	out.Write(b.buf[:b.start])
	return out.String()
}

// drainAgentOutput collects output already accepted before a session closed.
// CloseOutput must run first so both ranges terminate deterministically.
func drainAgentOutput(sess *ProxySession, stdout, stderr *cappedBuffer) {
	for data := range sess.WriteCh {
		stdout.Write(data)
	}
	for data := range sess.StderrCh {
		stderr.Write(data)
	}
}

// --- shell ---

type shellTool struct {
	srv    *Server
	client *ClientConn
	// Cwd is the thread working directory sent with the exec session ("" =
	// device process default).
	Cwd string
	Env []string
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
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	result, err := t.runShellReporting(runCtx, command, nil)
	return result.Output, err
}

type agentCommandResult struct {
	Output   string
	ExitCode int
}

// runShellReporting uses the caller's lifetime. Background services may live
// beyond the foreground shell timeout and stream output until explicitly stopped.
func (t *shellTool) runShellReporting(ctx context.Context, command string, output func(string)) (agentCommandResult, error) {
	if err := ctx.Err(); err != nil {
		return agentCommandResult{}, err
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
		return agentCommandResult{}, fmt.Errorf("too many active sessions on device")
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
		Env:       t.Env,
		Pty:       false,
	}); err != nil {
		return agentCommandResult{}, fmt.Errorf("failed to reach device: %v", err)
	}

	stdout := &cappedBuffer{cap: 48 * 1024, onWrite: output}
	stderr := &cappedBuffer{cap: 16 * 1024, onWrite: output}

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
			drainAgentOutput(sess, stdout, stderr)
			code := sess.WaitExitCode(500 * time.Millisecond)
			if code < 0 {
				return agentCommandResult{Output: format("exit code: unknown"), ExitCode: -1}, nil
			}
			return agentCommandResult{Output: format(fmt.Sprintf("exit code: %d", code)), ExitCode: code}, nil
		case <-ctx.Done():
			_ = t.client.Send(&protocol.Message{Type: protocol.MsgClose, SessionID: sessionID})
			select {
			case <-sess.CloseCh:
				sess.NotifyObserversClose()
				sess.CloseOutput()
				drainAgentOutput(sess, stdout, stderr)
			case <-time.After(2 * time.Second):
			}
			return agentCommandResult{}, fmt.Errorf("%w; partial output:\n%s", ctx.Err(), format("exit code: unknown (interrupted)"))
		}
	}
}

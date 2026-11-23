package client

import (
	"context"
	"io"
	"log"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"rdev/internal/protocol"
	"rdev/internal/ptyutil"
	"rdev/internal/wincompat"
)

// sftpRWC bridges SFTP server over WebSocket
type sftpRWC struct {
	reader io.Reader
	writer io.Writer
	closer io.Closer
}

func (s *sftpRWC) Read(p []byte) (int, error)  { return s.reader.Read(p) }
func (s *sftpRWC) Write(p []byte) (int, error) { return s.writer.Write(p) }
func (s *sftpRWC) Close() error                { return s.closer.Close() }

// clientSession represents a proxied session on the client device
type clientSession struct {
	id string

	// PTY mode
	ptyProc *ptyutil.Process

	// Non-PTY exec mode
	stdinPipe  io.WriteCloser
	stdinOnce  sync.Once
	cmdWaitFn  func() (int, error)
	cancelExec func()

	// SFTP mode
	sftpInput  *io.PipeWriter
	sftpOutput *io.PipeReader

	done chan struct{}
	once sync.Once
}

func (s *clientSession) close() {
	s.once.Do(func() {
		if s.ptyProc != nil {
			s.ptyProc.Close()
		}
		if s.cancelExec != nil {
			s.cancelExec()
		}
		s.closeInput()
		close(s.done)
	})
}

// Keep input handles immutable after publication; EOF and process exit may race.
func (s *clientSession) closeInput() {
	s.stdinOnce.Do(func() {
		if s.stdinPipe != nil {
			s.stdinPipe.Close()
		}
		if s.sftpInput != nil {
			s.sftpInput.Close()
		}
	})
}

func (c *Client) handleBinData(sessionID string, data []byte) {
	c.mu.Lock()
	sess, ok := c.sessions[sessionID]
	c.mu.Unlock()
	if !ok || len(data) == 0 {
		return
	}

	switch {
	case sess.ptyProc != nil:
		sess.ptyProc.Write(data)
	case sess.stdinPipe != nil:
		sess.stdinPipe.Write(data)
	case sess.sftpInput != nil:
		sess.sftpInput.Write(data)
	}
}

// --- Session handling ---

func (c *Client) handleNewSession(msg *protocol.Message) {
	sessionID := msg.SessionID
	log.Printf("new session: id=%s subsystem=%q command=%q pty=%v",
		sessionID, msg.Subsystem, msg.Command, msg.Pty)

	var sess *clientSession
	var err error

	switch msg.Subsystem {
	case "sftp":
		sess, err = c.startSFTPSession(sessionID)
	default:
		sess, err = c.startShellExecSession(msg)
	}

	if err != nil {
		log.Printf("session %s start failed: %v", sessionID, err)
		c.sendClose(sessionID)
		return
	}

	c.mu.Lock()
	c.sessions[sessionID] = sess
	c.mu.Unlock()
}

func (c *Client) startShellExecSession(msg *protocol.Message) (*clientSession, error) {
	sess := &clientSession{
		id:   msg.SessionID,
		done: make(chan struct{}),
	}

	if msg.Pty {
		cfg := &ptyutil.Config{
			Command: msg.Command,
			Shell:   c.shell,
			Env:     msg.Env,
			Term:    msg.Term,
			Rows:    uint16(msg.Rows),
			Cols:    uint16(msg.Cols),
			Modes:   convertModes(msg.Modes),
		}
		proc, ptyErr := ptyutil.Start(cfg)
		if ptyErr != nil {
			// PTY unavailable (e.g. ConPty on Wine, no /dev/pts) → fallback to exec mode
			log.Printf("session %s: PTY unavailable (%v), falling back to exec mode", msg.SessionID, ptyErr)
		} else {
			sess.ptyProc = proc

			// Read PTY output -> coalesced binary send to server.
			// WinPTY on legacy Windows emits many tiny chunks; a slightly longer
			// output-only aggregation window keeps SSH input responsive while
			// avoiding WebSocket frame storms during screen redraws.
			cw := newCoalescingWriterWithInterval(c, msg.SessionID, protocol.BinData, 16*time.Millisecond)
			readDone := make(chan struct{})
			go func() {
				defer close(readDone)
				io.Copy(cw, proc)
				cw.flush()
			}()

			go func() {
				exitCode, _ := proc.Wait()

				// On some Windows versions, ConPTY output pipe does not EOF
				// after the process exits. Close the input side first so the
				// ConPTY host drains and closes the output pipe, unblocking Read.
				// For the go-pty ConPTY backend this means we close inPipe early;
				// Process.Close handles the rest idempotently.
				proc.CloseInput()

				// Give the read loop a few seconds to drain remaining output.
				select {
				case <-readDone:
					// read loop finished normally
				case <-time.After(5 * time.Second):
					log.Printf("session %s: PTY read did not finish after process exit, forcing close", msg.SessionID)
				}

				proc.Close()
				c.sendExitCode(msg.SessionID, exitCode)
				c.sendClose(msg.SessionID)
				sess.close()
			}()

			log.Printf("session %s: PTY started (cmd=%q)", msg.SessionID, msg.Command)
			return sess, nil
		}
	}

	if msg.Command != "" {
		if gitCmd, ok := parseGitSmartSSHCommand(msg.Command); ok && !hasSystemGitCommand(gitCmd.Name) {
			return c.startGitFallbackSession(sess, gitCmd)
		}
	}

	// Exec mode (non-PTY, or PTY fallback)
	shell := c.shell
	if shell == "" {
		shell = os.Getenv("SHELL")
		if shell == "" {
			shell = os.Getenv("COMSPEC")
			if shell == "" {
				if runtime.GOOS == "windows" {
					shell = "cmd.exe"
				} else {
					shell = "/bin/sh"
				}
			}
		}
	}

	flag := "-c"
	if runtime.GOOS == "windows" {
		flag = "/c"
	}

	ctx, cancel := context.WithCancel(context.Background())
	started := false
	defer func() {
		if !started {
			cancel()
		}
	}()
	var cmd *exec.Cmd
	if msg.Command != "" {
		cmd = exec.CommandContext(ctx, shell, flag, msg.Command)
	} else {
		cmd = exec.CommandContext(ctx, shell)
	}
	cmd.Env = append(os.Environ(), msg.Env...)

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		stdinR.Close()
		stdinW.Close()
		return nil, err
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		stdinR.Close()
		stdinW.Close()
		stdoutR.Close()
		stdoutW.Close()
		return nil, err
	}

	cmd.Stdin = stdinR
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW

	releaseProcess, err := startExecCommand(cmd)
	if err != nil {
		stdinR.Close()
		stdinW.Close()
		stdoutR.Close()
		stdoutW.Close()
		stderrR.Close()
		stderrW.Close()
		return nil, err
	}

	stdinR.Close()
	stdoutW.Close()
	stderrW.Close()

	started = true
	sess.cancelExec = func() {
		cancel()
		stdoutR.Close()
		stderrR.Close()
	}
	sess.stdinPipe = wincompat.EncodeInput(stdinW)
	sess.cmdWaitFn = func() (int, error) {
		defer releaseProcess()
		err := cmd.Wait()
		if err == nil {
			return 0, nil
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), nil
		}
		return -1, err
	}

	var ioWg sync.WaitGroup

	ioWg.Add(1)
	go func() {
		defer ioWg.Done()
		defer stdoutR.Close()
		cw := newCoalescingWriter(c, msg.SessionID, protocol.BinData)
		io.Copy(cw, wincompat.DecodeOutput(stdoutR))
		cw.flush()
	}()

	ioWg.Add(1)
	go func() {
		defer ioWg.Done()
		defer stderrR.Close()
		cw := newCoalescingWriter(c, msg.SessionID, protocol.BinStderr)
		io.Copy(cw, wincompat.DecodeOutput(stderrR))
		cw.flush()
	}()

	go func() {
		exitCode, _ := sess.cmdWaitFn()
		ioWg.Wait()
		c.sendExitCode(msg.SessionID, exitCode)
		c.sendClose(msg.SessionID)
		sess.close()
	}()

	log.Printf("session %s: exec started (cmd=%q)", msg.SessionID, msg.Command)

	return sess, nil
}

func (c *Client) startSFTPSession(sessionID string) (*clientSession, error) {
	pr1, pw1 := io.Pipe()
	pr2, pw2 := io.Pipe()

	rwc := &sftpRWC{reader: pr1, writer: pw2, closer: pw1}

	sess := &clientSession{
		id:         sessionID,
		sftpInput:  pw1,
		sftpOutput: pr2,
		done:       make(chan struct{}),
	}

	go func() {
		defer pw2.Close()
		defer pr1.Close()

		server, err := sftp.NewServer(rwc)
		if err != nil {
			log.Printf("session %s: sftp init error: %v", sessionID, err)
			c.sendClose(sessionID)
			return
		}
		defer server.Close()

		if err := server.Serve(); err != nil && err != io.EOF {
			log.Printf("session %s: sftp error: %v", sessionID, err)
		}
		c.sendClose(sessionID)
	}()

	// SFTP output → binary frames (coalesced)
	go func() {
		cw := newCoalescingWriter(c, sessionID, protocol.BinData)
		io.Copy(cw, pr2)
		cw.flush()
	}()

	log.Printf("session %s: SFTP server started", sessionID)
	return sess, nil
}

func (c *Client) handleStdinClose(msg *protocol.Message) {
	c.mu.Lock()
	sess, ok := c.sessions[msg.SessionID]
	c.mu.Unlock()
	if !ok {
		return
	}
	sess.closeInput()
}

func (c *Client) handleResize(msg *protocol.Message) {
	c.mu.Lock()
	sess, ok := c.sessions[msg.SessionID]
	c.mu.Unlock()
	if !ok || sess.ptyProc == nil {
		return
	}
	if err := sess.ptyProc.Resize(uint16(msg.Rows), uint16(msg.Cols)); err != nil {
		// Silently ignore resize on closed PTY (common during session teardown)
		log.Printf("session %s: resize error: %v", msg.SessionID, err)
	}
}

func (c *Client) handleClose(msg *protocol.Message) {
	c.mu.Lock()
	sess, ok := c.sessions[msg.SessionID]
	if ok {
		delete(c.sessions, msg.SessionID)
	}
	c.mu.Unlock()
	if ok {
		sess.close()
		log.Printf("session %s closed", msg.SessionID)
	}
}

func (c *Client) sendClose(sessionID string) error {
	return c.send(&protocol.Message{
		Type:      protocol.MsgClose,
		SessionID: sessionID,
	})
}

func (c *Client) sendExitCode(sessionID string, code int) error {
	return c.send(&protocol.Message{
		Type:      protocol.MsgExitCode,
		SessionID: sessionID,
		ExitCode:  code,
	})
}

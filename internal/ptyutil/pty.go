// Package ptyutil provides cross-platform PTY support using go-pty.
// On Unix it uses creack/pty; on Windows it uses ConPty (native pseudo-console).
// It also supports applying SSH terminal modes to the PTY.
package ptyutil

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"runtime"
	"sync/atomic"
	"syscall"

	"github.com/aymanbagabas/go-pty"
	"golang.org/x/crypto/ssh"
)

var errWinPTYUnsupported = errors.New("winpty unsupported on this platform")

type ptyBackend interface {
	Read([]byte) (int, error)
	Write([]byte) (int, error)
	Resize(rows, cols uint16) error
	Close() error
	Wait() (int, error)
}

// Process represents a running process attached to a PTY.
type Process struct {
	legacy ptyBackend
	pty    pty.Pty
	cmd    *pty.Cmd

	closed int32 // atomic: 1 = closed, prevents use-after-close
}

// Config for creating a PTY process
type Config struct {
	Command string            // command to execute (empty = shell)
	Shell   string            // shell to use (empty = auto-detect)
	Env     []string          // extra environment variables
	Term    string            // TERM value
	Rows    uint16            // initial rows (0 = 24)
	Cols    uint16            // initial cols (0 = 80)
	Modes   ssh.TerminalModes // SSH terminal modes (nil = skip)
}

// shellFlag returns the command-line flag for running a command in the shell.
// Windows cmd.exe uses /c; POSIX shells use -c.
func shellFlag() string {
	if runtime.GOOS == "windows" {
		return "/c"
	}
	return "-c"
}

// initialSize applies the 24x80 default shared by both backends.
func initialSize(cfg *Config) (rows, cols uint16) {
	rows, cols = cfg.Rows, cfg.Cols
	if rows == 0 {
		rows = 24
	}
	if cols == 0 {
		cols = 80
	}
	return rows, cols
}

// Start creates a new PTY process. On Windows this uses ConPty;
// on Unix this uses creack/pty. On unsupported platforms it returns an error.
func Start(cfg *Config) (proc *Process, err error) {
	defer func() {
		if r := recover(); r != nil {
			proc, err = startWinPTY(cfg)
			if err != nil {
				err = fmt.Errorf("pty panic: %v; winpty fallback: %w", r, err)
			}
		}
	}()

	pseudo, err := pty.New()
	if err != nil {
		if proc, winptyErr := startWinPTY(cfg); winptyErr == nil {
			return proc, nil
		}
		return nil, fmt.Errorf("pty new: %w", err)
	}

	shell := cfg.Shell
	fallbacks := []string(nil)
	if shell == "" {
		shell = detectShell()
		fallbacks = shellCandidates()
	}

	flag := shellFlag()

	newCmd := func(shell string) *pty.Cmd {
		if cfg.Command != "" {
			return pseudo.Command(shell, flag, cfg.Command)
		}
		return pseudo.Command(shell)
	}

	// Build environment
	env := os.Environ()
	env = append(env, cfg.Env...)
	if cfg.Term != "" {
		env = append(env, "TERM="+cfg.Term)
	} else {
		env = append(env, "TERM=xterm-256color")
	}

	// Resize to initial dimensions
	rows, cols := initialSize(cfg)
	// Non-fatal: some platforms may not support resize before start.
	_ = pseudo.Resize(int(cols), int(rows))

	// Apply SSH terminal modes if provided (Unix only, no-op on Windows)
	if cfg.Modes != nil {
		_ = pty.ApplyTerminalModes(int(pseudo.Fd()), int(cols), int(rows), cfg.Modes)
	}

	cmd := newCmd(shell)
	cmd.Env = env
	err = cmd.Start()
	// A detected shell can exist yet be unusable (wrong permissions, removed by
	// an update, or missing on Android). Try the remaining candidates before
	// reporting the failure, so a stale $SHELL cannot break every session.
	for _, candidate := range fallbacks {
		if err == nil || !retryableShellError(err) {
			break
		}
		cmd = newCmd(candidate)
		cmd.Env = env
		err = cmd.Start()
		if err == nil {
			log.Printf("pty: fell back to shell %s", candidate)
		}
	}
	if err != nil {
		pseudo.Close()
		return nil, fmt.Errorf("pty start: %w", err)
	}

	return &Process{
		pty: pseudo,
		cmd: cmd,
	}, nil
}

// retryableShellError reports whether another shell may work instead.
func retryableShellError(err error) bool {
	return errors.Is(err, fs.ErrPermission) || errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOEXEC)
}

// shellCandidates lists fallbacks in the order a platform should try them.
func shellCandidates() []string {
	candidates := []string{os.Getenv("SHELL")}
	for _, shell := range detectedShells() {
		if shell != "" && shell != candidates[0] {
			candidates = append(candidates, shell)
		}
	}
	return candidates
}

// detectedShells returns the platform-specific shell order without $SHELL.
func detectedShells() []string {
	if runtime.GOOS == "android" {
		var shells []string
		if prefix := os.Getenv("PREFIX"); prefix != "" {
			shells = append(shells, prefix+"/bin/bash", prefix+"/bin/sh")
		}
		return append(shells, "/system/bin/sh", "/bin/bash", "/bin/sh")
	}
	if runtime.GOOS == "windows" {
		return []string{os.Getenv("COMSPEC"), "cmd.exe"}
	}
	return []string{"/bin/bash", "/bin/sh"}
}

var errPtyClosed = errors.New("pty: closed")

// CloseInput closes only the input (stdin) side of the PTY.
// On Windows ConPTY, closing the input pipe signals the attached process that
// stdin is gone and helps the ConPTY host drain and close the output pipe,
// which in turn unblocks a blocked Read call. On other backends this is a no-op.
func (p *Process) CloseInput() {
	if atomic.LoadInt32(&p.closed) == 1 {
		return
	}
	if p.legacy != nil {
		// WinPTY: no separate input close; will be handled by full Close.
		return
	}
	// go-pty ConPTY: close the input pipe so Read can unblock.
	if cp, ok := p.pty.(interface{ InputPipe() *os.File }); ok {
		if in := cp.InputPipe(); in != nil {
			_ = in.Close()
		}
	}
}

// Read from the PTY (stdout)
func (p *Process) Read(b []byte) (int, error) {
	if atomic.LoadInt32(&p.closed) == 1 {
		return 0, errPtyClosed
	}
	if p.legacy != nil {
		return p.legacy.Read(b)
	}
	return p.pty.Read(b)
}

// Write to the PTY (stdin)
func (p *Process) Write(b []byte) (int, error) {
	if atomic.LoadInt32(&p.closed) == 1 {
		return 0, errPtyClosed
	}
	if p.legacy != nil {
		return p.legacy.Write(b)
	}
	return p.pty.Write(b)
}

// Resize the terminal window
func (p *Process) Resize(rows, cols uint16) error {
	if atomic.LoadInt32(&p.closed) == 1 {
		return errPtyClosed // avoid ResizePseudoConsole on freed handle
	}
	if p.legacy != nil {
		return p.legacy.Resize(rows, cols)
	}
	return p.pty.Resize(int(cols), int(rows))
}

// Close the PTY. Safe to call multiple times.
func (p *Process) Close() error {
	if !atomic.CompareAndSwapInt32(&p.closed, 0, 1) {
		return nil // already closed
	}
	if p.legacy != nil {
		return p.legacy.Close()
	}
	return p.pty.Close()
}

// Wait waits for the process to exit and returns the exit code.
func (p *Process) Wait() (int, error) {
	if p.legacy != nil {
		return p.legacy.Wait()
	}
	err := p.cmd.Wait()
	if err == nil {
		return 0, nil
	}
	// Try to extract exit code from the process state
	if p.cmd.ProcessState != nil {
		code := p.cmd.ProcessState.ExitCode()
		if code >= 0 {
			return code, nil
		}
	}
	return -1, err
}

// detectShell returns the appropriate shell for the current platform.
// Termux has no /bin/sh, so an Android host falls back through $PREFIX first.
func detectShell() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	if s := os.Getenv("COMSPEC"); s != "" {
		return s
	}
	if runtime.GOOS == "android" {
		if shell := shellInPrefix(os.Getenv("PREFIX")); shell != "" {
			return shell
		}
		if _, err := os.Stat("/system/bin/sh"); err == nil {
			return "/system/bin/sh"
		}
	}
	if _, err := os.Stat("/bin/bash"); err == nil {
		return "/bin/bash"
	}
	return "/bin/sh"
}

// shellInPrefix returns the first usable shell under a Termux-style prefix.
func shellInPrefix(prefix string) string {
	if prefix == "" {
		return ""
	}
	for _, candidate := range []string{prefix + "/bin/bash", prefix + "/bin/sh"} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

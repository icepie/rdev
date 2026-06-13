// Package ptyutil provides cross-platform PTY support using go-pty.
// On Unix it uses creack/pty; on Windows it uses ConPty (native pseudo-console).
// It also supports applying SSH terminal modes to the PTY.
package ptyutil

import (
	"fmt"
	"os"
	"runtime"

	"github.com/aymanbagabas/go-pty"
	"golang.org/x/crypto/ssh"
)

// Process represents a running process attached to a PTY.
type Process struct {
	pty  pty.Pty
	cmd  *pty.Cmd
	term string
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
func shellFlag(shell string) string {
	if runtime.GOOS == "windows" {
		return "/c"
	}
	return "-c"
}

// Start creates a new PTY process. On Windows this uses ConPty;
// on Unix this uses creack/pty. On unsupported platforms it returns an error.
func Start(cfg *Config) (*Process, error) {
	pseudo, err := pty.New()
	if err != nil {
		return nil, fmt.Errorf("pty new: %w", err)
	}

	shell := cfg.Shell
	if shell == "" {
		shell = detectShell()
	}

	flag := shellFlag(shell)

	var cmd *pty.Cmd
	if cfg.Command != "" {
		cmd = pseudo.Command(shell, flag, cfg.Command)
	} else {
		cmd = pseudo.Command(shell)
	}

	// Build environment
	env := os.Environ()
	env = append(env, cfg.Env...)
	if cfg.Term != "" {
		env = append(env, "TERM="+cfg.Term)
	} else {
		env = append(env, "TERM=xterm-256color")
	}
	cmd.Env = env

	// Resize to initial dimensions
	rows, cols := cfg.Rows, cfg.Cols
	if rows == 0 {
		rows = 24
	}
	if cols == 0 {
		cols = 80
	}
	if err := pseudo.Resize(int(cols), int(rows)); err != nil {
		// Non-fatal: some platforms may not support resize before start
		_ = err
	}

	// Apply SSH terminal modes if provided (Unix only, no-op on Windows)
	if cfg.Modes != nil {
		_ = pty.ApplyTerminalModes(int(pseudo.Fd()), int(cols), int(rows), cfg.Modes)
	}

	if err := cmd.Start(); err != nil {
		pseudo.Close()
		return nil, fmt.Errorf("pty start: %w", err)
	}

	return &Process{
		pty:  pseudo,
		cmd:  cmd,
		term: cfg.Term,
	}, nil
}

// Read from the PTY (stdout)
func (p *Process) Read(b []byte) (int, error) {
	return p.pty.Read(b)
}

// Write to the PTY (stdin)
func (p *Process) Write(b []byte) (int, error) {
	return p.pty.Write(b)
}

// Resize the terminal window
func (p *Process) Resize(rows, cols uint16) error {
	return p.pty.Resize(int(cols), int(rows))
}

// Close the PTY. Does NOT wait for the process.
func (p *Process) Close() error {
	return p.pty.Close()
}

// Wait waits for the process to exit and returns the exit code.
func (p *Process) Wait() (int, error) {
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

// Name returns the PTY name (e.g. /dev/pts/0 on Unix, "windows-pty" on Windows)
func (p *Process) Name() string {
	return p.pty.Name()
}

// detectShell returns the appropriate shell for the current platform
func detectShell() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	if s := os.Getenv("COMSPEC"); s != "" {
		return s
	}
	// Fallback
	if _, err := os.Stat("/bin/bash"); err == nil {
		return "/bin/bash"
	}
	return "/bin/sh"
}

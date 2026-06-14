//go:build windows && (amd64 || 386)

package ptyutil

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"

	winpty "github.com/iamacarpet/go-winpty"
	"golang.org/x/sys/windows"
	"rdev/internal/wincompat"
)

type readCloser struct {
	io.Reader
	io.Closer
}

type winPTYProcess struct {
	wp     *winpty.WinPTY
	stdin  io.WriteCloser
	stdout io.ReadCloser
	closed int32 // atomic: 1 = closed
}

func startWinPTY(cfg *Config) (*Process, error) {
	dir := findWinPTYDir()
	if dir == "" {
		return nil, fmt.Errorf("winpty runtime not found")
	}

	shell := cfg.Shell
	if shell == "" {
		shell = detectShell()
	}
	cmdline := quoteWindowsArg(shell)
	if cfg.Command != "" {
		cmdline += " " + shellFlag(shell) + " " + quoteWindowsArg(cfg.Command)
	}

	rows, cols := cfg.Rows, cfg.Cols
	if rows == 0 {
		rows = 24
	}
	if cols == 0 {
		cols = 80
	}

	opts := winpty.Options{
		DLLPrefix:   dir,
		Command:     cmdline,
		Dir:         currentDir(),
		Env:         append(os.Environ(), cfg.Env...),
		InitialCols: uint32(cols),
		InitialRows: uint32(rows),
	}
	wp, err := winpty.OpenWithOptions(opts)
	if err != nil {
		return nil, err
	}
	stdout := &readCloser{Reader: wincompat.DecodeOutput(wp.StdOut), Closer: wp.StdOut}
	return &Process{legacy: &winPTYProcess{wp: wp, stdin: wincompat.EncodeInput(wp.StdIn), stdout: stdout}, term: cfg.Term}, nil
}

func findWinPTYDir() string {
	candidates := []string{}
	if dir := os.Getenv("RDEV_WINPTY_DIR"); dir != "" {
		candidates = append(candidates, dir)
	}
	if exe, err := os.Executable(); err == nil {
		base := filepath.Dir(exe)
		candidates = append(candidates, filepath.Join(base, "winpty"), base)
	}
	if tmp := os.Getenv("TEMP"); tmp != "" {
		candidates = append(candidates, filepath.Join(tmp, "rdev-winpty"))
	}
	for _, dir := range candidates {
		if fileExists(filepath.Join(dir, "winpty.dll")) && fileExists(filepath.Join(dir, "winpty-agent.exe")) {
			return dir
		}
	}
	return ""
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func currentDir() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

func quoteWindowsArg(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t\"\\") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

func (p *winPTYProcess) Read(b []byte) (int, error) {
	if atomic.LoadInt32(&p.closed) == 1 {
		return 0, io.EOF
	}
	return p.stdout.Read(b)
}
func (p *winPTYProcess) Write(b []byte) (int, error) {
	if atomic.LoadInt32(&p.closed) == 1 {
		return 0, fmt.Errorf("winpty: closed")
	}
	return p.stdin.Write(b)
}
func (p *winPTYProcess) Resize(rows, cols uint16) error {
	if atomic.LoadInt32(&p.closed) == 1 {
		return fmt.Errorf("winpty: closed")
	}
	p.wp.SetSize(uint32(cols), uint32(rows))
	return nil
}
func (p *winPTYProcess) Close() error {
	if !atomic.CompareAndSwapInt32(&p.closed, 0, 1) {
		return nil
	}
	p.wp.Close()
	return nil
}
func (p *winPTYProcess) Wait() (int, error) {
	h := syscall.Handle(p.wp.GetProcHandle())
	_, err := windows.WaitForSingleObject(windows.Handle(h), windows.INFINITE)
	if err != nil {
		return -1, err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(windows.Handle(h), &code); err != nil {
		return -1, err
	}
	return int(code), nil
}

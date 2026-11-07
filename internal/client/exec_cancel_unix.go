//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package client

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// Each command owns a process group, so canceling a shell also stops its
// children without signaling another concurrent session.
func startExecCommand(cmd *exec.Cmd) (func(), error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return func() {}, cmd.Start()
}

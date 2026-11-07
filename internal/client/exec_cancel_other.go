//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly && !windows

package client

import "os/exec"

// CommandContext terminates the direct process on other platforms.
func startExecCommand(cmd *exec.Cmd) (func(), error) { return func() {}, cmd.Start() }

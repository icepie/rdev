//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package client

import "os/exec"

// CommandContext terminates the direct process on other platforms.
func configureExecCancellation(cmd *exec.Cmd) {}

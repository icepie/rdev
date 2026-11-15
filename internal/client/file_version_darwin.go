//go:build darwin

package client

import (
	"os"
	"syscall"
)

func fileVersionChangeTime(info os.FileInfo) (int64, int64) {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return int64(stat.Ctimespec.Sec), int64(stat.Ctimespec.Nsec)
	}
	return 0, 0
}

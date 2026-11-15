//go:build linux

package client

import (
	"os"
	"syscall"
)

func fileVersionChangeTime(info os.FileInfo) (int64, int64) {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return int64(stat.Ctim.Sec), int64(stat.Ctim.Nsec)
	}
	return 0, 0
}

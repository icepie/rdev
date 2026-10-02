//go:build (amd64 || arm64) && windows

package client

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// x264LibraryCandidates lists shared library names to probe, newest first.
func x264LibraryCandidates() []string {
	var candidates []string
	for version := 170; version >= 150; version-- {
		candidates = append(candidates, fmt.Sprintf("libx264-%d.dll", version))
	}
	candidates = append(candidates, "x264.dll", "libx264.dll")
	return candidates
}

func openX264Library(name string) (uintptr, bool) {
	handle, err := windows.LoadLibrary(name)
	if err != nil {
		return 0, false
	}
	return uintptr(handle), true
}

func lookupX264Symbol(handle uintptr, name string) (uintptr, error) {
	return windows.GetProcAddress(windows.Handle(handle), name)
}

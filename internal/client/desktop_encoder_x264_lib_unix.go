//go:build (amd64 || arm64) && !windows

package client

import (
	"fmt"

	"github.com/ebitengine/purego"
)

// x264LibraryCandidates lists shared library names to probe, newest first.
func x264LibraryCandidates() []string {
	var candidates []string
	for version := 170; version >= 150; version-- {
		candidates = append(candidates, fmt.Sprintf("libx264.so.%d", version))
	}
	candidates = append(candidates, "libx264.so")
	if len(candidates) == 0 {
		return nil
	}
	return candidates
}

func openX264Library(name string) (uintptr, bool) {
	handle, err := purego.Dlopen(name, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return 0, false
	}
	return handle, true
}

func lookupX264Symbol(handle uintptr, name string) (uintptr, error) {
	return purego.Dlsym(handle, name)
}

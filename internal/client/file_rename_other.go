//go:build !linux && !darwin && !windows

package client

import "fmt"

func renameFileNoReplace(from, to string) error {
	return fmt.Errorf("atomic no-replace rename is unsupported on this platform")
}

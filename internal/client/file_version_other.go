//go:build !linux && !darwin

package client

import "os"

// Other platforms still compare descriptor identity, size and modification
// time; every consumer verifies the full content hash before publication.
func fileVersionChangeTime(os.FileInfo) (int64, int64) { return 0, 0 }

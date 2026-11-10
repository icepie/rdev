package client

import "golang.org/x/sys/unix"

func renameFileNoReplace(from, to string) error {
	return unix.RenamexNp(from, to, unix.RENAME_EXCL)
}

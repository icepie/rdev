package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func validFileSHA256(value string) bool {
	if len(value) != sha256.Size*2 { return false }
	for _, ch := range value {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') { return false }
	}
	return true
}


type cancellableFileReader struct {
	reader io.Reader
	cancel <-chan struct{}
	interrupt <-chan struct{}
}

func (r cancellableFileReader) Read(p []byte) (int, error) {
	select { case <-r.cancel: return 0, context.Canceled; case <-r.interrupt: return 0, context.Canceled; default: }
	return r.reader.Read(p)
}

// SectionReader leaves the transfer offset untouched and bounds memory.
func filePrefixSHA256Cancelable(file *os.File, size int64, cancel, interrupt <-chan struct{}) (string, error) {
	h := sha256.New()
	n, err := io.Copy(h, cancellableFileReader{io.NewSectionReader(file, 0, size), cancel, interrupt})
	if err != nil { return "", err }
	if n != size { return "", io.ErrUnexpectedEOF }
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Write the binding only after truncating an unrelated partial. A crash can lose
// resumability, never make bytes from an older source eligible for publication.
func bindUploadPartial(up *managedUpload, restart bool) error {
	identity := fmt.Sprintf("%s\n%d\n", up.sha256, up.size)
	metadata := up.partPath + ".meta"
	var previous []byte
	meta, err := os.Open(metadata)
	if err == nil {
		previous, err = io.ReadAll(io.LimitReader(meta, 128))
		meta.Close()
	}
	if err != nil && !os.IsNotExist(err) { return err }
	resume := !restart && err == nil && string(previous) == identity
	st, err := up.file.Stat()
	if err != nil { return err }
	if !st.Mode().IsRegular() { return fmt.Errorf("upload partial is not a regular file") }
	if !resume || st.Size() > up.size {
		if err := up.file.Truncate(0); err != nil { return err }
		if err := up.file.Sync(); err != nil { return err }
		up.offset = 0
	} else {
		up.offset = st.Size()
	}
	if resume { return nil }
	tmp, err := os.CreateTemp(filepath.Dir(metadata), ".rdev-upload-identity-*")
	if err != nil { return err }
	defer os.Remove(tmp.Name())
	_, err = tmp.WriteString(identity)
	if err == nil { err = tmp.Sync() }
	if closeErr := tmp.Close(); err == nil { err = closeErr }
	if err != nil { return err }
	return os.Rename(tmp.Name(), metadata)
}

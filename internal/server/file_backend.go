package server

import (
	"context"
	"io"
	"os"
	"time"

	"rdev/internal/protocol"
)

type fileBackend interface {
	ID() string
	Label() string
	List(ctx context.Context, path string) ([]protocol.FileEntry, string, string, bool, error)
	OpenUpload(ctx context.Context, path, parentPath, name string, size int64, modTime string) (backendUpload, error)
	DownloadURL(ctx context.Context, path string) (backendDownloadInfo, string, error)
	Download(ctx context.Context, path string, offset int64, w io.Writer) (backendDownloadInfo, error)
	Stat(ctx context.Context, path string) (os.FileInfo, error)
	ReadFile(ctx context.Context, path string) (io.ReaderAt, os.FileInfo, func() error, error)
	WriteFile(ctx context.Context, path string) (io.WriterAt, func() error, error)
	Mkdir(ctx context.Context, path string) error
	Remove(ctx context.Context, path string) error
	Rename(ctx context.Context, oldPath, newPath string) error
}

type transferUploadBackend interface {
	OpenTransferUpload(ctx context.Context, taskID, name string, size int64, modTime string) (backendUpload, error)
}

type absolutePathBackend interface {
	DownloadURLAbsolute(ctx context.Context, panPath string) (backendDownloadInfo, string, error)
}

type backendUpload interface {
	Path() string
	Offset() int64
	WriteAt(p []byte, off int64) (int, error)
	Commit(ctx context.Context) (int64, error)
	Cancel() error
}

type backendDownloadInfo struct {
	Path    string
	Name    string
	Size    int64
	Offset  int64
	ModTime time.Time
}

func backendHasPassword(backend fileBackend) bool {
	if b, ok := backend.(*AliyunPanBackend); ok {
		return b.Password() != ""
	}
	return false
}

func (s *Server) backendByID(id string) fileBackend {
	if s.FileBackend != nil && id == s.FileBackend.ID() {
		return s.FileBackend
	}
	return nil
}

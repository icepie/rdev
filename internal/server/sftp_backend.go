package server

import (
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/pkg/sftp"
)

type backendSFTPHandler struct {
	backend fileBackend
}

func (h backendSFTPHandler) Fileread(req *sftp.Request) (io.ReaderAt, error) {
	reader, _, closeFn, err := h.backend.ReadFile(req.Context(), req.Filepath)
	if err != nil {
		return nil, err
	}
	return &readerAtCloser{ReaderAt: reader, closeFn: closeFn}, nil
}

func (h backendSFTPHandler) Filewrite(req *sftp.Request) (io.WriterAt, error) {
	writer, closeFn, err := h.backend.WriteFile(req.Context(), req.Filepath)
	if err != nil {
		return nil, err
	}
	return &writerAtCloser{WriterAt: writer, closeFn: closeFn}, nil
}

func (h backendSFTPHandler) Filecmd(req *sftp.Request) error {
	switch strings.ToLower(req.Method) {
	case "mkdir":
		return h.backend.Mkdir(req.Context(), req.Filepath)
	case "remove", "rmdir":
		return h.backend.Remove(req.Context(), req.Filepath)
	case "rename":
		return h.backend.Rename(req.Context(), req.Filepath, req.Target)
	case "setstat":
		return nil
	default:
		return errors.New("unsupported operation")
	}
}

func (h backendSFTPHandler) Filelist(req *sftp.Request) (sftp.ListerAt, error) {
	switch strings.ToLower(req.Method) {
	case "stat":
		info, err := h.backend.Stat(req.Context(), req.Filepath)
		if err != nil {
			return nil, err
		}
		return fileInfoLister{items: []os.FileInfo{info}}, nil
	case "list":
		entries, _, _, _, err := h.backend.List(req.Context(), req.Filepath)
		if err != nil {
			return nil, err
		}
		infos := make([]os.FileInfo, 0, len(entries))
		for _, entry := range entries {
			mode := os.FileMode(0644)
			if entry.IsDir {
				mode = os.ModeDir | 0755
			}
			infos = append(infos, sftpFileInfo{name: entry.Name, size: entry.Size, mode: mode, modTime: parsePanTime(entry.ModTime)})
		}
		return fileInfoLister{items: infos}, nil
	case "readlink":
		return nil, errors.New("readlink is not supported")
	default:
		return nil, errors.New("unsupported operation")
	}
}

func serveBackendSFTP(rwc io.ReadWriteCloser, backend fileBackend) error {
	handler := backendSFTPHandler{backend: backend}
	server := sftp.NewRequestServer(rwc, sftp.Handlers{FileGet: handler, FilePut: handler, FileCmd: handler, FileList: handler})
	defer server.Close()
	return server.Serve()
}

type readerAtCloser struct {
	io.ReaderAt
	closeFn func() error
}

func (r *readerAtCloser) Close() error {
	if r.closeFn != nil {
		return r.closeFn()
	}
	return nil
}

type writerAtCloser struct {
	io.WriterAt
	closeFn func() error
}

func (w *writerAtCloser) Close() error {
	if w.closeFn != nil {
		return w.closeFn()
	}
	return nil
}

type fileInfoLister struct {
	items []os.FileInfo
}

func (l fileInfoLister) ListAt(dst []os.FileInfo, offset int64) (int, error) {
	if offset >= int64(len(l.items)) {
		return 0, io.EOF
	}
	n := copy(dst, l.items[offset:])
	if n < len(dst) {
		return n, io.EOF
	}
	return n, nil
}

type sftpFileInfo struct {
	name    string
	size    int64
	mode    os.FileMode
	modTime time.Time
}

func (s sftpFileInfo) Name() string       { return s.name }
func (s sftpFileInfo) Size() int64        { return s.size }
func (s sftpFileInfo) Mode() os.FileMode  { return s.mode }
func (s sftpFileInfo) ModTime() time.Time { return s.modTime }
func (s sftpFileInfo) IsDir() bool        { return s.mode.IsDir() }
func (s sftpFileInfo) Sys() any           { return nil }

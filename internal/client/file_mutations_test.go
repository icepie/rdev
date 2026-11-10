package client

import (
	"os"
	"path/filepath"
	"rdev/internal/protocol"
	"testing"
)

func TestFileMutations(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a")
	b := filepath.Join(root, "b")
	os.WriteFile(a, []byte("keep"), 0600)
	os.WriteFile(b, []byte("existing"), 0600)
	request := &protocol.Message{Type: protocol.MsgFileRename, RequestID: "r", Data: a, FilePath: b}
	if mutateFileEntry(request).Success {
		t.Fatal("overwrote existing file")
	}
	os.Remove(b)
	response := mutateFileEntry(request)
	if !response.Success || response.RequestID != "r" {
		t.Fatal(response.Error)
	}
	if data, err := os.ReadFile(b); err != nil || string(data) != "keep" {
		t.Fatal("rename corrupted data")
	}
	nested := filepath.Join(root, "nested")
	os.Mkdir(nested, 0700)
	os.WriteFile(filepath.Join(nested, "file"), nil, 0600)
	request = &protocol.Message{Type: protocol.MsgFileDelete, Path: nested}
	if mutateFileEntry(request).Success {
		t.Fatal("deleted nonempty directory without recursive flag")
	}
	request.Success = true
	if !mutateFileEntry(request).Success {
		t.Fatal("recursive deletion failed")
	}
	request.Path = string(filepath.Separator)
	if mutateFileEntry(request).Success {
		t.Fatal("allowed filesystem root deletion")
	}
}

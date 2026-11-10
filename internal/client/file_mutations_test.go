package client

import (
	"fmt"
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

func TestFileCreateAndRenameNeverReplaceConcurrentDestination(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "winner")
	results := make(chan *protocol.Message, 16)
	for i := 0; i < 16; i++ {
		go func() { results <- mutateFileEntry(&protocol.Message{Type: protocol.MsgFileCreate, Path: target}) }()
	}
	winners := 0
	for i := 0; i < 16; i++ {
		if (<-results).Success {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("exclusive create winners = %d", winners)
	}
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 16; i++ {
		source := filepath.Join(root, fmt.Sprint(i))
		if err := os.WriteFile(source, []byte("replace"), 0600); err != nil {
			t.Fatal(err)
		}
		go func() {
			results <- mutateFileEntry(&protocol.Message{Type: protocol.MsgFileRename, Data: source, FilePath: target})
		}()
	}
	for i := 0; i < 16; i++ {
		if (<-results).Success {
			t.Fatal("replaced destination")
		}
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "keep" {
		t.Fatal("destination changed", err)
	}
}

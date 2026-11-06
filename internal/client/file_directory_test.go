package client

import (
	"os"
	"path/filepath"
	"rdev/internal/protocol"
	"testing"
)

func TestCreateFileDirectory(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested", "empty")
	for range 2 {
		response := createFileDirectory(&protocol.Message{RequestID: "mkdir-1", Path: nested})
		if !response.Success || response.Type != protocol.MsgFileMkdirResult || response.RequestID != "mkdir-1" || response.Path != nested {
			t.Fatalf("unexpected response: %+v", response)
		}
	}
	info, err := os.Stat(nested)
	if err != nil || !info.IsDir() {
		t.Fatalf("directory missing: %v", err)
	}
	file := filepath.Join(root, "existing-file")
	if err := os.WriteFile(file, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", file, filepath.Join(file, "child")} {
		response := createFileDirectory(&protocol.Message{RequestID: "failed", Path: path})
		if response.Success || response.Error == "" || response.RequestID != "failed" {
			t.Fatalf("unexpected response: %+v", response)
		}
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "keep" {
		t.Fatalf("existing file changed: %q %v", data, err)
	}
}

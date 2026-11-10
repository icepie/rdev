package client

import (
	"fmt"
	"os"
	"path/filepath"
	"rdev/internal/protocol"
	"strings"
)

func mutateFileEntry(msg *protocol.Message) *protocol.Message {
	response := &protocol.Message{Type: protocol.MessageType(string(msg.Type) + "_result"), RequestID: msg.RequestID, Path: msg.Path}
	err := func() error {
		if msg.Type == protocol.MsgFileRename {
			if strings.TrimSpace(msg.Data) == "" || strings.TrimSpace(msg.FilePath) == "" {
				return fmt.Errorf("source and destination are required")
			}
			source, err := filepath.Abs(msg.Data)
			if err != nil {
				return err
			}
			dest, err := filepath.Abs(msg.FilePath)
			if err != nil {
				return err
			}
			if filepath.Dir(source) == source || filepath.Dir(dest) == dest {
				return fmt.Errorf("cannot rename a filesystem root")
			}
			if _, err := os.Lstat(dest); err == nil {
				return fmt.Errorf("destination already exists")
			} else if !os.IsNotExist(err) {
				return err
			}
			return renameFileNoReplace(source, dest)
		}
		if msg.Type == protocol.MsgFileCreate {
			if strings.TrimSpace(msg.Path) == "" {
				return fmt.Errorf("path is required")
			}
			f, err := os.OpenFile(msg.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
			if err != nil {
				return err
			}
			return f.Close()
		}
		if strings.TrimSpace(msg.Path) == "" {
			return fmt.Errorf("path is required")
		}
		path, err := filepath.Abs(msg.Path)
		if err != nil {
			return err
		}
		if filepath.Dir(path) == path {
			return fmt.Errorf("cannot delete a filesystem root")
		}
		if _, err := os.Lstat(path); err != nil {
			return err
		}
		if msg.Success {
			return os.RemoveAll(path)
		}
		return os.Remove(path)
	}()
	if err != nil {
		response.Error = err.Error()
	} else {
		response.Success = true
	}
	return response
}

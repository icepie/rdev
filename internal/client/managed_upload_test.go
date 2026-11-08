package client

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"rdev/internal/protocol"
)

type uploadTestTransport struct{ messages chan protocol.Message }

func (t *uploadTestTransport) WriteJSON(raw []byte) error {
	var m protocol.Message
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	t.messages <- m
	return nil
}
func (t *uploadTestTransport) WriteBinary([]byte) error { return nil }
func (t *uploadTestTransport) WritePing([]byte) error   { return nil }
func (t *uploadTestTransport) Close(string) error       { return nil }
func newUploadTestClient() (*Client, *uploadTestTransport) {
	tr := &uploadTestTransport{make(chan protocol.Message, 128)}
	c := NewClient("", "upload-test", "", "")
	c.transport = tr
	return c, tr
}
func uploadReply(t *testing.T, tr *uploadTestTransport) protocol.Message {
	t.Helper()
	select {
	case m := <-tr.messages:
		return m
	case <-time.After(time.Second):
		t.Fatal("missing upload reply")
		return protocol.Message{}
	}
}

func TestManagedUploadDestinationOwnership(t *testing.T) {
	c, tr := newUploadTestClient()
	path := filepath.Join(t.TempDir(), "file.txt")
	var wg sync.WaitGroup
	for _, id := range []string{"one", "two"} {
		wg.Go(func() { c.handleManagedUploadStart(&protocol.Message{TaskID: id, Path: path, Size: 4}) })
	}
	wg.Wait()
	replies := []protocol.Message{uploadReply(t, tr), uploadReply(t, tr)}
	winner := ""
	for _, m := range replies {
		if m.Type == protocol.MsgFileUploadReady {
			if winner != "" {
				t.Fatal("two owners")
			}
			winner = m.TaskID
		} else if m.Type != protocol.MsgFileTransferError {
			t.Fatal(m)
		}
	}
	if winner == "" {
		t.Fatal("no owner")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("destination changed before commit")
	}
	c.handleManagedUploadChunk(winner, 0, []byte("data"))
	c.handleManagedUploadEnd(&protocol.Message{TaskID: winner})
	if m := uploadReply(t, tr); m.Type != protocol.MsgFileTransferEnd || !m.Success {
		t.Fatal(m)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "data" {
		t.Fatal(string(data), err)
	}
	if len(c.uploads) != 0 || len(c.uploadPaths) != 0 {
		t.Fatal("destination ownership leaked")
	}
	c.handleManagedUploadStart(&protocol.Message{TaskID: "next", Path: path, Size: 3})
	if m := uploadReply(t, tr); m.Type != protocol.MsgFileUploadReady {
		t.Fatal(m)
	}
	c.handleManagedTransferCancel("next")
}

func TestManagedUploadAliasesAndResumeAfterDisconnect(t *testing.T) {
	c, tr := newUploadTestClient()
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Skip(err)
	}
	path := filepath.Join(real, "file.txt")
	c.handleManagedUploadStart(&protocol.Message{TaskID: "owner", Path: path, Size: 6})
	uploadReply(t, tr)
	c.handleManagedUploadChunk("owner", 0, []byte("abc"))
	c.handleManagedUploadStart(&protocol.Message{TaskID: "other", Path: filepath.Join(alias, "file.txt"), Size: 6})
	if m := uploadReply(t, tr); m.Type != protocol.MsgFileTransferError {
		t.Fatal("alias bypassed ownership", m)
	}
	left, right := net.Pipe()
	defer right.Close()
	c.closeCurrentTransport(tr, left)
	if len(c.uploads) != 0 || len(c.uploadPaths) != 0 {
		t.Fatal("disconnect leaked ownership")
	}
	c.mu.Lock()
	c.transport = tr
	c.mu.Unlock()
	c.handleManagedUploadStart(&protocol.Message{TaskID: "resume", Path: path, Size: 6})
	if m := uploadReply(t, tr); m.Type != protocol.MsgFileUploadReady || m.Offset != 3 {
		t.Fatal("resume lost partial data", m)
	}
	c.handleManagedUploadChunk("resume", 3, []byte("def"))
	c.handleManagedUploadEnd(&protocol.Message{TaskID: "resume"})
	uploadReply(t, tr)
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "abcdef" {
		t.Fatal(string(data), err)
	}
}

func TestManagedUploadsDifferentDestinationsDoNotShareIOLock(t *testing.T) {
	c, tr := newUploadTestClient()
	dir := t.TempDir()
	c.handleManagedUploadStart(&protocol.Message{TaskID: "one", Path: filepath.Join(dir, "a"), Size: 1})
	uploadReply(t, tr)
	first := c.managedUploadFor("one")
	first.mu.Lock()
	done := make(chan struct{})
	go func() {
		c.handleManagedUploadStart(&protocol.Message{TaskID: "two", Path: filepath.Join(dir, "b"), Size: 1})
		c.handleManagedUploadChunk("two", 0, []byte("b"))
		c.handleManagedUploadEnd(&protocol.Message{TaskID: "two"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		first.mu.Unlock()
		t.Fatal("unrelated upload blocked by first file")
	}
	first.mu.Unlock()
	c.handleManagedTransferCancel("one")
}

func TestManagedUploadFinishCancelWriteRace(t *testing.T) {
	for i := 0; i < 30; i++ {
		c, _ := newUploadTestClient()
		path := filepath.Join(t.TempDir(), "race.bin")
		data := bytes.Repeat([]byte("x"), 8192)
		c.handleManagedUploadStart(&protocol.Message{TaskID: "race", Path: path, Size: int64(len(data))})
		var wg sync.WaitGroup
		wg.Go(func() { c.handleManagedUploadChunk("race", 0, data) })
		wg.Go(func() { c.handleManagedUploadEnd(&protocol.Message{TaskID: "race"}) })
		wg.Go(func() { c.handleManagedTransferCancel("race") })
		wg.Wait()
		if len(c.uploads) != 0 || len(c.uploadPaths) != 0 {
			t.Fatal("terminal race leaked task")
		}
		got, err := os.ReadFile(path)
		if err == nil && !bytes.Equal(got, data) {
			t.Fatal("partial file committed")
		}
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
}

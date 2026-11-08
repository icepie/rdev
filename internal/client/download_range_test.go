package client

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"rdev/internal/protocol"
)

type downloadRecordingTransport struct {
	messages   []protocol.Message
	frames     [][]byte
	afterChunk func()
}

func (t *downloadRecordingTransport) WriteJSON(data []byte) error {
	var msg protocol.Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return err
	}
	t.messages = append(t.messages, msg)
	return nil
}
func (t *downloadRecordingTransport) WriteBinary(data []byte) error {
	t.frames = append(t.frames, bytes.Clone(data))
	if data[0] == protocol.BinFileDownloadChunk && t.afterChunk != nil {
		t.afterChunk()
	}
	return nil
}
func (t *downloadRecordingTransport) WritePing([]byte) error { return nil }
func (t *downloadRecordingTransport) Close(string) error     { return nil }

func TestManagedDownloadRanges(t *testing.T) {
	content := bytes.Repeat([]byte("0123456789"), 120000)
	path := filepath.Join(t.TempDir(), "video.bin")
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name           string
		offset, length int64
		start, end     int
		invalid        bool
	}{
		{"middle across chunks", 77, 700000, 77, 700077, false},
		{"suffix clamped at EOF", 1199993, 100, 1199993, 1200000, false},
		{"legacy resume", 1199993, 0, 1199993, 1200000, false},
		{"legacy full", 0, 0, 0, 1200000, false},
		{"empty at EOF", 1200000, 100, 1200000, 1200000, false},
		{"invalid negative offset", -1, 5, 0, 0, true},
		{"invalid past EOF", 1200001, 5, 0, 0, true},
		{"invalid length", 0, -1, 0, 0, true},
		{"overflow safe clamp", 5, 1<<63 - 1, 5, 1200000, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := &downloadRecordingTransport{}
			c := NewClient("", "range-test", "", "")
			c.transport = tr
			c.handleManagedDownloadStart(&protocol.Message{TaskID: "range", Path: path, Offset: tc.offset, Length: tc.length})
			if tc.invalid {
				if len(tr.messages) != 1 || tr.messages[0].Type != protocol.MsgFileTransferError || len(tr.frames) != 0 {
					t.Fatalf("invalid range sent data: %+v", tr.messages)
				}
				return
			}
			if len(tr.messages) != 2 || tr.messages[0].Type != protocol.MsgFileDownloadStart || tr.messages[1].Type != protocol.MsgFileTransferEnd || !tr.messages[1].Success {
				t.Fatalf("messages: %+v", tr.messages)
			}
			meta := tr.messages[0]
			if meta.Size != int64(len(content)) || meta.Length != int64(tc.end-tc.start) {
				t.Fatalf("metadata: %+v", meta)
			}
			cursor := int64(tc.start)
			var output []byte
			for i, raw := range tr.frames {
				typ, id, offset, payload, err := protocol.DecodeBinFrameOffset(raw)
				if err != nil || id != "range" || offset != cursor {
					t.Fatalf("frame %d: %q %d %v", i, id, offset, err)
				}
				if typ == protocol.BinFileDownloadChunk {
					output = append(output, payload...)
					cursor += int64(len(payload))
				} else if typ != protocol.BinFileTransferEnd || i != len(tr.frames)-1 {
					t.Fatalf("unexpected frame %d", typ)
				}
			}
			if !bytes.Equal(output, content[tc.start:tc.end]) || cursor != int64(tc.end) || tr.messages[1].Offset != cursor {
				t.Fatalf("wrong range: got %d bytes end %d", len(output), cursor)
			}
			if len(c.downloads) != 0 {
				t.Fatal("leaked download")
			}
		})
	}
}

func TestManagedDownloadRangeCancelAndTruncation(t *testing.T) {
	for _, cancel := range []bool{true, false} {
		t.Run(map[bool]string{true: "cancel", false: "truncate"}[cancel], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "large.bin")
			if err := os.WriteFile(path, bytes.Repeat([]byte("x"), 1024*1024), 0600); err != nil {
				t.Fatal(err)
			}
			tr := &downloadRecordingTransport{}
			c := NewClient("", "range-test", "", "")
			c.transport = tr
			tr.afterChunk = func() {
				if cancel {
					c.handleManagedTransferCancel("range")
				} else if err := os.Truncate(path, 512*1024); err != nil {
					t.Fatal(err)
				}
			}
			c.handleManagedDownloadStart(&protocol.Message{TaskID: "range", Path: path, Length: 1024 * 1024})
			for _, m := range tr.messages {
				if m.Type == protocol.MsgFileTransferEnd {
					t.Fatal("incomplete range reported success")
				}
			}
			if !cancel && tr.messages[len(tr.messages)-1].Type != protocol.MsgFileTransferError {
				t.Fatal("missing truncation error")
			}
			if len(tr.frames) != 1 || len(c.downloads) != 0 {
				t.Fatal("sent after cancellation/truncation or leaked task")
			}
		})
	}
}

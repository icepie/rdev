package server

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"rdev/internal/protocol"
)

func TestAgentUploadVerifiesResumedPrefixAndCompletion(t *testing.T) {
	for _, mismatch := range []string{"", "prefix", "completion"} {
		t.Run(mismatch, func(t *testing.T) {
			srv := NewServer()
			content := []byte("bbbbbbbb")
			digest := fmt.Sprintf("%x", sha256.Sum256(content))
			prefix := fmt.Sprintf("%x", sha256.Sum256(content[:4]))
			device := &ClientConn{ID: "device"}
			device.Transport = &fakeDeviceTransport{onSend: func(msg *protocol.Message) {
				switch msg.Type {
				case protocol.MsgFileUploadStart:
					if msg.SHA256 != digest { t.Errorf("missing source digest: %+v", msg) }
					if mismatch == "prefix" { prefix = fmt.Sprintf("%x", sha256.Sum256([]byte("OLD!"))) }
					srv.routeAgentFileMsg(&protocol.Message{Type: protocol.MsgFileUploadReady, TaskID: msg.TaskID, Size: 8, Offset: 4, SHA256: digest, PrefixSHA256: prefix})
				case protocol.MsgFileUploadEnd:
					actual := digest
					if mismatch == "completion" { actual = strings.Repeat("0", 64) }
					srv.routeAgentFileMsg(&protocol.Message{Type: protocol.MsgFileTransferEnd, TaskID: msg.TaskID, Size: 8, SHA256: actual, Success: true})
				}
			}}
			tool := &writeFileTool{srv: srv, client: device}
			_, err := tool.upload(context.Background(), "/target", content, 0)
			if (err != nil) != (mismatch != "") { t.Fatalf("mismatch=%q err=%v", mismatch, err) }
		})
	}
}

func TestAgentDownloadRejectsCorruptBytesAndDishonestBounds(t *testing.T) {
	for _, payload := range []string{"bbbb", "OLD!", "oversized"} {
		t.Run(payload, func(t *testing.T) {
			srv := NewServer()
			digest := fmt.Sprintf("%x", sha256.Sum256([]byte("bbbb")))
			device := &ClientConn{ID: "device"}
			device.Transport = &fakeDeviceTransport{onSend: func(msg *protocol.Message) {
				if msg.Type != protocol.MsgFileDownloadStart { return }
				srv.routeAgentFileMsg(&protocol.Message{Type: protocol.MsgFileDownloadStart, TaskID: msg.TaskID, Size: 4, SHA256: digest})
				srv.routeAgentFileBinary(protocol.EncodeBinFrameOffset(protocol.BinFileDownloadChunk, msg.TaskID, 0, []byte(payload)))
				srv.routeAgentFileMsg(&protocol.Message{Type: protocol.MsgFileTransferEnd, TaskID: msg.TaskID, Size: 4, SHA256: digest, Success: true})
			}}
			tool := &readFileTool{srv: srv, client: device}
			_, err := tool.download(context.Background(), "/source", 4)
			if (err != nil) != (payload != "bbbb") { t.Fatalf("payload=%q err=%v", payload, err) }
		})
	}
}

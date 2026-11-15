package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"rdev/internal/protocol"
)

func TestRTCICEServersLegacyAndHostOnly(t *testing.T) {
	if defaults := rtcICEServers(nil); len(defaults) != 1 || len(defaults[0].URLs) != 1 || defaults[0].URLs[0] != "stun:stun.l.google.com:19302" {
		t.Fatal("legacy peer lost default STUN")
	}
	empty := []protocol.IceServer{}
	if configured := rtcICEServers(&empty); configured == nil || len(configured) != 0 {
		t.Fatal("explicit empty configuration lost host-only mode")
	}
	servers := []protocol.IceServer{{URLs: []string{"turn:relay.example:3478"}, Username: "alice", Credential: "private-token"}}
	configured := rtcICEServers(&servers)
	if len(configured) != 1 || configured[0].URLs[0] != "turn:relay.example:3478" || configured[0].Username != "alice" || configured[0].Credential != "private-token" {
		t.Fatal("peer did not use authoritative TURN configuration")
	}
}

func TestFileRTCUploadFramesCannotAddressOtherOperations(t *testing.T) {
	r := &fileRTCSession{start: protocol.Message{Type: protocol.MsgFileUploadStart, TaskID: "private:owned"}}
	for _, tc := range []struct {
		name        string
		frame       []byte
		text, valid bool
	}{
		{"chunk", protocol.EncodeBinFrameOffset(protocol.BinFileUploadChunk, "", 9, []byte("bytes")), false, true},
		{"local-task", protocol.EncodeBinFrameOffset(protocol.BinFileUploadChunk, "owned", 9, []byte("bytes")), false, true},
		{"foreign-namespace", protocol.EncodeBinFrameOffset(protocol.BinFileUploadChunk, "other:owned", 0, nil), false, false},
		{"foreign-task", protocol.EncodeBinFrameOffset(protocol.BinFileUploadChunk, "other", 0, nil), false, false},
		{"finish", protocol.EncodeBinFrameOffset(protocol.BinFileTransferEnd, "", 0, nil), false, false},
		{"cancel", protocol.EncodeBinFrameOffset(protocol.BinFileTransferCancel, "", 0, nil), false, false},
		{"arbitrary-operation", []byte(`{"op":"delete","path":"/tmp/file"}`), true, false},
		{"short-frame", []byte{protocol.BinFileUploadChunk, 0}, false, false},
		{"negative-offset", protocol.EncodeBinFrameOffset(protocol.BinFileUploadChunk, "", -1, nil), false, false},
		{"oversize", protocol.EncodeBinFrameOffset(protocol.BinFileUploadChunk, "", 0, make([]byte, fileRTCChunkSize+1)), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, valid := r.uploadFrame(webrtc.DataChannelMessage{Data: tc.frame, IsString: tc.text})
			if valid != tc.valid {
				t.Fatalf("frame acceptance = %v, want %v", valid, tc.valid)
			}
		})
	}
	r.start.Type = protocol.MsgFileDownloadStart
	if _, _, ok := r.uploadFrame(webrtc.DataChannelMessage{Data: protocol.EncodeBinFrameOffset(protocol.BinFileUploadChunk, "", 0, nil)}); ok {
		t.Fatal("download peer accepted an upload")
	}
}

func TestFileRTCRangeAckIsNegotiatedAndBounded(t *testing.T) {
	if !rtcRangeAckOffered(json.RawMessage(`{"type":"offer","rangeAck":true}`)) {
		t.Fatal("range ACK offer was not recognized")
	}
	for _, raw := range []json.RawMessage{nil, json.RawMessage(`{"type":"offer"}`), json.RawMessage(`{"type":"offer","rangeAck":false}`)} {
		if rtcRangeAckOffered(raw) {
			t.Fatalf("unexpected range ACK negotiation for %s", raw)
		}
	}
	r := &fileRTCSession{rangeAck: true, awaitingAck: true, expectedAck: 16, ack: make(chan int64, 1)}
	valid := protocol.EncodeBinFrameOffset(fileRTCRangeAck, "", 16, nil)
	for _, tc := range []struct {
		name string
		msg  webrtc.DataChannelMessage
	}{
		{"wrong-offset", webrtc.DataChannelMessage{Data: protocol.EncodeBinFrameOffset(fileRTCRangeAck, "", 15, nil)}},
		{"wrong-type", webrtc.DataChannelMessage{Data: protocol.EncodeBinFrameOffset(protocol.BinFileTransferCancel, "", 16, nil)}},
		{"task-id", webrtc.DataChannelMessage{Data: protocol.EncodeBinFrameOffset(fileRTCRangeAck, "foreign", 16, nil)}},
		{"payload", webrtc.DataChannelMessage{Data: protocol.EncodeBinFrameOffset(fileRTCRangeAck, "", 16, []byte{1})}},
		{"text", webrtc.DataChannelMessage{Data: valid, IsString: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if r.downloadRangeAck(tc.msg) {
				t.Fatal("invalid range acknowledgement accepted")
			}
		})
	}
	if !r.downloadRangeAck(webrtc.DataChannelMessage{Data: valid}) {
		t.Fatal("valid range acknowledgement rejected")
	}
	if offset := <-r.ack; offset != 16 {
		t.Fatalf("acknowledged offset = %d, want 16", offset)
	}
	if r.downloadRangeAck(webrtc.DataChannelMessage{Data: valid}) {
		t.Fatal("duplicate range acknowledgement accepted")
	}
}

func TestFileRTCRangeAckWaitUnblocksOnCancel(t *testing.T) {
	done := make(chan struct{})
	close(done)
	r := &fileRTCSession{done: done, ack: make(chan int64, 1)}
	if err := r.waitRangeAck(16); err == nil {
		t.Fatal("range acknowledgement wait ignored cancellation")
	}
}

type fileRTCNoRelayTransport struct {
	uploadTestTransport
	binary atomic.Int64
}

func (t *fileRTCNoRelayTransport) WriteBinary([]byte) error { t.binary.Add(1); return nil }

func TestFileRTCFailedDirectSendNeverFallsBackPerFrame(t *testing.T) {
	transport := &fileRTCNoRelayTransport{uploadTestTransport: uploadTestTransport{messages: make(chan protocol.Message, 8)}}
	c := NewClient("", "test", "", "")
	c.transport = transport
	r := &fileRTCSession{client: c, done: make(chan struct{})}
	for _, typ := range []byte{protocol.BinFileUploadAck, protocol.BinFileDownloadChunk, protocol.BinFileTransferEnd} {
		if err := c.sendFileBinary(r, typ, "task", 0, []byte("payload")); err == nil {
			t.Fatal("missing direct channel reported success")
		}
	}
	if transport.binary.Load() != 0 {
		t.Fatal("failed direct data silently used relay")
	}
	if err := c.sendFileBinary(nil, protocol.BinFileUploadAck, "legacy", 9, nil); err != nil || transport.binary.Load() != 1 {
		t.Fatal("legacy relay was disabled")
	}
}

func TestFileRTCInvalidOfferDoesNotStartManagedUpload(t *testing.T) {
	c, transport := newUploadTestClient()
	path := filepath.Join(t.TempDir(), "not-created")
	c.startFileTransfer(&protocol.Message{Type: protocol.MsgFileUploadStart, TaskID: "offer", Path: path, Size: 3, SHA256: transferDigest("abc"), RTCType: "offer", RTCData: json.RawMessage(`{"type":"answer","sdp":"invalid"}`)})
	m := uploadReply(t, transport)
	if m.Type != protocol.MsgFileRTCSignal || m.RTCType != "close" {
		t.Fatalf("invalid offer reply: %+v", m)
	}
	if c.managedUploadFor("offer") != nil {
		t.Fatal("upload reserved before channel opened")
	}
	if _, err := os.Stat(path + ".rdevpart"); !os.IsNotExist(err) {
		t.Fatalf("negotiation touched destination: %v", err)
	}
}

func TestFileRTCEarlyCandidatesBoundedAndCancelPreventsLateStart(t *testing.T) {
	c, transport := newUploadTestClient()
	r := &fileRTCSession{client: c, start: protocol.Message{Type: protocol.MsgFileUploadStart, TaskID: "task"}, transport: c.transport, done: make(chan struct{}), wake: make(chan struct{}, 1)}
	c.fileRTC = map[string]*fileRTCSession{"task": r}
	for range 129 {
		c.handleFileRTCSignal(&protocol.Message{TaskID: "task", RTCType: "candidate", RTCData: json.RawMessage(`{"candidate":"candidate:test"}`)})
	}
	select {
	case <-r.done:
	case <-time.After(time.Second):
		t.Fatal("candidate cap did not close peer")
	}
	m := uploadReply(t, transport)
	if m.RTCType != "close" {
		t.Fatalf("missing candidate-overflow close: %+v", m)
	}
	path := filepath.Join(t.TempDir(), "canceled")
	c.handleManagedUploadStart(&protocol.Message{Type: protocol.MsgFileUploadStart, TaskID: "task", Path: path, RTCType: "offer", Size: 3, SHA256: transferDigest("abc")})
	if c.managedUploadFor("task") != nil {
		t.Fatal("canceled direct task reserved upload")
	}
	if _, err := os.Stat(path + ".rdevpart"); !os.IsNotExist(err) {
		t.Fatalf("canceled upload opened part: %v", err)
	}
}

func TestFileRTCCancelWaitsForUploadStartBarrier(t *testing.T) {
	c, _ := newUploadTestClient()
	r := &fileRTCSession{client: c, start: protocol.Message{TaskID: "task"}, done: make(chan struct{})}
	c.fileRTC = map[string]*fileRTCSession{"task": r}
	r.startMu.Lock()
	finished := make(chan struct{})
	go func() { c.handleManagedTransferCancel("task"); close(finished) }()
	<-r.done
	select {
	case <-finished:
		t.Fatal("cancel returned before upload start barrier")
	default:
	}
	r.startMu.Unlock()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("cancel did not finish")
	}
}

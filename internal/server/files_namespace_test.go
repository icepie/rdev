package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lxzan/gws"
	"rdev/internal/protocol"
)

type fileNamespaceTransport struct {
	fakeDeviceTransport
	frames chan []byte
}

func (t *fileNamespaceTransport) WriteBinary(raw []byte) error {
	t.frames <- bytes.Clone(raw)
	return nil
}

type fileNamespaceRecorder struct {
	gws.BuiltinEventHandler
	text   chan fileMsg
	binary chan []byte
}

func (r *fileNamespaceRecorder) OnMessage(_ *gws.Conn, m *gws.Message) {
	defer m.Close()
	if m.Opcode == gws.OpcodeBinary {
		r.binary <- bytes.Clone(m.Bytes())
		return
	}
	var reply fileMsg
	if json.Unmarshal(m.Bytes(), &reply) == nil {
		r.text <- reply
	}
}
func receiveFileFrame[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("missing file frame")
		var zero T
		return zero
	}
}

func TestFileSocketsNamespaceTasksAndRequests(t *testing.T) {
	deviceMessages := make(chan *protocol.Message, 32)
	tr := &fileNamespaceTransport{fakeDeviceTransport: fakeDeviceTransport{onSend: func(m *protocol.Message) { deviceMessages <- m }}, frames: make(chan []byte, 16)}
	srv := &Server{clients: map[string]*ClientConn{"dev": {ID: "dev", Transport: tr}}, fileRequests: make(map[string]*fileSocket), fileTasks: make(map[string]*fileTaskRoute)}
	server := httptest.NewServer(http.HandlerFunc(srv.HandleFilesWS))
	defer server.Close()
	connect := func() (*gws.Conn, *fileNamespaceRecorder) {
		rec := &fileNamespaceRecorder{text: make(chan fileMsg, 16), binary: make(chan []byte, 16)}
		c, _, err := gws.NewClient(rec, &gws.ClientOption{Addr: "ws" + strings.TrimPrefix(server.URL, "http"), HandshakeTimeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.WriteClose(1000, nil) })
		go c.ReadLoop()
		return c, rec
	}
	a, ar := connect()
	b, br := connect()
	send := func(c *gws.Conn, m fileMsg) {
		t.Helper()
		data, _ := json.Marshal(m)
		if err := c.WriteMessage(gws.OpcodeText, data); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []*gws.Conn{a, b} {
		send(c, fileMsg{Op: "download_start", DeviceID: "dev", TaskID: "same-task", Path: "/video.mp4"})
	}
	first, second := receiveFileFrame(t, deviceMessages), receiveFileFrame(t, deviceMessages)
	if first.TaskID == second.TaskID || first.TaskID == "same-task" || second.TaskID == "same-task" {
		t.Fatal("wire task IDs collided")
	}
	// Identify each owner through metadata; ordering between sockets is free.
	srv.handleFileManagerMessage(&protocol.Message{Type: protocol.MsgFileDownloadStart, TaskID: first.TaskID, Path: "first", Size: 10})
	srv.handleFileManagerMessage(&protocol.Message{Type: protocol.MsgFileDownloadStart, TaskID: second.TaskID, Path: "second", Size: 10})
	am, bm := receiveFileFrame(t, ar.text), receiveFileFrame(t, br.text)
	if am.TaskID != "same-task" || bm.TaskID != "same-task" || am.Path == bm.Path {
		t.Fatal("metadata delivered to wrong owner", am, bm)
	}
	wireA, wireB := first.TaskID, second.TaskID
	if am.Path == "second" {
		wireA, wireB = wireB, wireA
	}
	srv.handleFileManagerBinary(protocol.EncodeBinFrameOffset(protocol.BinFileDownloadChunk, wireA, 2, []byte("A")))
	srv.handleFileManagerBinary(protocol.EncodeBinFrameOffset(protocol.BinFileDownloadChunk, wireB, 5, []byte("B")))
	for i, rec := range []*fileNamespaceRecorder{ar, br} {
		_, id, offset, payload, err := protocol.DecodeBinFrameOffset(receiveFileFrame(t, rec.binary))
		if err != nil || id != "same-task" || offset != []int64{2, 5}[i] || string(payload) != []string{"A", "B"}[i] {
			t.Fatalf("binary routing: %q %d %s %v", id, offset, payload, err)
		}
	}
	send(a, fileMsg{Op: "cancel", TaskID: "same-task"})
	canceled := receiveFileFrame(t, deviceMessages)
	if canceled.Type != protocol.MsgFileTransferCancel || canceled.TaskID != wireA {
		t.Fatal("canceled wrong transfer")
	}
	if reply := receiveFileFrame(t, ar.text); reply.Op != "canceled" || reply.TaskID != "same-task" {
		t.Fatal(reply)
	}
	if srv.getFileTask(wireA) != nil || srv.getFileTask(wireB) == nil {
		t.Fatal("cancel affected other socket")
	}
	// JSON cancellation also has a binary fallback, with the private wire ID.
	_, cancelID, _, _, err := protocol.DecodeBinFrameOffset(receiveFileFrame(t, tr.frames))
	if err != nil || cancelID != wireA {
		t.Fatal("wrong binary cancel fallback")
	}
	if err := b.WriteMessage(gws.OpcodeBinary, protocol.EncodeBinFrameOffset(protocol.BinFileTransferCancel, "same-task", 5, nil)); err != nil {
		t.Fatal(err)
	}
	_, cancelID, offset, _, err := protocol.DecodeBinFrameOffset(receiveFileFrame(t, tr.frames))
	if err != nil || cancelID != wireB || offset != 5 {
		t.Fatal("incoming binary ID was not scoped")
	}
	// Identical request IDs also coexist and reverse-order replies stay owned.
	send(a, fileMsg{Op: "list", DeviceID: "dev", RequestID: "same-request", Path: "/A"})
	ra := receiveFileFrame(t, deviceMessages)
	send(b, fileMsg{Op: "list", DeviceID: "dev", RequestID: "same-request", Path: "/B"})
	rb := receiveFileFrame(t, deviceMessages)
	if ra.RequestID == rb.RequestID {
		t.Fatal("request IDs collided")
	}
	srv.handleFileManagerMessage(&protocol.Message{Type: protocol.MsgFileListResult, RequestID: rb.RequestID, Path: "/B"})
	srv.handleFileManagerMessage(&protocol.Message{Type: protocol.MsgFileListResult, RequestID: ra.RequestID, Path: "/A"})
	for i, rec := range []*fileNamespaceRecorder{ar, br} {
		m := receiveFileFrame(t, rec.text)
		if m.RequestID != "same-request" || m.Path != []string{"/A", "/B"}[i] {
			t.Fatal("request misrouted", m)
		}
	}
	send(a, fileMsg{Op: "download_start", DeviceID: "dev", Path: "/generated.mp4"})
	generated := receiveFileFrame(t, deviceMessages)
	srv.handleFileManagerMessage(&protocol.Message{Type: protocol.MsgFileDownloadStart, TaskID: generated.TaskID, Size: 10})
	generatedReply := receiveFileFrame(t, ar.text)
	if generatedReply.TaskID == "" || generatedReply.TaskID == generated.TaskID {
		t.Fatal("generated ID not translated")
	}
	send(a, fileMsg{Op: "cancel", TaskID: generatedReply.TaskID})
	if receiveFileFrame(t, deviceMessages).TaskID != generated.TaskID {
		t.Fatal("generated ID could not be canceled")
	}
	receiveFileFrame(t, ar.text)
	send(a, fileMsg{Op: "download_start", DeviceID: "dev", TaskID: strings.Repeat("x", 223), Path: "/video.mp4"})
	if m := receiveFileFrame(t, ar.text); m.Op != "error" || m.TaskID != strings.Repeat("x", 223) {
		t.Fatal("oversize wire ID accepted", m)
	}
}

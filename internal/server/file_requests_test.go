package server

import (
	"encoding/json"
	"fmt"
	"github.com/lxzan/gws"
	"net/http"
	"net/http/httptest"
	"rdev/internal/protocol"
	"strings"
	"testing"
	"time"
)

func TestFileRequestDeadlinesOwnershipAndLegacyCapabilities(t *testing.T) {
	messages := make(chan *protocol.Message, 8)
	srv := NewServer()
	srv.clients["dev"] = &ClientConn{ID: "dev", Transport: &fakeDeviceTransport{onSend: func(m *protocol.Message) { messages <- m }}}
	server := httptest.NewServer(http.HandlerFunc(srv.HandleFilesWS))
	defer server.Close()
	rec := &fileNamespaceRecorder{text: make(chan fileMsg, 8), binary: make(chan []byte, 1)}
	conn, _, err := gws.NewClient(rec, &gws.ClientOption{Addr: "ws" + strings.TrimPrefix(server.URL, "http")})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.WriteClose(1000, nil)
	go conn.ReadLoop()
	send := func(msg fileMsg) {
		data, _ := json.Marshal(msg)
		if err := conn.WriteMessage(gws.OpcodeText, data); err != nil {
			t.Fatal(err)
		}
	}
	send(fileMsg{Op: "create", RequestID: "legacy", DeviceID: "dev", Path: "/tmp/new"})
	if reply := receiveFileFrame(t, rec.text); reply.Code != "unsupported" {
		t.Fatalf("legacy reply %+v", reply)
	}
	send(fileMsg{Op: "list", RequestID: "slow", DeviceID: "dev", Path: "/tmp"})
	wire := receiveFileFrame(t, messages)
	if srv.takeFileRequest(wire.RequestID, "other") != nil {
		t.Fatal("cross-device reply accepted")
	}
	srv.fileMu.RLock()
	route := srv.fileRequests[wire.RequestID]
	srv.fileMu.RUnlock()
	route.timer.Stop()
	srv.expireFileRequest(wire.RequestID, route)
	if reply := receiveFileFrame(t, rec.text); reply.Code != "timeout" || reply.RequestID != "slow" {
		t.Fatalf("timeout reply %+v", reply)
	}
	if srv.takeFileRequest(wire.RequestID, "dev") != nil {
		t.Fatal("expired route retained")
	}
	// Reusing a request ID must not let an old timer remove the new generation.
	if err := srv.registerFileRequest(wire.RequestID, route.socket, "dev"); err != nil {
		t.Fatal(err)
	}
	srv.expireFileRequest(wire.RequestID, route)
	if srv.takeFileRequest(wire.RequestID, "dev") == nil {
		t.Fatal("old deadline removed new generation")
	}
	select {
	case <-messages:
		t.Fatal("legacy operation was forwarded")
	case <-time.After(10 * time.Millisecond):
	}
}
func TestFileRequestCapacityAndSocketCleanup(t *testing.T) {
	srv := NewServer()
	socket := &fileSocket{}
	for i := 0; i < fileRequestsPerSocket; i++ {
		if err := srv.registerFileRequest(fmt.Sprint(i), socket, "dev"); err != nil {
			t.Fatal(err)
		}
	}
	if err := srv.registerFileRequest("overflow", socket, "dev"); err == nil {
		t.Fatal("unbounded requests")
	}
	other := &fileSocket{}
	if err := srv.registerFileRequest("other", other, "dev"); err != nil {
		t.Fatal(err)
	}
	srv.closeFileSocket(socket)
	if len(srv.fileRequests) != 1 {
		t.Fatal("socket requests leaked or other socket removed")
	}
	srv.closeFileSocket(other)
}

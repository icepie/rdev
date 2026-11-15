package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lxzan/gws"
	"rdev/internal/protocol"
)

func TestFileRTCOfferCapabilityGates(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		enabled, capable, direct bool
	}{
		{"disabled", false, true, false},
		{"legacy-device", true, false, false},
		{"file-only-device", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := filesWSHandler{srv: &Server{WebRTCEnabled: tc.enabled}}
			m := fileMsg{RTCType: "offer", RTCData: json.RawMessage(`{"type":"offer","sdp":"test"}`)}
			h.prepareFileRTC(&ClientConn{FileRTC: tc.capable}, &m)
			if (m.RTCType == "offer") != tc.direct || (!tc.direct && len(m.RTCData) != 0) {
				t.Fatalf("incorrect capability gate: %+v", m)
			}
		})
	}
	for _, invalid := range []json.RawMessage{nil, json.RawMessage(`null`), json.RawMessage(`{"type":"answer","sdp":"x"}`), json.RawMessage(`{"type":"offer"}`)} {
		if validFileRTCSignal("offer", invalid) {
			t.Fatalf("accepted invalid offer %s", invalid)
		}
	}
	if validFileRTCSignal("candidate", json.RawMessage(`{}`)) || validFileRTCSignal("offer", json.RawMessage(strings.Repeat("x", 65537))) {
		t.Fatal("accepted invalid or oversized signal")
	}
}

func TestFileRTCScopedAuthorizedSignalingAndTerminalDrain(t *testing.T) {
	messages := make(chan *protocol.Message, 256)
	transport := &fakeDeviceTransport{onSend: func(m *protocol.Message) { messages <- m }}
	device := &ClientConn{ID: "device", Password: "secret", FileRTC: true, Transport: transport}
	srv := NewServer()
	srv.WebRTCEnabled = true
	srv.WebRTCICEServers = []protocol.IceServer{{URLs: []string{"turn:relay.example:3478"}, Username: "file-user", Credential: "private-token"}}
	srv.clients[device.ID] = device
	server := httptest.NewServer(http.HandlerFunc(srv.HandleFilesWS))
	defer server.Close()
	recorder := &fileNamespaceRecorder{text: make(chan fileMsg, 32), binary: make(chan []byte, 4)}
	conn, _, err := gws.NewClient(recorder, &gws.ClientOption{Addr: "ws" + strings.TrimPrefix(server.URL, "http")})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.WriteClose(1000, nil)
	go conn.ReadLoop()
	send := func(m fileMsg) {
		t.Helper()
		raw, _ := json.Marshal(m)
		if err := conn.WriteMessage(gws.OpcodeText, raw); err != nil {
			t.Fatal(err)
		}
	}
	digest := strings.Repeat("b", 64)
	start := fileMsg{Op: "download_start", DeviceID: device.ID, TaskID: "attempt", Path: "/authorized", SHA256: digest, RTCType: "offer", RTCData: json.RawMessage(`{"type":"offer","sdp":"test"}`)}
	send(start)
	if m := receiveFileFrame(t, recorder.text); m.Op != "auth_required" {
		t.Fatalf("unauthenticated start: %+v", m)
	}
	send(fileMsg{Op: "auth", DeviceID: device.ID, Password: "wrong"})
	if m := receiveFileFrame(t, recorder.text); m.Op != "auth_fail" {
		t.Fatalf("wrong password: %+v", m)
	}
	if len(messages) != 0 {
		t.Fatal("unauthorized negotiation reached device")
	}
	send(fileMsg{Op: "auth", DeviceID: device.ID, Password: "secret"})
	if m := receiveFileFrame(t, recorder.text); m.Op != "auth_ok" {
		t.Fatalf("auth failed: %+v", m)
	}
	send(start)
	wire := receiveFileFrame(t, messages)
	if wire.TaskID == start.TaskID || wire.RTCType != "offer" || wire.SHA256 != digest {
		t.Fatalf("start not scoped: %+v", wire)
	}
	if wire.IceServers == nil || len(*wire.IceServers) != 1 || (*wire.IceServers)[0].URLs[0] != "turn:relay.example:3478" || (*wire.IceServers)[0].Credential != "private-token" {
		t.Fatal("authorized file offer lacked server ICE config")
	}
	route := srv.getFileTask(wire.TaskID)
	if route == nil || route.client != device || !route.rtc {
		t.Fatal("missing bound direct route")
	}
	srv.handleClientMessage(device, &protocol.Message{Type: protocol.MsgFileDownloadStart, TaskID: wire.TaskID, SHA256: digest})
	if m := receiveFileFrame(t, recorder.text); m.Op != "download_start" || m.SHA256 != digest {
		t.Fatalf("download identity stripped: %+v", m)
	}
	prefix := strings.Repeat("a", 64)
	srv.handleClientMessage(device, &protocol.Message{Type: protocol.MsgFileUploadReady, TaskID: wire.TaskID, SHA256: digest, PrefixSHA256: prefix})
	if m := receiveFileFrame(t, recorder.text); m.Op != "upload_ready" || m.SHA256 != digest || m.PrefixSHA256 != prefix {
		t.Fatalf("upload prefix identity stripped: %+v", m)
	}
	h := filesWSHandler{srv: srv}
	candidate := fileMsg{TaskID: wire.TaskID, RTCType: "candidate", RTCData: json.RawMessage(`{"candidate":"candidate:test"}`)}
	h.forwardFileRTCSignal(&fileSocket{}, candidate)
	if len(messages) != 0 {
		t.Fatal("foreign socket signaled owned task")
	}
	h.forwardFileRTCSignal(route.socket, candidate)
	if m := receiveFileFrame(t, messages); m.Type != protocol.MsgFileRTCSignal || m.TaskID != wire.TaskID {
		t.Fatalf("bad scoped signal: %+v", m)
	}
	route.socket.authMu.Lock()
	route.socket.authorized[device.ID] = "stale-password"
	route.socket.authMu.Unlock()
	h.forwardFileRTCSignal(route.socket, candidate)
	if len(messages) != 0 {
		t.Fatal("stale authorization accepted")
	}
	route.socket.authMu.Lock()
	route.socket.authorized[device.ID] = passwordFingerprint(device.Password)
	route.socket.authMu.Unlock()
	replacement := &ClientConn{ID: device.ID, Password: device.Password, FileRTC: true, Transport: transport}
	srv.mu.Lock()
	srv.clients[device.ID] = replacement
	srv.mu.Unlock()
	h.forwardFileRTCSignal(route.socket, candidate)
	srv.handleFileRTCSignal(replacement, &protocol.Message{TaskID: wire.TaskID, RTCType: "answer", RTCData: json.RawMessage(`{"type":"answer","sdp":"wrong-device"}`)})
	srv.handleClientMessage(replacement, &protocol.Message{Type: protocol.MsgFileTransferEnd, TaskID: wire.TaskID, Success: true})
	if len(messages) != 0 || len(recorder.text) != 0 || srv.getFileTask(wire.TaskID) != route {
		t.Fatal("replacement inherited old route")
	}
	srv.mu.Lock()
	srv.clients[device.ID] = device
	srv.mu.Unlock()
	srv.handleFileRTCSignal(device, &protocol.Message{TaskID: wire.TaskID, RTCType: "answer", RTCData: json.RawMessage(`{"type":"answer","sdp":"test"}`)})
	if m := receiveFileFrame(t, recorder.text); m.Op != "rtc_signal" || m.TaskID != "attempt" {
		t.Fatalf("answer leaked scope: %+v", m)
	}
	// Candidate work is bounded even when the authenticated browser floods it.
	for range 140 {
		h.forwardFileRTCSignal(route.socket, candidate)
	}
	if len(messages) != 127 {
		t.Fatalf("candidate cap: got %d remaining signals", len(messages))
	}
	for len(messages) > 0 {
		<-messages
	}
	srv.handleFileManagerBinary(protocol.EncodeBinFrameOffset(protocol.BinFileTransferEnd, wire.TaskID, 0, nil))
	_ = receiveFileFrame(t, recorder.binary)
	if srv.getFileTask(wire.TaskID) != route {
		t.Fatal("binary range end discarded authoritative text completion route")
	}
	srv.handleClientMessage(device, &protocol.Message{Type: protocol.MsgFileTransferEnd, TaskID: wire.TaskID, SHA256: digest, Success: true})
	if m := receiveFileFrame(t, recorder.text); m.Op != "transfer_end" || m.SHA256 != digest {
		t.Fatalf("terminal missing: %+v", m)
	}
	if srv.getFileTask(wire.TaskID) != route {
		t.Fatal("text terminal prematurely detached direct channel")
	}
	h.forwardFileRTCSignal(route.socket, fileMsg{TaskID: wire.TaskID, RTCType: "close"})
	if m := receiveFileFrame(t, messages); m.RTCType != "close" {
		t.Fatal("terminal acknowledgment not forwarded")
	}
	if srv.getFileTask(wire.TaskID) != nil {
		t.Fatal("acknowledged route leaked")
	}
}

func TestFileRTCExpiryCancelsOriginatingPeer(t *testing.T) {
	// Socket-close cancellation is covered by TestClosingFileSocketDoesNotBlockOtherWorkspaces;
	// expiration must also stop the originating peer, never its replacement.
	messages := make(chan *protocol.Message, 1)
	device := &ClientConn{ID: "device", Transport: &fakeDeviceTransport{onSend: func(m *protocol.Message) { messages <- m }}}
	srv := NewServer()
	route := &fileTaskRoute{client: device, rtc: true}
	srv.fileTasks["task"] = route
	srv.expireFileRTC("task", route)
	srv.fileMu.Lock()
	route.timer.Reset(time.Millisecond)
	srv.fileMu.Unlock()
	if m := receiveFileFrame(t, messages); m.Type != protocol.MsgFileTransferCancel || m.TaskID != "task" {
		t.Fatalf("expiry did not cancel peer: %+v", m)
	}
	if srv.getFileTask("task") != nil {
		t.Fatal("expired route leaked")
	}
}

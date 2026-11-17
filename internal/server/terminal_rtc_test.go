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

func terminalRTCFixture(t *testing.T, password string) (*Server, *ClientConn, chan *protocol.Message, *gws.Conn, *terminalReadyRecorder) {
	t.Helper()
	s := NewServer()
	s.WebRTCEnabled = true
	s.WebRTCICEServers = []protocol.IceServer{}
	sent := make(chan *protocol.Message, 32)
	c := &ClientConn{ID: "dev", Password: password, TerminalRTC: true, Sessions: make(map[string]*ProxySession), Transport: &fakeDeviceTransport{onSend: func(m *protocol.Message) { sent <- m }}}
	s.clients[c.ID] = c
	socket, rec := connectTerminalURLFixture(t, s, "?device=dev&rtc=1")
	return s, c, sent, socket, rec
}

func terminalSend(t *testing.T, socket *gws.Conn, msg terminalMsg) {
	t.Helper()
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	if err := socket.WriteMessage(gws.OpcodeText, raw); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalRTCAuthenticationAndSamePTYFallback(t *testing.T) {
	s, c, sent, socket, rec := terminalRTCFixture(t, "secret")
	if frame := receiveFileFrame(t, rec.text); frame.Op != "auth" {
		t.Fatalf("before auth: %+v", frame)
	}
	terminalSend(t, socket, terminalMsg{Op: "rtc_ready", SessionID: "unauthorized"})
	terminalSend(t, socket, terminalMsg{Op: "auth", Password: "secret"})
	if frame := receiveFileFrame(t, rec.text); frame.Op != "auth_ok" {
		t.Fatalf("auth: %+v", frame)
	}
	start := receiveFileFrame(t, rec.text)
	if start.Op != "rtc_start" || start.SessionID == "" {
		t.Fatalf("start: %+v", start)
	}
	id := start.SessionID
	terminalSend(t, socket, terminalMsg{Op: "rtc_signal", SessionID: id, RTCType: "offer", RTCData: json.RawMessage(`{"type":"offer","sdp":"fixture"}`)})
	offer := receiveFileFrame(t, sent)
	if offer.Type != protocol.MsgTerminalRTCSignal || offer.IceServers == nil || *offer.IceServers == nil || len(*offer.IceServers) != 0 {
		t.Fatalf("host-only offer: %+v", offer)
	}
	terminalSend(t, socket, terminalMsg{Op: "rtc_ready", SessionID: id})
	// A replaced device connection cannot activate this terminal.
	s.handleTerminalRTCSignal(&ClientConn{ID: c.ID}, &protocol.Message{SessionID: id, RTCType: "ready"})
	s.handleTerminalRTCSignal(c, &protocol.Message{SessionID: id, RTCType: "ready"})
	created := receiveFileFrame(t, sent)
	if created.Type != protocol.MsgNewSession || !created.TerminalRTC || created.SessionID != id {
		t.Fatalf("create: %+v", created)
	}
	if frame := receiveFileFrame(t, rec.text); frame.Op != "ready" {
		t.Fatalf("ready: %+v", frame)
	}
	terminalSend(t, socket, terminalMsg{Op: "rtc_fallback", SessionID: id})
	request := receiveFileFrame(t, sent)
	if request.RTCType != "fallback" || request.SessionID != id {
		t.Fatalf("fallback: %+v", request)
	}
	if frame := receiveFileFrame(t, rec.text); frame.Op != "rtc_signal" || frame.RTCType != "close" {
		t.Fatalf("pause: %+v", frame)
	}
	s.handleTerminalRTCSignal(c, &protocol.Message{SessionID: id, RTCType: "fallback_ready"})
	if frame := receiveFileFrame(t, rec.text); frame.Op != "rtc_fallback" || frame.Message == "" {
		t.Fatalf("fallback ack: %+v", frame)
	}
	session := s.getSession(id)
	session.WriteCh <- []byte("same shell remains alive")
	if output := receiveFileFrame(t, rec.binary); string(output) != "same shell remains alive" {
		t.Fatalf("relay: %q", output)
	}
	terminalSend(t, socket, terminalMsg{Op: "resize", Rows: 31, Cols: 117})
	resized := receiveFileFrame(t, sent)
	if resized.Type != protocol.MsgResize || resized.SessionID != id {
		t.Fatalf("PTY recreated or resize lost: %+v", resized)
	}
	s.handleClientMessage(c, &protocol.Message{Type: protocol.MsgExitCode, SessionID: id, ExitCode: 7})
	s.handleClientMessage(c, &protocol.Message{Type: protocol.MsgClose, SessionID: id})
	if frame := receiveFileFrame(t, rec.text); frame.Op != "exit" || frame.Code != 7 {
		t.Fatalf("exit: %+v", frame)
	}
}

func TestTerminalRTCNegotiationFallbackStartsOnlyOnce(t *testing.T) {
	s, c, sent, socket, rec := terminalRTCFixture(t, "")
	start := receiveFileFrame(t, rec.text)
	terminalSend(t, socket, terminalMsg{Op: "rtc_fallback", SessionID: start.SessionID})
	if msg := receiveFileFrame(t, sent); msg.RTCType != "close" {
		t.Fatalf("close peer: %+v", msg)
	}
	if msg := receiveFileFrame(t, sent); msg.Type != protocol.MsgNewSession || msg.TerminalRTC {
		t.Fatalf("relay start: %+v", msg)
	}
	if msg := receiveFileFrame(t, rec.text); msg.Op != "rtc_fallback" {
		t.Fatalf("fallback: %+v", msg)
	}
	if msg := receiveFileFrame(t, rec.text); msg.Op != "ready" {
		t.Fatalf("ready: %+v", msg)
	}
	s.handleTerminalRTCSignal(c, &protocol.Message{SessionID: start.SessionID, RTCType: "ready"})
	terminalSend(t, socket, terminalMsg{Op: "rtc_ready", SessionID: start.SessionID})
	terminalSend(t, socket, terminalMsg{Op: "resize", Rows: 24, Cols: 80})
	if msg := receiveFileFrame(t, sent); msg.Type != protocol.MsgResize {
		t.Fatalf("late ready recreated shell: %+v", msg)
	}
}

func TestTerminalRTCClosedNegotiationCannotStartShell(t *testing.T) {
	s, c, sent, socket, rec := terminalRTCFixture(t, "")
	start := receiveFileFrame(t, rec.text)
	tc := s.getSession(start.SessionID).terminal
	socket.WriteClose(1000, nil)
	select {
	case <-tc.done:
	case <-time.After(time.Second):
		t.Fatal("terminal not closed")
	}
	(&terminalWSHandler{srv: s}).fallbackTerminalRTC(tc, c)
	(&terminalWSHandler{srv: s}).terminalRTCReady(tc, c, true)
	for range 2 {
		if msg := receiveFileFrame(t, sent); msg.Type == protocol.MsgNewSession {
			t.Fatal("closed terminal spawned shell")
		}
	}
	select {
	case msg := <-sent:
		t.Fatalf("unexpected post-close command: %+v", msg)
	default:
	}
}

func TestTerminalRTCObserverRestoresBroadcastWithoutNewShell(t *testing.T) {
	s, c, sent, socket, rec := terminalRTCFixture(t, "")
	start := receiveFileFrame(t, rec.text)
	terminalSend(t, socket, terminalMsg{Op: "rtc_ready", SessionID: start.SessionID})
	s.handleTerminalRTCSignal(c, &protocol.Message{SessionID: start.SessionID, RTCType: "ready"})
	if msg := receiveFileFrame(t, sent); msg.Type != protocol.MsgNewSession {
		t.Fatalf("start: %+v", msg)
	}
	if msg := receiveFileFrame(t, rec.text); msg.Op != "ready" {
		t.Fatalf("ready: %+v", msg)
	}
	server := httptest.NewServer(http.HandlerFunc(s.HandleSessionAttachWS))
	t.Cleanup(server.Close)
	observer := &terminalReadyRecorder{text: make(chan terminalMsg, 8), binary: make(chan []byte, 8)}
	view, _, err := gws.NewClient(observer, &gws.ClientOption{Addr: "ws" + strings.TrimPrefix(server.URL, "http") + "?session=" + start.SessionID, HandshakeTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { view.WriteClose(1000, nil) })
	go view.ReadLoop()
	if msg := receiveFileFrame(t, observer.text); msg.Op != "auth_ok" {
		t.Fatalf("attach: %+v", msg)
	}
	if msg := receiveFileFrame(t, sent); msg.RTCType != "fallback" || msg.SessionID != start.SessionID {
		t.Fatalf("observer did not recover relay: %+v", msg)
	}
	s.handleTerminalRTCSignal(c, &protocol.Message{SessionID: start.SessionID, RTCType: "fallback_ready"})
	s.getSession(start.SessionID).BroadcastOutput([]byte("shared output"))
	if output := receiveFileFrame(t, observer.binary); string(output) != "shared output" {
		t.Fatalf("observer output: %q", output)
	}
}

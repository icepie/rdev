package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lxzan/gws"
	"rdev/internal/protocol"
)

type terminalReadyRecorder struct {
	gws.BuiltinEventHandler
	text   chan terminalMsg
	binary chan []byte
}

func (r *terminalReadyRecorder) OnMessage(_ *gws.Conn, m *gws.Message) {
	defer m.Close()
	if m.Opcode == gws.OpcodeBinary {
		r.binary <- bytes.Clone(m.Bytes())
		return
	}
	var frame terminalMsg
	if json.Unmarshal(m.Bytes(), &frame) == nil {
		r.text <- frame
	}
}
func connectTerminalFixture(t *testing.T, s *Server) (*gws.Conn, *terminalReadyRecorder) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(s.HandleTerminalWS))
	t.Cleanup(server.Close)
	rec := &terminalReadyRecorder{text: make(chan terminalMsg, 8), binary: make(chan []byte, 8)}
	c, _, err := gws.NewClient(rec, &gws.ClientOption{Addr: "ws" + strings.TrimPrefix(server.URL, "http") + "?device=dev", HandshakeTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.WriteClose(1000, nil) })
	go c.ReadLoop()
	return c, rec
}
func TestTerminalReadyPublishesRouteBeforeDeviceOutput(t *testing.T) {
	for _, password := range []string{"", "fixture-password"} {
		t.Run(map[bool]string{true: "password", false: "unprotected"}[password != ""], func(t *testing.T) { testTerminalReady(t, password) })
	}
}
func testTerminalReady(t *testing.T, password string) {
	srv := &Server{sessions: make(map[string]*ProxySession), authLimiter: newAuthRateLimiter()}
	sent := make(chan *protocol.Message, 16)
	tr := &fakeDeviceTransport{onSend: func(m *protocol.Message) {
		if m.Type == protocol.MsgNewSession {
			session := srv.getSession(m.SessionID)
			if session == nil {
				t.Error("device started before output route was registered")
			} else {
				session.WriteCh <- []byte("first prompt")
			}
		}
		sent <- m
	}}
	client := &ClientConn{ID: "dev", Password: password, Transport: tr, Sessions: make(map[string]*ProxySession)}
	srv.clients = map[string]*ClientConn{"dev": client}
	c, rec := connectTerminalFixture(t, srv)
	if password != "" {
		if frame := receiveFileFrame(t, rec.text); frame.Op != "auth" {
			t.Fatalf("expected auth: %+v", frame)
		}
		raw, _ := json.Marshal(terminalMsg{Op: "auth", Password: password})
		if err := c.WriteMessage(gws.OpcodeText, raw); err != nil {
			t.Fatal(err)
		}
		if frame := receiveFileFrame(t, rec.text); frame.Op != "auth_ok" {
			t.Fatalf("expected auth_ok: %+v", frame)
		}
	}
	start := receiveFileFrame(t, sent)
	if start.Type != protocol.MsgNewSession {
		t.Fatal("missing start")
	}
	if frame := receiveFileFrame(t, rec.text); frame.Op != "ready" {
		t.Fatalf("not ready: %+v", frame)
	}
	if output := receiveFileFrame(t, rec.binary); string(output) != "first prompt" {
		t.Fatalf("lost output: %s", output)
	}
	raw, _ := json.Marshal(terminalMsg{Op: "resize", Rows: 42, Cols: 132})
	if err := c.WriteMessage(gws.OpcodeText, raw); err != nil {
		t.Fatal(err)
	}
	resize := receiveFileFrame(t, sent)
	if resize.Type != protocol.MsgResize || resize.Rows != 42 || resize.Cols != 132 || resize.SessionID != start.SessionID {
		t.Fatalf("bad resize: %+v", resize)
	}
}

type failingTerminalTransport struct{ fakeDeviceTransport }

func (*failingTerminalTransport) WriteJSON([]byte) error { return errors.New("disconnected") }
func TestTerminalStartFailureDoesNotLeakSessionReservation(t *testing.T) {
	for _, limited := range []bool{false, true} {
		t.Run(map[bool]string{false: "send failure", true: "quota"}[limited], func(t *testing.T) {
			client := &ClientConn{ID: "dev", Transport: &failingTerminalTransport{}, Sessions: make(map[string]*ProxySession)}
			srv := &Server{clients: map[string]*ClientConn{"dev": client}, sessions: make(map[string]*ProxySession)}
			if limited {
				srv.MaxSessions = 1
				client.Sessions["existing"] = &ProxySession{ID: "existing"}
				client.Transport = &fakeDeviceTransport{onSend: func(m *protocol.Message) {
					if m.Type == protocol.MsgNewSession {
						t.Error("started device session past quota")
					}
				}}
			}
			_, rec := connectTerminalFixture(t, srv)
			if frame := receiveFileFrame(t, rec.text); frame.Op != "error" {
				t.Fatalf("expected error: %+v", frame)
			}
			srv.sessMu.RLock()
			remaining := len(srv.sessions)
			srv.sessMu.RUnlock()
			client.mu.Lock()
			owned := len(client.Sessions)
			client.mu.Unlock()
			want := 0
			if limited {
				want = 1
			}
			if remaining != 0 || owned != want {
				t.Fatalf("leaked reservation: global=%d client=%d", remaining, owned)
			}
		})
	}
}

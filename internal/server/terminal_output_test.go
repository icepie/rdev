package server

import (
	"testing"

	"rdev/internal/protocol"
)

// A normal device end must deliver stdout/stderr that were already queued
// before the exit message, never the exit message first.
func TestTerminalFlushesQueuedOutputBeforeExit(t *testing.T) {
	sent := make(chan *protocol.Message, 8)
	srv := &Server{sessions: make(map[string]*ProxySession), authLimiter: newAuthRateLimiter()}
	client := &ClientConn{
		ID:        "dev",
		Transport: &fakeDeviceTransport{onSend: func(m *protocol.Message) { sent <- m }},
		Sessions:  make(map[string]*ProxySession),
	}
	srv.clients = map[string]*ClientConn{"dev": client}
	_, rec := connectTerminalFixture(t, srv)
	start := receiveFileFrame(t, sent)
	if start.Type != protocol.MsgNewSession {
		t.Fatalf("device start = %+v", start)
	}
	receiveFileFrame(t, rec.text) // ready

	sess := srv.getSession(start.SessionID)
	if sess == nil {
		t.Fatal("session not registered")
	}
	sess.WriteCh <- []byte("stdout-tail")
	sess.StderrCh <- []byte("stderr-tail")
	sess.SetExitCode(7)
	sess.SignalClose()

	seen := map[string]bool{}
	for range 2 {
		output := receiveFileFrame(t, rec.binary)
		seen[string(output)] = true
	}
	if !seen["stdout-tail"] || !seen["stderr-tail"] {
		t.Fatalf("queued output lost: %v", seen)
	}
	exit := receiveFileFrame(t, rec.text)
	if exit.Op != "exit" || exit.Code != 7 {
		t.Fatalf("exit frame = %+v", exit)
	}
}

// Output the browser cannot consume is an explicit session failure: the
// device is told to stop and the session is reclaimed, never truncated.
func TestSessionOutputBackpressureClosesSession(t *testing.T) {
	sent := make(chan *protocol.Message, 4)
	client := &ClientConn{
		ID:        "dev",
		Transport: &fakeDeviceTransport{onSend: func(m *protocol.Message) { sent <- m }},
		Sessions:  make(map[string]*ProxySession),
	}
	sess := &ProxySession{
		ID: "s1", ClientID: "dev",
		WriteCh:  make(chan []byte, 1),
		StderrCh: make(chan []byte, 1),
		CloseCh:  make(chan struct{}, 1),
		exitDone: make(chan struct{}),
	}
	client.Sessions["s1"] = sess
	srv := &Server{
		clients:  map[string]*ClientConn{"dev": client},
		sessions: map[string]*ProxySession{"s1": sess},
	}

	srv.pushSessionOutput(sess, client, []byte("first"), false)
	srv.pushSessionOutput(sess, client, []byte("second"), false)

	if srv.getSession("s1") != nil {
		t.Fatal("overrun session stayed registered")
	}
	if _, ok := client.Sessions["s1"]; ok {
		t.Fatal("overrun session stayed attached to the device")
	}
	select {
	case <-sess.CloseCh:
	default:
		t.Fatal("overrun session was not signalled to end")
	}
	msg := receiveFileFrame(t, sent)
	if msg.Type != protocol.MsgClose || msg.SessionID != "s1" {
		t.Fatalf("device message = %+v", msg)
	}
}

func TestPushOutputRejectsClosedOutput(t *testing.T) {
	sess := &ProxySession{
		ID:       "closed",
		WriteCh:  make(chan []byte, 1),
		StderrCh: make(chan []byte, 1),
		exitDone: make(chan struct{}),
	}
	sess.CloseOutput()
	queued, open := sess.PushOutput([]byte("late"), false)
	if queued || open {
		t.Fatalf("PushOutput(closed) = queued:%v open:%v", queued, open)
	}
}

// A teardown path can close the output channels before the terminal drains
// them; buffered output and the exit message must still reach the browser.
func TestTerminalSendsExitWhenOutputClosedByTeardown(t *testing.T) {
	sent := make(chan *protocol.Message, 8)
	srv := &Server{sessions: make(map[string]*ProxySession), authLimiter: newAuthRateLimiter()}
	client := &ClientConn{
		ID:        "dev",
		Transport: &fakeDeviceTransport{onSend: func(m *protocol.Message) { sent <- m }},
		Sessions:  make(map[string]*ProxySession),
	}
	srv.clients = map[string]*ClientConn{"dev": client}
	_, rec := connectTerminalFixture(t, srv)
	start := receiveFileFrame(t, sent)
	receiveFileFrame(t, rec.text) // ready

	sess := srv.getSession(start.SessionID)
	if sess == nil {
		t.Fatal("session not registered")
	}
	sess.WriteCh <- []byte("buffered-before-close")
	sess.SetExitCode(3)
	sess.CloseOutput()
	sess.SignalClose()

	output := receiveFileFrame(t, rec.binary)
	if string(output) != "buffered-before-close" {
		t.Fatalf("output = %q", output)
	}
	exit := receiveFileFrame(t, rec.text)
	if exit.Op != "exit" || exit.Code != 3 {
		t.Fatalf("exit frame = %+v", exit)
	}
}

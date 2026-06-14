package server

import (
	"strings"
	"testing"
	"time"

	"rdev/internal/protocol"
)

func TestAllocateClientIDLockedUsesSuffixOnConflict(t *testing.T) {
	s := NewServer()
	s.clients["device"] = &ClientConn{ID: "device"}

	if got := s.allocateClientIDLocked("other"); got != "other" {
		t.Fatalf("expected free ID to be unchanged, got %q", got)
	}
	if got := s.allocateClientIDLocked("device"); got != "device-2" {
		t.Fatalf("expected first conflicting ID to use -2 suffix, got %q", got)
	}

	s.clients["device-2"] = &ClientConn{ID: "device-2"}
	s.clients["device-3"] = &ClientConn{ID: "device-3"}
	if got := s.allocateClientIDLocked("device"); got != "device-4" {
		t.Fatalf("expected suffix allocation to skip occupied IDs, got %q", got)
	}
}

func TestTerminalSessionEnvAdvertisesImageProtocols(t *testing.T) {
	env := strings.Join(terminalSessionEnv(), "\n")
	for _, want := range []string{
		"COLORTERM=truecolor",
		"TERM_PROGRAM=RDev",
		"RDEV_IMAGE_PROTOCOLS=sixel,iterm2,kitty",
	} {
		if !strings.Contains(env, want) {
			t.Fatalf("terminalSessionEnv() missing %q in %q", want, env)
		}
	}
}

func TestProxyForwardOpenAndFailSignals(t *testing.T) {
	fwd := &ProxyForward{OpenCh: make(chan struct{})}
	fwd.SignalOpen()
	select {
	case <-fwd.OpenCh:
	case <-time.After(time.Second):
		t.Fatal("expected open signal")
	}
	fwd.SignalOpen()

	failed := &ProxyForward{OpenCh: make(chan struct{})}
	failed.SignalFail("dial refused")
	select {
	case <-failed.OpenCh:
	case <-time.After(time.Second):
		t.Fatal("expected fail to signal open waiters")
	}
	if got := failed.FailError(); got != "dial refused" {
		t.Fatalf("FailError() = %q, want dial refused", got)
	}
}

func TestForwardRegisterLimitAndTCPFail(t *testing.T) {
	s := NewServer()
	s.MaxForwards = 1
	client := &ClientConn{ID: "device", Forwards: make(map[string]*ProxyForward)}
	first := &ProxyForward{ID: "f1", ClientID: "device", WriteCh: make(chan []byte), CloseCh: make(chan struct{}), OpenCh: make(chan struct{})}
	second := &ProxyForward{ID: "f2", ClientID: "device", WriteCh: make(chan []byte), CloseCh: make(chan struct{}), OpenCh: make(chan struct{})}

	if !s.RegisterForward(first, client) {
		t.Fatal("expected first forward registration to succeed")
	}
	if s.RegisterForward(second, client) {
		t.Fatal("expected second forward registration to hit limit")
	}

	closed := false
	first.CloseSSH = func() { closed = true }
	s.handleClientMessage(client, &protocol.Message{Type: protocol.MsgTCPFail, ForwardID: "f1", Error: "refused"})

	select {
	case <-first.OpenCh:
	case <-time.After(time.Second):
		t.Fatal("expected TCP fail to wake open waiters")
	}
	if !closed {
		t.Fatal("expected TCP fail to close SSH channel")
	}
	if first.FailError() != "refused" {
		t.Fatalf("FailError() = %q, want refused", first.FailError())
	}
	if got := s.getForward("f1"); got != nil {
		t.Fatalf("forward should be removed after TCP fail, got %#v", got)
	}
}

func TestReverseForwardOpenFailAndRegistry(t *testing.T) {
	s := NewServer()
	rev := &ReverseForward{ID: "listen-1", OpenCh: make(chan struct{})}
	s.RegisterReverseForward(rev)
	if got := s.getReverseForward("listen-1"); got != rev {
		t.Fatalf("getReverseForward() = %#v, want rev", got)
	}

	s.handleClientMessage(&ClientConn{ID: "device"}, &protocol.Message{Type: protocol.MsgTCPListenOK, ListenID: "listen-1", Port: 4321})
	select {
	case <-rev.OpenCh:
	case <-time.After(time.Second):
		t.Fatal("expected listen ok to wake waiters")
	}
	if port, errText := rev.Result(); port != 4321 || errText != "" {
		t.Fatalf("Result() = (%d, %q), want (4321, '')", port, errText)
	}

	failed := &ReverseForward{ID: "listen-2", OpenCh: make(chan struct{})}
	s.RegisterReverseForward(failed)
	s.handleClientMessage(&ClientConn{ID: "device"}, &protocol.Message{Type: protocol.MsgTCPListenOK, ListenID: "listen-2", Error: "bind failed"})
	select {
	case <-failed.OpenCh:
	case <-time.After(time.Second):
		t.Fatal("expected listen fail to wake waiters")
	}
	if _, errText := failed.Result(); errText != "bind failed" {
		t.Fatalf("listen fail error = %q, want bind failed", errText)
	}

	s.removeReverseForward("listen-1")
	if got := s.getReverseForward("listen-1"); got != nil {
		t.Fatalf("reverse forward should be removed, got %#v", got)
	}
}

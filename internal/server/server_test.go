package server

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/lxzan/gws"
	"rdev/internal/protocol"
)

func TestProxySessionHistoryReplayAndLimit(t *testing.T) {
	sess := &ProxySession{}
	sess.BroadcastOutput([]byte("hello "))
	sess.BroadcastStderr([]byte("world"))

	history, writeCh, stderrCh, done := sess.AddObserver("obs-1")
	if got := bytes.Join(history, nil); string(got) != "hello world" {
		t.Fatalf("history = %q, want hello world", got)
	}

	sess.BroadcastOutput([]byte(" live"))
	select {
	case got := <-writeCh:
		if string(got) != " live" {
			t.Fatalf("live output = %q, want live", got)
		}
	case <-time.After(time.Second):
		t.Fatal("expected live output after observer registration")
	}

	sess.NotifyObserversClose()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("expected observer done after close notification")
	}
	select {
	case got := <-stderrCh:
		t.Fatalf("unexpected stderr after history replay: %q", got)
	default:
	}

	large := bytes.Repeat([]byte("x"), sessionHistoryLimit+128)
	limited := &ProxySession{}
	limited.BroadcastOutput(large)
	limitedHistory := bytes.Join(limited.HistorySnapshot(), nil)
	if len(limitedHistory) != sessionHistoryLimit {
		t.Fatalf("limited history size = %d, want %d", len(limitedHistory), sessionHistoryLimit)
	}
	if !bytes.Equal(limitedHistory, large[len(large)-sessionHistoryLimit:]) {
		t.Fatal("history should keep the newest bytes")
	}
}

func TestRegisterClientReplacesSameInstanceReconnect(t *testing.T) {
	s := NewServer()
	oldConn := &gws.Conn{}
	newConn := &gws.Conn{}
	oldClient := &ClientConn{ID: "device", RequestedID: "device", InstanceID: "inst-1", Conn: oldConn, Sessions: make(map[string]*ProxySession), Forwards: make(map[string]*ProxyForward)}
	newClient := &ClientConn{ID: "device", RequestedID: "device", InstanceID: "inst-1", Conn: newConn, Sessions: make(map[string]*ProxySession), Forwards: make(map[string]*ProxyForward)}

	if old, assigned, duplicate := s.registerClient(oldClient); old != nil || assigned != "device" || duplicate {
		t.Fatalf("first registration = (%#v, %q, %v), want nil device false", old, assigned, duplicate)
	}
	old, assigned, duplicate := s.registerClient(newClient)
	if old != oldClient || assigned != "device" || duplicate {
		t.Fatalf("same-instance reconnect = (%#v, %q, %v), want oldClient device false", old, assigned, duplicate)
	}
	if got := s.clients["device"]; got != newClient {
		t.Fatalf("current client = %#v, want newClient", got)
	}
	if _, ok := s.clients["device-2"]; ok {
		t.Fatal("same device reconnect should not allocate a suffixed ID")
	}
}

func TestRegisterClientDuplicateIDGetsSuffix(t *testing.T) {
	s := NewServer()
	first := &ClientConn{ID: "device", RequestedID: "device", InstanceID: "inst-1", Conn: &gws.Conn{}, Sessions: make(map[string]*ProxySession), Forwards: make(map[string]*ProxyForward)}
	second := &ClientConn{ID: "device", RequestedID: "device", InstanceID: "inst-2", Conn: &gws.Conn{}, Sessions: make(map[string]*ProxySession), Forwards: make(map[string]*ProxyForward)}

	if old, assigned, duplicate := s.registerClient(first); old != nil || assigned != "device" || duplicate {
		t.Fatalf("first registration = (%#v, %q, %v), want nil device false", old, assigned, duplicate)
	}
	old, assigned, duplicate := s.registerClient(second)
	if old != nil || assigned != "device-2" || !duplicate {
		t.Fatalf("duplicate registration = (%#v, %q, %v), want nil device-2 true", old, assigned, duplicate)
	}
	if got := s.clients["device"]; got != first {
		t.Fatalf("original client = %#v, want first", got)
	}
	if got := s.clients["device-2"]; got != second {
		t.Fatalf("duplicate client = %#v, want second", got)
	}
}

func TestClientGPUDesktopAvailableFromCapabilities(t *testing.T) {
	s := NewServer()
	for _, backend := range []string{"rdev-desktop", "gpu-desktop-tunnel", "pipewire"} {
		client := &ClientConn{
			ID: "device-" + backend,
			Desktop: &protocol.DesktopCapabilities{
				Supported: true,
				Backends:  []string{backend},
			},
		}
		if !s.clientGPUDesktopAvailable(client) {
			t.Fatalf("clientGPUDesktopAvailable(%q) = false, want true", backend)
		}
	}
}

func TestClientGPUDesktopAvailableRequiresSupportedCapability(t *testing.T) {
	s := NewServer()
	client := &ClientConn{
		ID: "device",
		Desktop: &protocol.DesktopCapabilities{
			Supported: false,
			Backends:  []string{"rdev-desktop"},
		},
	}
	if s.clientGPUDesktopAvailable(client) {
		t.Fatal("clientGPUDesktopAvailable should ignore unsupported desktop capability")
	}
}

func TestUnregisterClientIgnoresStaleSocket(t *testing.T) {
	s := NewServer()
	oldConn := &gws.Conn{}
	newConn := &gws.Conn{}
	newClient := &ClientConn{ID: "device", Conn: newConn, Sessions: make(map[string]*ProxySession), Forwards: make(map[string]*ProxyForward)}
	s.clients["device"] = newClient

	if removed, ok := s.unregisterClient("device", oldConn); ok || removed != nil {
		t.Fatalf("stale unregister removed %#v", removed)
	}
	if got := s.clients["device"]; got != newClient {
		t.Fatalf("stale unregister changed current client to %#v", got)
	}
	removed, ok := s.unregisterClient("device", newConn)
	if !ok || removed != newClient {
		t.Fatalf("current unregister = (%#v, %v), want newClient true", removed, ok)
	}
	if got := s.clients["device"]; got != nil {
		t.Fatalf("current unregister should remove device, got %#v", got)
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

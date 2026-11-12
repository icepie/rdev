package server

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"rdev/internal/protocol"
)

func TestAutomationExpiredLeaseClosesBeforeReplacement(t *testing.T) {
	s := NewServer()
	old := testAutomation(t, s, "device", "old")
	next := testAutomation(t, s, "device", "next")
	closed := false
	old.state.desktop = map[string]*agentDesktopSession{"device": {closeRoute: func() { closed = true }}}
	old.state.screens = map[string]automationScreen{"device": {at: time.Now()}}
	s.desktopOwners = map[string]desktopAutomationLease{"device": {owner: old.state, tool: old, until: time.Now().Add(-time.Second)}}
	old.state.opMu.Lock()
	if err := next.claim("device"); err == nil {
		t.Fatal("expired but active input was stolen")
	}
	if closed {
		t.Fatal("active input resource was closed")
	}
	old.state.opMu.Unlock()
	if err := next.claim("device"); err != nil {
		t.Fatal(err)
	}
	defer s.finishAutomationTurn(next.state)
	if !closed || len(old.state.screens) != 0 || s.desktopOwners["device"].owner != next.state {
		t.Fatal("replacement did not close and invalidate the previous owner")
	}
}

func TestAutomationExpiryAndStaleTimer(t *testing.T) {
	s := NewServer()
	tool := testAutomation(t, s, "device", "thread")
	closed := false
	tool.state.desktop = map[string]*agentDesktopSession{"device": {closeRoute: func() { closed = true }}}
	until := time.Now().Add(-time.Second)
	s.desktopOwners = map[string]desktopAutomationLease{"device": {owner: tool.state, tool: tool, until: until}}
	s.expireAutomationDesktop("device", tool.state, until.Add(-time.Second))
	if closed {
		t.Fatal("stale timer closed renewed ownership")
	}
	s.expireAutomationDesktop("device", tool.state, until)
	if !closed || len(s.desktopOwners) != 0 {
		t.Fatal("expiry retained resource or ownership")
	}
}

func TestAutomationManualReservationBlocksAllConversations(t *testing.T) {
	s := NewServer()
	first := testAutomation(t, s, "device", "first")
	second := testAutomation(t, s, "device", "second")
	releaseA := s.beginDesktopManualInput("device")
	releaseB := s.beginDesktopManualInput("device")
	if err := first.claim("device"); err == nil {
		t.Fatal("new conversation overlapped human input")
	}
	releaseA()
	if err := second.claim("device"); err == nil {
		t.Fatal("nested manual reservation was dropped")
	}
	releaseB()
	if err := second.claim("device"); err != nil {
		t.Fatal(err)
	}
	s.finishAutomationTurn(second.state)
}

func TestAutomationCanceledConsumerReleasesLease(t *testing.T) {
	s := NewServer()
	tool := testAutomation(t, s, "device", "thread")
	parent, cancel := context.WithCancel(context.Background())
	_, finish, err := tool.begin(parent)
	if err != nil {
		t.Fatal(err)
	}
	if err := tool.claim("device"); err != nil {
		t.Fatal(err)
	}
	closed := false
	tool.state.desktop = map[string]*agentDesktopSession{"device": {closeRoute: func() { closed = true }}}
	cancel()
	finish()
	if !closed || len(s.desktopOwners) != 0 {
		t.Fatal("canceled consumer retained desktop control")
	}
}

func TestComputerOpenErrorReleasesLease(t *testing.T) {
	s, client, transport := newProxyTestServer(t, "device", "")
	client.Desktop = &protocol.DesktopCapabilities{Supported: true, Input: true}
	tool := computerTool{testAutomation(t, s, "device", "first")}
	transport.onSend = func(m *protocol.Message) {
		if m.Type == protocol.MsgDesktopStart {
			s.handleDesktopMessage(&protocol.Message{Type: protocol.MsgDesktopClose, SessionID: m.SessionID, Error: "capture failed"})
		}
	}
	if _, err := tool.Execute(context.Background(), []byte(`{"action":"screenshot"}`)); err == nil || !strings.Contains(err.Error(), "capture failed") {
		t.Fatalf("capture error = %v", err)
	}
	other := testAutomation(t, s, "device", "second")
	if err := other.claim("device"); err != nil {
		t.Fatal("failed consumer retained ownership", err)
	}
	s.finishAutomationTurn(other.state)
}

func TestGPUDesktopManualFrameInspectionPreservesWire(t *testing.T) {
	for _, tc := range []struct {
		payload string
		manual  bool
	}{
		{`"GetScreenshot"`, false},
		{`"ReleaseKeyboard"`, true},
		{`{"PointerEvent":{"x":0.5}}`, true},
		{`{"KeyboardEvent":{"key":"Enter"}}`, true},
		{`{"TextInputEvent":{"text":"你好"}}`, true},
		{`{"Config":{"capture_cursor":true}}`, false},
	} {
		wire := []byte{0x81, 0x80 | byte(len(tc.payload)), 1, 2, 3, 4}
		for i, b := range []byte(tc.payload) {
			wire = append(wire, b^wire[2+i%4])
		}
		got, manual, err := readGPUDesktopClientFrame(bytes.NewReader(wire))
		if err != nil || manual != tc.manual || !bytes.Equal(got, wire) {
			t.Fatalf("%s: manual=%v error=%v wire preserved=%v", tc.payload, manual, err, bytes.Equal(got, wire))
		}
	}
	if _, _, err := readGPUDesktopClientFrame(bytes.NewReader([]byte{0x81, 0})); err == nil || errors.Is(err, context.Canceled) {
		t.Fatal("unmasked browser frame accepted")
	}
}

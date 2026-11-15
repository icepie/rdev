package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGPUDesktopTunnelAdmissionTracksIdentityAndControlConnection(t *testing.T) {
	s := NewServer()
	identity, proof := strings.Repeat("a", 64), strings.Repeat("b", 64)
	id, _, _, err := s.authenticateRegistration(identity, "device", proof)
	if err != nil {
		t.Fatal(err)
	}
	client := &ClientConn{ID: id, DeviceIdentity: identity, ReconnectProof: proof}
	if _, assigned, _ := s.registerClient(client); assigned != id {
		t.Fatal("registration failed")
	}
	first := newGPUDesktopTunnel(id, nil)
	t.Cleanup(first.close)
	if s.registerGPUDesktopTunnel(first, client, strings.Repeat("c", 64)) {
		t.Fatal("forged proof admitted")
	}
	if !s.registerGPUDesktopTunnel(first, client, proof) {
		t.Fatal("valid tunnel rejected")
	}
	replacement := &ClientConn{ID: id, DeviceIdentity: identity, ReconnectProof: proof}
	if _, assigned, _ := s.registerClient(replacement); assigned != id {
		t.Fatal("reconnect failed")
	}
	late := newGPUDesktopTunnel(id, nil)
	t.Cleanup(late.close)
	if s.registerGPUDesktopTunnel(late, client, proof) {
		t.Fatal("stale control connection published a tunnel")
	}
	if !s.registerGPUDesktopTunnel(late, replacement, proof) {
		t.Fatal("current control connection rejected")
	}
	select {
	case <-first.closed:
	default:
		t.Fatal("replaced tunnel remained open")
	}
	request := httptest.NewRequest(http.MethodPost, "/api/identity/revoke", strings.NewReader(`{"deviceIdentity":"`+identity+`"}`))
	response := httptest.NewRecorder()
	s.HandleIdentityAPI(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d %s", response.Code, response.Body.String())
	}
	select {
	case <-late.closed:
	default:
		t.Fatal("revoked tunnel remained open")
	}
	if s.gpuDesktopTunnel(id) != nil {
		t.Fatal("revoked tunnel remained discoverable")
	}
	postRevoke := newGPUDesktopTunnel(id, nil)
	t.Cleanup(postRevoke.close)
	if s.registerGPUDesktopTunnel(postRevoke, replacement, proof) {
		t.Fatal("tunnel published after revocation")
	}
}

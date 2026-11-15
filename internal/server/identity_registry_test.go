package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIdentityEnrollmentProofRestartAndRevocation(t *testing.T) {
	dir := t.TempDir()
	s := NewServer()
	s.DataDir = dir
	identity := strings.Repeat("1", 64)
	proof := strings.Repeat("2", 64)
	assigned, _, _, err := s.authenticateRegistration(identity, "device", proof)
	if err != nil || assigned != "device" {
		t.Fatalf("enrollment = %q, %v", assigned, err)
	}
	if _, _, _, err := s.authenticateRegistration(identity, "device", strings.Repeat("3", 64)); err == nil {
		t.Fatal("forged reconnect proof accepted")
	}
	newServer := NewServer()
	newServer.DataDir = dir
	assigned, _, resumed, err := newServer.authenticateRegistration(identity, "renamed", proof)
	if err != nil || !resumed || assigned != "device" {
		t.Fatalf("reconnect after restart = %q, %v, %v", assigned, resumed, err)
	}
	newServer.AdminToken = "admin"
	req := httptest.NewRequest(http.MethodPost, "/api/identity/revoke", strings.NewReader(`{"deviceIdentity":"`+identity+`"}`))
	req.Header.Set("Authorization", "Bearer admin")
	res := httptest.NewRecorder()
	newServer.HandleIdentityAPI(res, req)
	if res.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d: %s", res.Code, res.Body.String())
	}
	if _, _, _, err := newServer.authenticateRegistration(identity, "device", proof); err == nil {
		t.Fatal("revoked identity reconnected")
	}
	other, _, _, err := newServer.authenticateRegistration(strings.Repeat("4", 64), "device", strings.Repeat("5", 64))
	if err != nil || other == "device" {
		t.Fatalf("new identity reused revoked public ID %q: %v", other, err)
	}
	var disk struct {
		Records []identityRecord `json:"records"`
	}
	data, err := os.ReadFile(filepath.Join(dir, "identity-registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &disk); err != nil || len(disk.Records) != 2 {
		t.Fatalf("registry records=%d err=%v", len(disk.Records), err)
	}
}

func TestIdentityOpenModeDoesNotAllowPublicNameTakeover(t *testing.T) {
	s := NewServer()
	first := strings.Repeat("a", 64)
	proof := strings.Repeat("b", 64)
	assigned, _, _, err := s.authenticateRegistration(first, "device", proof)
	if err != nil || assigned != "device" {
		t.Fatalf("first enrollment: %q %v", assigned, err)
	}
	other, _, _, err := s.authenticateRegistration(strings.Repeat("c", 64), "device", strings.Repeat("d", 64))
	if err != nil || other == assigned {
		t.Fatalf("open mode takeover: %q %v", other, err)
	}
}

// Enroll through the real verifier path; fixture connections never bypass it.
func enrollIdentityTestClient(t *testing.T, s *Server, client *ClientConn) {
	t.Helper()
	if client.DeviceIdentity == "" {
		var credential [64]byte
		if _, err := rand.Read(credential[:]); err != nil {
			t.Fatal(err)
		}
		client.DeviceIdentity = hex.EncodeToString(credential[:32])
		client.ReconnectProof = hex.EncodeToString(credential[32:])
	}
	assigned, _, _, err := s.authenticateRegistration(client.DeviceIdentity, client.ID, client.ReconnectProof)
	if err != nil {
		t.Fatal(err)
	}
	client.ID = assigned
}

func TestIdentityRejectsAnonymousAndMalformedCredentials(t *testing.T) {
	s := NewServer()
	identity, proof := strings.Repeat("a", 64), strings.Repeat("b", 64)
	for _, credential := range [][2]string{{"", ""}, {identity, ""}, {identity, strings.Repeat("B", 64)}, {strings.Repeat("a", 63), proof}} {
		if _, _, _, err := s.authenticateRegistration(credential[0], "device", credential[1]); err == nil {
			t.Fatal("invalid credential accepted")
		}
	}
	if _, assigned, _ := s.registerClient(&ClientConn{ID: "device", InstanceID: "known"}); assigned != "" {
		t.Fatal("anonymous publication accepted")
	}
}

func TestIdentityLongNameCollisionSurvivesRestart(t *testing.T) {
	s := NewServer()
	s.DataDir = t.TempDir()
	name := strings.Repeat("a", 64)
	if _, _, _, err := s.authenticateRegistration(strings.Repeat("1", 64), name, strings.Repeat("2", 64)); err != nil {
		t.Fatal(err)
	}
	assigned, _, _, err := s.authenticateRegistration(strings.Repeat("3", 64), name, strings.Repeat("4", 64))
	if err != nil || !validClientID(assigned) || assigned == name {
		t.Fatalf("collision ID %q: %v", assigned, err)
	}
	restarted := NewServer()
	restarted.DataDir = s.DataDir
	got, _, _, err := restarted.authenticateRegistration(strings.Repeat("3", 64), name, strings.Repeat("4", 64))
	if err != nil || got != assigned {
		t.Fatalf("restart ID %q: %v", got, err)
	}
}

func TestIdentityAPIProtectsVerifierAndRejectsForeignOrigin(t *testing.T) {
	s := NewServer()
	s.AdminToken = "admin"
	identity, proof := strings.Repeat("a", 64), strings.Repeat("b", 64)
	if _, _, _, err := s.authenticateRegistration(identity, "device", proof); err != nil {
		t.Fatal(err)
	}
	denied := httptest.NewRecorder()
	s.HandleIdentityAPI(denied, httptest.NewRequest(http.MethodGet, "/api/identity", nil))
	if denied.Code != http.StatusUnauthorized {
		t.Fatal("identity list not protected")
	}
	req := httptest.NewRequest(http.MethodGet, "/api/identity", nil)
	req.Header.Set("Authorization", "Bearer admin")
	res := httptest.NewRecorder()
	s.HandleIdentityAPI(res, req)
	if res.Code != http.StatusOK || strings.Contains(res.Body.String(), proof) || strings.Contains(res.Body.String(), "proofHash") {
		t.Fatalf("unsafe identity response: %d %s", res.Code, res.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/identity/revoke", strings.NewReader(`{"deviceIdentity":"`+identity+`"}`))
	req.Header.Set("Authorization", "Bearer admin")
	req.Header.Set("Origin", "https://foreign.example")
	res = httptest.NewRecorder()
	s.HandleIdentityAPI(res, req)
	if res.Code != http.StatusForbidden || !s.validateReconnectProof(identity, proof) {
		t.Fatal("foreign-origin revoke accepted")
	}
}

func TestRevocationBetweenAuthenticationAndPublication(t *testing.T) {
	s := NewServer()
	s.DataDir = t.TempDir()
	client := &ClientConn{ID: "device"}
	enrollIdentityTestClient(t, s, client)
	req := httptest.NewRequest(http.MethodPost, "/api/identity/revoke", strings.NewReader(`{"deviceIdentity":"`+client.DeviceIdentity+`"}`))
	res := httptest.NewRecorder()
	s.HandleIdentityAPI(res, req)
	if res.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d", res.Code)
	}
	if _, assigned, _ := s.registerClient(client); assigned != "" {
		t.Fatal("revoked enrollment published")
	}
	restarted := NewServer()
	restarted.DataDir = s.DataDir
	if _, _, _, err := restarted.authenticateRegistration(client.DeviceIdentity, "device", client.ReconnectProof); err == nil {
		t.Fatal("revocation lost after restart")
	}
}

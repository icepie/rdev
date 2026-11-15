package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHealthAuthenticationAndReadiness(t *testing.T) {
	s := NewServer()
	s.AdminToken = "health-admin"
	s.DataDir = t.TempDir()
	call := func(handler http.HandlerFunc, path, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if token != "" {
			r.Header.Set("X-RDev-Token", token)
		}
		w := httptest.NewRecorder()
		handler(w, r)
		return w
	}
	for path, handler := range map[string]http.HandlerFunc{"/api/health": s.HandleHealth, "/metrics": s.HandleMetrics, "/readyz": s.HandleReadiness} {
		if w := call(handler, path, ""); w.Code != http.StatusUnauthorized {
			t.Fatalf("%s exposed operational state: %d %s", path, w.Code, w.Body)
		}
	}
	if w := call(s.HandleLiveness, "/healthz", ""); w.Code != http.StatusOK || w.Body.String() != "ok\n" {
		t.Fatalf("liveness: %d %s", w.Code, w.Body)
	}
	if w := call(s.HandleReadiness, "/readyz", s.AdminToken); w.Code != http.StatusOK {
		t.Fatalf("idle readiness: %d %s", w.Code, w.Body)
	}
	if s.scheduler != nil || s.agentStoreInst != nil {
		t.Fatal("health polling initialized a lazy subsystem")
	}
	m, err := s.Scheduler()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.StopScheduler)
	m.mu.Lock()
	m.fault = errors.New("disk unavailable")
	m.mu.Unlock()
	if w := call(s.HandleReadiness, "/readyz", s.AdminToken); w.Code != http.StatusServiceUnavailable || w.Body.String() != "degraded\n" {
		t.Fatalf("fault did not fail readiness: %d %s", w.Code, w.Body)
	}
	if w := call(s.HandleLiveness, "/healthz", ""); w.Code != http.StatusOK {
		t.Fatalf("storage failure incorrectly fails liveness: %d", w.Code)
	}
	w := call(s.HandleHealth, "/api/health", s.AdminToken)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "disk unavailable") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("diagnostic unavailable: %d %s", w.Code, w.Body)
	}
}

func TestHealthMetricsBoundedFailureCategories(t *testing.T) {
	s := NewServer()
	s.recordTransferFailure("device")
	s.recordTransferFailure("transport")
	s.recordTransferFailure("untrusted-path-secret")
	w := httptest.NewRecorder()
	s.HandleMetrics(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := w.Body.String()
	if !strings.Contains(body, "rdev_transfer_failures_total{reason=\"device\"} 1\n") || !strings.Contains(body, "rdev_transfer_failures_total{reason=\"transport\"} 2\n") || strings.Contains(body, "untrusted-path-secret") {
		t.Fatalf("unexpected failure categories: %s", body)
	}
	w = httptest.NewRecorder()
	s.HandleHealth(w, httptest.NewRequest(http.MethodPost, "/api/health", nil))
	if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("write method accepted: %d", w.Code)
	}
}

func TestHealthHistoryRecoveryRestoresReadiness(t *testing.T) {
	s := NewServer()
	s.DataDir = t.TempDir()
	s.AgentSessionTTL = time.Hour
	store := s.agentStore()
	path := store.path("device", "t_1")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{\"t\":\"meta\",\"v\":1}\n{broken\n"), 0600); err != nil {
		t.Fatal(err)
	}
	store.load("device", "t_1")
	response := httptest.NewRecorder()
	s.HandleReadiness(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("damaged history readiness = %d", response.Code)
	}
	response = httptest.NewRecorder()
	s.HandleHealth(response, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	var health healthSnapshot
	if err := json.Unmarshal(response.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if health.Status != "degraded" || health.History.Faulted != 1 || len(health.History.Faults) != 1 || health.History.Faults[0].Thread != "t_1" {
		t.Fatalf("missing damaged-thread diagnostic: %+v", health)
	}
	if _, _, err := store.recover("device", "t_1"); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	s.HandleReadiness(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusOK || response.Body.String() != "ok\n" {
		t.Fatalf("recovered readiness = %d %s", response.Code, response.Body.String())
	}
}

func TestHealthIdentityStorageFaultFailsReadiness(t *testing.T) {
	s := NewServer()
	s.DataDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(s.DataDir, "identity-registry.json"), []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.authenticateRegistration(strings.Repeat("a", 64), "device", strings.Repeat("b", 64)); err == nil {
		t.Fatal("registration accepted corrupt identity registry")
	}
	response := httptest.NewRecorder()
	s.HandleReadiness(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("identity fault readiness = %d", response.Code)
	}
	response = httptest.NewRecorder()
	s.HandleMetrics(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(response.Body.String(), "rdev_identity_storage_fault 1\n") || !strings.Contains(response.Body.String(), "rdev_ready 0\n") {
		t.Fatalf("identity fault missing from metrics: %s", response.Body.String())
	}
}

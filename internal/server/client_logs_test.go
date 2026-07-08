package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rdev/internal/protocol"
)

func TestSafeClientLogDeviceID(t *testing.T) {
	cases := map[string]string{
		"dev1":          "dev1",
		"../etc/passwd": "_etc_passwd",
		"/abs/path":     "_abs_path",
		"控制\x00id":      "控制_id",
		"..":            "device",
	}
	for in, want := range cases {
		if got := safeClientLogDeviceID(in); got != want {
			t.Fatalf("safeClientLogDeviceID(%q)=%q want %q", in, got, want)
		}
	}
}

func TestClientLogAppendTailAndCleanup(t *testing.T) {
	dir := t.TempDir()
	m := NewClientLogManager(dir, time.Hour, 64)
	if err := m.appendBatch("dev/one", "go/test", []protocol.LogEntry{{Level: "info", Module: "test", Message: "hello token=abc"}, {Level: "warn", Module: "test", Message: "world"}}); err != nil {
		t.Fatal(err)
	}
	lines, err := m.tail("dev/one", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || !strings.Contains(lines[0], "hello token=abc") || !strings.Contains(lines[1], "world") {
		t.Fatalf("tail lines = %#v", lines)
	}
	old := filepath.Join(dir, safeClientLogDeviceID("dev/one"), "2000-01-01.log")
	if err := os.WriteFile(old, []byte("old\n"), 0644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(old, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	m.cleanupOnce()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old log should be removed, stat err=%v", err)
	}
}

func TestClientLogsAPIAuthConfigTailDownloadDelete(t *testing.T) {
	s := NewServer()
	s.AdminToken = "tok"
	s.ClientLogs = NewClientLogManager(t.TempDir(), 7*24*time.Hour, 32*1024*1024)

	req := httptest.NewRequest(http.MethodGet, "/api/client-logs/config", nil)
	w := httptest.NewRecorder()
	s.HandleClientLogsAPI(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized code=%d", w.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/client-logs/devices/dev1/config?token=tok", strings.NewReader(`{"enabled":true,"level":"debug"}`))
	w = httptest.NewRecorder()
	s.HandleClientLogsAPI(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("set config code=%d body=%s", w.Code, w.Body.String())
	}
	var cfg clientLogConfig
	if err := json.Unmarshal(w.Body.Bytes(), &cfg); err != nil || !cfg.Enabled || cfg.Level != "debug" {
		t.Fatalf("config response cfg=%#v err=%v", cfg, err)
	}

	if err := s.ClientLogs.appendBatch("dev1", "go/test", []protocol.LogEntry{{Level: "info", Module: "api", Message: "line1"}}); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/client-logs/devices/dev1/tail?lines=1&token=tok", nil)
	w = httptest.NewRecorder()
	s.HandleClientLogsAPI(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "line1") {
		t.Fatalf("tail code=%d body=%s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/client-logs/devices/dev1/download?date="+time.Now().Format("2006-01-02")+"&token=tok", nil)
	w = httptest.NewRecorder()
	s.HandleClientLogsAPI(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "line1") {
		t.Fatalf("download code=%d body=%s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest(http.MethodDelete, "/api/client-logs/devices/dev1?token=tok", nil)
	w = httptest.NewRecorder()
	s.HandleClientLogsAPI(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete code=%d", w.Code)
	}
}

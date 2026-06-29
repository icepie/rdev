package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rdev/internal/server"
)

func TestCleanPageRoutes(t *testing.T) {
	srv := server.NewServer()
	mux := http.NewServeMux()
	mux.HandleFunc("/terminal", splitPageAndWebSocket(srv.StaticPageHandler("terminal.html"), func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusSwitchingProtocols)
	}))
	mux.HandleFunc("/remote-desktop", srv.StaticPageHandler("desktop.html"))

	t.Run("terminal page", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/terminal?device=test", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "<title") || !strings.Contains(rec.Body.String(), "RDev Terminal") {
			t.Fatalf("/terminal did not return terminal HTML")
		}
	})

	t.Run("terminal websocket", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/terminal?device=test", nil)
		req.Header.Set("Upgrade", "websocket")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusSwitchingProtocols {
			t.Fatalf("status = %d, want 101", rec.Code)
		}
	})

	t.Run("remote desktop page", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/remote-desktop?device=test", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "<title") || !strings.Contains(rec.Body.String(), "RDev Desktop") {
			t.Fatalf("/remote-desktop did not return desktop HTML")
		}
	})
}

package client

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNormalizeWebSocketURLPreservesQuery(t *testing.T) {
	got, err := normalizeWebSocketURL("wss://example.com?token=abc")
	if err != nil {
		t.Fatal(err)
	}
	want := "wss://example.com/ws?token=abc"
	if got != want {
		t.Fatalf("normalize = %q, want %q", got, want)
	}
}

func TestResolveWebSocketURLFollowsRedirect(t *testing.T) {
	var target string
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	}))
	defer redirector.Close()

	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws" {
			t.Fatalf("path = %q, want /ws", r.URL.Path)
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer targetServer.Close()
	target = targetServer.URL

	got, err := resolveWebSocketURL("ws"+redirector.URL[len("http"):], 5)
	if err != nil {
		t.Fatal(err)
	}
	want := "ws" + targetServer.URL[len("http"):] + "/ws"
	if got != want {
		t.Fatalf("resolved = %q, want %q", got, want)
	}
}

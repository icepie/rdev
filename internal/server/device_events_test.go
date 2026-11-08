package server

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDeviceEventsCoalesceAndIgnoreStaleDisconnect(t *testing.T) {
	s := &Server{clients: make(map[string]*ClientConn)}
	ch, unsubscribe := s.subscribeDeviceEvents()
	<-ch
	old := &ClientConn{ID: "device", InstanceID: "instance"}
	s.registerClient(old)
	next := &ClientConn{ID: "device", InstanceID: "instance", Transport: &streamDeviceTransport{}}
	s.registerClient(next)
	// Both mutations collapse into one invalidation for a slow listener.
	if len(ch) != 1 {
		t.Fatalf("pending changes: %d", len(ch))
	}
	<-ch
	if _, ok := s.unregisterClientTransport("device", old.Transport); ok {
		t.Fatal("stale disconnect removed replacement")
	}
	if len(ch) != 0 {
		t.Fatal("stale disconnect published a false offline state")
	}
	if _, ok := s.unregisterClientTransport("device", next.Transport); !ok {
		t.Fatal("live disconnect failed")
	}
	if len(ch) != 1 {
		t.Fatal("missing disconnect event")
	}
	unsubscribe()
	s.mu.RLock()
	remaining := len(s.deviceWatchers)
	s.mu.RUnlock()
	if remaining != 0 {
		t.Fatal("subscriber leaked")
	}
}

func TestDeviceEventStreamAuthenticatesAndResynchronizes(t *testing.T) {
	s := &Server{AdminToken: "fixture-token", clients: make(map[string]*ClientConn)}
	denied := httptest.NewRecorder()
	s.HandleDeviceEvents(denied, httptest.NewRequest("GET", "/api/clients/events", nil))
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("unprotected event stream: %d", denied.Code)
	}
	server := httptest.NewServer(http.HandlerFunc(s.HandleDeviceEvents))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
	req.Header.Set("Authorization", "Bearer fixture-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal("not SSE")
	}
	reader := bufio.NewReader(resp.Body)
	for _, want := range []string{"event: devices\n", "data: {}\n", "\n"} {
		got, err := reader.ReadString('\n')
		if err != nil || got != want {
			t.Fatalf("snapshot invalidation %q %v", got, err)
		}
	}
	s.registerClient(&ClientConn{ID: "device", Password: "private"})
	got, err := reader.ReadString('\n')
	if err != nil || got != "event: devices\n" {
		t.Fatalf("registration update %q %v", got, err)
	}
	cancel()
}

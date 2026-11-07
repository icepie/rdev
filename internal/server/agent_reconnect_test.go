package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lxzan/gws"
)

type agentCloseRecorder struct {
	gws.BuiltinEventHandler
	closed chan error
}

func (r *agentCloseRecorder) OnClose(_ *gws.Conn, err error) { r.closed <- err }

func TestOfflineAgentConnectionCanRetry(t *testing.T) {
	srv := newPrefsTestServer(t)
	server := httptest.NewServer(http.HandlerFunc(srv.HandleAgentWS))
	defer server.Close()
	rec := &agentCloseRecorder{closed: make(chan error, 1)}
	socket, _, err := gws.NewClient(rec, &gws.ClientOption{
		Addr:             "ws" + strings.TrimPrefix(server.URL, "http") + "/agent?device=offline",
		HandshakeTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.WriteClose(1000, nil)
	go socket.ReadLoop()
	select {
	case err := <-rec.closed:
		var closed *gws.CloseError
		if !errors.As(err, &closed) || closed.Code != 1013 {
			t.Fatalf("offline close must permit retry: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("offline connection did not close")
	}
}

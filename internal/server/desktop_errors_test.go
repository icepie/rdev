package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lxzan/gws"
)

func TestDesktopOfflineErrorIsRetryable(t *testing.T) {
	srv := &Server{clients: map[string]*ClientConn{}}
	server := httptest.NewServer(http.HandlerFunc(srv.HandleDesktopWS))
	defer server.Close()
	for _, device := range []string{"", "offline"} {
		t.Run(device, func(t *testing.T) {
			rec := &rawWSRecorder{msgs: make(chan map[string]json.RawMessage, 4)}
			socket, _, err := gws.NewClient(rec, &gws.ClientOption{
				Addr:             "ws" + strings.TrimPrefix(server.URL, "http") + "?device=" + device,
				HandshakeTimeout: time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer socket.WriteClose(1000, nil)
			go socket.ReadLoop()
			select {
			case frame := <-rec.msgs:
				var response desktopMsg
				raw, _ := json.Marshal(frame)
				if err := json.Unmarshal(raw, &response); err != nil {
					t.Fatal(err)
				}
				if response.Op != "error" || response.Retryable != (device != "") {
					t.Fatalf("unexpected desktop error: %+v", response)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("no desktop error received")
			}
		})
	}
}

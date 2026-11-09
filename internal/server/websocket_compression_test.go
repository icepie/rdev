package server

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// OkHttp offers only permessage-deflate and rejects client_max_window_bits,
// even when the server's upgrade otherwise succeeds.
func TestDeviceWebSocketCompressionNegotiation(t *testing.T) {
	s := NewServer()
	ts := httptest.NewServer(http.HandlerFunc(s.HandleWS))
	defer ts.Close()
	for _, offer := range []string{"", "permessage-deflate", "permessage-deflate; client_max_window_bits"} {
		t.Run(offer, func(t *testing.T) {
			conn, err := net.DialTimeout("tcp", ts.Listener.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			_, err = fmt.Fprintf(conn, "GET /ws HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Extensions: %s\r\n\r\n", ts.Listener.Addr(), offer)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusSwitchingProtocols {
				t.Fatalf("upgrade: %s", resp.Status)
			}
			ext := resp.Header.Get("Sec-WebSocket-Extensions")
			if offer == "" {
				if ext != "" {
					t.Fatalf("unsolicited compression: %q", ext)
				}
				return
			}
			if !strings.Contains(ext, "permessage-deflate") {
				t.Fatalf("compression not negotiated: %q", ext)
			}
			if strings.Contains(ext, "client_max_window_bits") {
				t.Fatalf("unsupported client compression constraint: %q", ext)
			}
		})
	}
}

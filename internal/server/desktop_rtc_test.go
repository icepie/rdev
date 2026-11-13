package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lxzan/gws"
	"rdev/internal/protocol"
)

func TestDesktopRTCAPIOptInAndAuth(t *testing.T) {
	srv := NewServer()
	srv.AdminToken = "secret"
	for _, enabled := range []bool{false, true} {
		srv.WebRTCEnabled = enabled
		for _, authorized := range []bool{false, true} {
			req := httptest.NewRequest(http.MethodGet, "/api/desktop/rtc", nil)
			if authorized {
				req.Header.Set("X-RDev-Token", "secret")
			}
			res := httptest.NewRecorder()
			srv.HandleDesktopRTCAPI(res, req)
			if !authorized {
				if res.Code != http.StatusUnauthorized {
					t.Fatalf("unauthenticated status = %d", res.Code)
				}
				continue
			}
			var body map[string]bool
			if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil || res.Code != http.StatusOK || len(body) != 1 || body["enabled"] != enabled {
				t.Fatalf("enabled=%v response=%d %s", enabled, res.Code, res.Body.String())
			}
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/api/desktop/rtc?token=secret", nil)
	res := httptest.NewRecorder()
	srv.HandleDesktopRTCAPI(res, req)
	if res.Code != http.StatusMethodNotAllowed || res.Header().Get("Allow") != "GET" {
		t.Fatalf("POST response = %d, Allow=%q", res.Code, res.Header().Get("Allow"))
	}
}

func TestDesktopRTCSignalingGates(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		enabled, capable               bool
		admin, token, password, origin string
		status                         int
	}{
		{name: "default off", capable: true, status: http.StatusForbidden},
		{name: "auth before disabled", admin: "secret", status: http.StatusUnauthorized},
		{name: "auth when enabled", enabled: true, capable: true, admin: "secret", status: http.StatusUnauthorized},
		{name: "foreign origin", enabled: true, capable: true, origin: "https://foreign.invalid", status: http.StatusForbidden},
		{name: "device password", enabled: true, capable: true, password: "wrong", status: http.StatusUnauthorized},
		{name: "capability required", enabled: true, password: "device-secret", status: http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewServer()
			srv.WebRTCEnabled, srv.AdminToken = tc.enabled, tc.admin
			srv.clients["device"] = &ClientConn{ID: "device", Password: "device-secret", Desktop: &protocol.DesktopCapabilities{RTC: tc.capable}}
			req := httptest.NewRequest(http.MethodGet, "/desktop/rtc?device=device&session=session&password="+tc.password, nil)
			req.Header.Set("X-RDev-Token", tc.token)
			req.Header.Set("Origin", tc.origin)
			res := httptest.NewRecorder()
			srv.HandleDesktopRTCWS(res, req)
			if res.Code != tc.status || len(srv.desktops) != 0 {
				t.Fatalf("status=%d routes=%d; want %d and no routes", res.Code, len(srv.desktops), tc.status)
			}
		})
	}
}

func TestDesktopRTCDeviceSignalsDroppedWithoutOptIn(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		srv := NewServer()
		srv.WebRTCEnabled = enabled
		client := &ClientConn{ID: "device", Desktop: &protocol.DesktopCapabilities{RTC: !enabled}}
		// A nil socket intentionally catches any attempt to forward this signal.
		srv.desktops["session"] = &desktopRoute{clientID: client.ID, rtc: &desktopRTCConn{client: client}}
		for _, kind := range []string{"answer", "candidate", "close"} {
			data := json.RawMessage(`{}`)
			if kind == "close" {
				data = nil
			}
			srv.handleDesktopRTCSignal(client, &protocol.Message{SessionID: "session", RTCType: kind, RTCData: data})
		}
	}
}

func TestDesktopRTCEnabledRelaysBoundSessionAndCleansUp(t *testing.T) {
	for _, admin := range []bool{false, true} {
		srv := NewServer()
		srv.WebRTCEnabled = true
		transport := &jobTestTransport{sent: make(chan protocol.Message, 8)}
		client := &ClientConn{ID: "device", Password: "device-secret", Transport: transport, Desktop: &protocol.DesktopCapabilities{RTC: true}}
		srv.clients[client.ID] = client
		query := "?device=device&session=session&password=device-secret"
		if admin {
			srv.AdminToken = "secret"
			query = "?device=device&session=session&token=secret"
		}
		server := httptest.NewServer(http.HandlerFunc(srv.HandleDesktopRTCWS))
		recorder := &rawWSRecorder{msgs: make(chan map[string]json.RawMessage, 8)}
		socket, _, err := gws.NewClient(recorder, &gws.ClientOption{Addr: "ws" + strings.TrimPrefix(server.URL, "http") + query, HandshakeTimeout: time.Second})
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		go socket.ReadLoop()
		waitRawMsg(t, recorder.msgs, "rtc_ready", func(msg map[string]json.RawMessage) bool { return string(msg["op"]) == `"rtc_ready"` })
		if err := socket.WriteMessage(gws.OpcodeText, []byte(`{"op":"rtc_signal","session":"forged","rtcType":"offer","rtcData":{"type":"offer","sdp":"test"}}`)); err != nil {
			t.Fatal(err)
		}
		select {
		case message := <-transport.sent:
			if message.Type != protocol.MsgDesktopRTCSignal || message.SessionID != "session" || message.RTCType != "offer" {
				t.Fatalf("unexpected relayed signal: %+v", message)
			}
		case <-time.After(time.Second):
			t.Fatal("no offer relayed")
		}
		srv.handleDesktopRTCSignal(client, &protocol.Message{SessionID: "session", RTCType: "answer", RTCData: json.RawMessage(`{"type":"answer","sdp":"test"}`)})
		waitRawMsg(t, recorder.msgs, "answer", func(msg map[string]json.RawMessage) bool { return string(msg["rtcType"]) == `"answer"` })
		_ = socket.WriteClose(1000, nil)
		select {
		case message := <-transport.sent:
			if message.RTCType != "close" || message.SessionID != "session" {
				t.Fatalf("unexpected cleanup: %+v", message)
			}
		case <-time.After(time.Second):
			t.Fatal("device not notified of RTC close")
		}
		server.Close()
	}
}

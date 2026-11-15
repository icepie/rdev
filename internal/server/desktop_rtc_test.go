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
	srv.WebRTCICEServers = []protocol.IceServer{{URLs: []string{"turn:relay.example:3478?transport=udp"}, Username: "user", Credential: "sensitive-rtc-secret"}}
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
				if res.Code != http.StatusUnauthorized || strings.Contains(res.Body.String(), "sensitive-rtc-secret") {
					t.Fatalf("unauthenticated status = %d or credential leaked", res.Code)
				}
				continue
			}
			var body struct {
				Enabled    bool                 `json:"enabled"`
				IceServers []protocol.IceServer `json:"iceServers"`
			}
			if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil || res.Code != http.StatusOK || body.Enabled != enabled || len(body.IceServers) != 1 || body.IceServers[0].Credential != "sensitive-rtc-secret" || len(body.IceServers[0].URLs) != 1 {
				t.Fatalf("enabled=%v response=%d (invalid ICE response)", enabled, res.Code)
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

func TestWebRTCICEServersConfiguration(t *testing.T) {
	configured, err := ParseWebRTCICEServers(`[{"urls":["stun:stun.example:3478","turns:relay.example:5349"],"username":"alice","credential":"private-token"}]`)
	if err != nil || len(configured) != 1 || len(configured[0].URLs) != 2 || configured[0].Username != "alice" || configured[0].Credential != "private-token" {
		t.Fatal("valid WebRTC ICE configuration was not parsed")
	}
	for _, input := range []string{``, `null`, `{}`, `[{}]`, `[{"urls":"stun:example"}]`, `[{"urls":[]}]`, `[{"urls":["invalid"]}]`, `[{"urls":["turn:relay"],"credential":1}]`, `[{"urls":["stun:example"],"extra":1}]`, `[] null`, `[{"urls":["turn:relay"],"credential":"private-token",}]`} {
		if _, err := ParseWebRTCICEServers(input); err == nil || strings.Contains(err.Error(), "private-token") {
			t.Fatal("invalid WebRTC ICE configuration accepted or secret included in error")
		}
	}
	for _, address := range []string{"stun:", "turn:host:invalid", "turn:host?transport=bogus", "https://relay.example", "stun:host:99999"} {
		raw, _ := json.Marshal([]protocol.IceServer{{URLs: []string{address}}})
		if _, err := ParseWebRTCICEServers(string(raw)); err == nil {
			t.Fatalf("invalid ICE URL accepted: %s", address)
		}
	}
	empty, err := ParseWebRTCICEServers(`[]`)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatal("host-only ICE configuration was not preserved")
	}
	srv := NewServer()
	if len(srv.webRTCICEServers()) != 1 || len(srv.webRTCICEServers()[0].URLs) != 1 {
		t.Fatal("default STUN configuration was lost")
	}
	srv.WebRTCICEServers = empty
	if servers := srv.rtcOfferICEServers("offer"); servers == nil || *servers == nil || len(*servers) != 0 {
		t.Fatal("offer did not retain explicit host-only ICE configuration")
	}
	if srv.rtcOfferICEServers("answer") != nil {
		t.Fatal("ICE config attached to non-offer signaling")
	}
	srv.AdminToken = "secret"
	request := httptest.NewRequest(http.MethodGet, "/api/desktop/rtc", nil)
	request.Header.Set("X-RDev-Token", "secret")
	response := httptest.NewRecorder()
	srv.HandleDesktopRTCAPI(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"iceServers":[]`) || !strings.Contains(response.Body.String(), `"enabled":false`) {
		t.Fatal("disabled host-only API response lost explicit empty ICE configuration")
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
		srv.WebRTCICEServers = []protocol.IceServer{}
		if admin {
			srv.WebRTCICEServers = []protocol.IceServer{{URLs: []string{"turn:relay.example:3478"}, Credential: "private-token"}}
		}
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
		if err := socket.WriteMessage(gws.OpcodeText, []byte(`{"op":"rtc_signal","session":"forged","rtcType":"offer","iceServers":[{"urls":["stun:attacker.invalid"]}],"rtcData":{"type":"offer","sdp":"test"}}`)); err != nil {
			t.Fatal(err)
		}
		select {
		case message := <-transport.sent:
			if message.Type != protocol.MsgDesktopRTCSignal || message.SessionID != "session" || message.RTCType != "offer" {
				t.Fatalf("unexpected relayed signal: %+v", message)
			}
			if message.IceServers == nil || len(*message.IceServers) != len(srv.WebRTCICEServers) {
				t.Fatal("authorized desktop offer omitted server ICE configuration")
			}
			if admin && ((*message.IceServers)[0].Credential != "private-token" || (*message.IceServers)[0].URLs[0] != "turn:relay.example:3478") {
				t.Fatal("browser ICE config overrode server desktop configuration")
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

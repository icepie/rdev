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

func TestClientOSCanonicalFallback(t *testing.T) {
	for _, tc := range []struct {
		name   string
		client *ClientConn
		want   string
	}{
		{"nil", nil, ""},
		{"empty", &ClientConn{}, ""},
		{"headless mac", &ClientConn{OS: "macos"}, "darwin"},
		{"case and whitespace", &ClientConn{OS: " MacOS "}, "darwin"},
		{"desktop mac", &ClientConn{Desktop: &protocol.DesktopCapabilities{Platform: "macos"}}, "darwin"},
		{"desktop windows", &ClientConn{OS: " ", Desktop: &protocol.DesktopCapabilities{Platform: "Win32"}}, "windows"},
		{"desktop android", &ClientConn{Desktop: &protocol.DesktopCapabilities{Platform: "Android"}}, "android"},
		{"registration wins", &ClientConn{OS: "linux", Desktop: &protocol.DesktopCapabilities{Platform: "android"}}, "linux"},
		{"other GOOS", &ClientConn{OS: "freebsd"}, "freebsd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := clientOS(tc.client); got != tc.want {
				t.Fatalf("OS=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestDeviceDisplayNameListsPreserveIDs(t *testing.T) {
	s := NewServer()
	s.clients["stable-id"] = &ClientConn{ID: "stable-id", DisplayName: "Office Mac", OS: "macos", Password: "secret"}
	s.clients["legacy"] = &ClientConn{ID: "legacy"}
	for _, handler := range []struct {
		name  string
		serve http.HandlerFunc
	}{
		{"clients", s.HandleAPI},
		{"terminal", s.HandleTerminalAPI},
		{"batch", s.HandleBatchDevicesAPI},
	} {
		t.Run(handler.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.serve(response, httptest.NewRequest(http.MethodGet, "/", nil))
			var devices []struct {
				ID          string `json:"id"`
				DisplayName string `json:"displayName"`
				OS          string `json:"os"`
				HasPassword bool   `json:"hasPassword"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &devices); err != nil {
				t.Fatal(err)
			}
			if len(devices) != 2 || devices[0].ID != "legacy" || devices[0].DisplayName != "legacy" {
				t.Fatalf("legacy metadata: %#v", devices)
			}
			if d := devices[1]; d.ID != "stable-id" || d.DisplayName != "Office Mac" || d.OS != "darwin" || !d.HasPassword {
				t.Fatalf("named metadata: %#v", d)
			}
		})
	}
}

func TestStreamDisplayNameReconnectPreservesRouting(t *testing.T) {
	s := NewServer()
	s.DataDir = t.TempDir()
	msg := &protocol.Message{
		Type:           protocol.MsgRegister,
		ClientID:       "stable-id",
		DisplayName:    "Office Mac",
		DeviceIdentity: strings.Repeat("a", 64),
		ReconnectProof: strings.Repeat("b", 64),
		Password:       "secret",
		ClientOS:       "macos",
	}
	for _, label := range []string{"tcp", "kcp"} {
		transport := &jobTestTransport{sent: make(chan protocol.Message, 4)}
		id, ok := s.registerStreamClient(transport, msg, label)
		if !ok || id != "stable-id" {
			t.Fatalf("registration id=%q ok=%v", id, ok)
		}
		ack := <-transport.sent
		if ack.ClientID != id || ack.DeviceIdentity != msg.DeviceIdentity || ack.ReconnectProof != "" {
			t.Fatalf("registration changed identity: %#v", ack)
		}
		client := s.clientByID(id)
		if client == nil || client.DisplayName != msg.DisplayName || client.Password != "secret" || client.ReconnectProof != msg.ReconnectProof || clientOS(client) != "darwin" {
			t.Fatalf("registered metadata: %#v", client)
		}
		if s.clientByID(msg.DisplayName) != nil || len(s.clients) != 1 {
			t.Fatal("display name became a route or allocated a new ID")
		}
		msg.DisplayName = "Renamed Mac"
	}
	s.unregisterClientTransport("stable-id", s.clientByID("stable-id").Transport)
}

type deviceRegistrationRecorder struct {
	gws.BuiltinEventHandler
	registered chan protocol.Message
}

func (r *deviceRegistrationRecorder) OnMessage(_ *gws.Conn, frame *gws.Message) {
	defer frame.Close()
	msg, err := protocol.Decode(frame.Bytes())
	if err == nil && msg.Type == protocol.MsgRegister {
		r.registered <- *msg
	}
}

func TestWebSocketDisplayNameRegistration(t *testing.T) {
	s := NewServer()
	s.DataDir = t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(s.HandleWS))
	defer server.Close()
	recorder := &deviceRegistrationRecorder{registered: make(chan protocol.Message, 1)}
	socket, _, err := gws.NewClient(recorder, &gws.ClientOption{
		Addr:             "ws" + strings.TrimPrefix(server.URL, "http"),
		HandshakeTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.WriteClose(1000, nil)
	go socket.ReadLoop()
	wire, err := protocol.Encode(&protocol.Message{
		Type:                protocol.MsgRegister,
		ClientID:            "stable-id",
		DisplayName:         "Office Mac",
		DeviceIdentity:      strings.Repeat("c", 64),
		ReconnectProof:      strings.Repeat("d", 64),
		DesktopCapabilities: &protocol.DesktopCapabilities{Platform: "macos"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := socket.WriteMessage(gws.OpcodeText, wire); err != nil {
		t.Fatal(err)
	}
	select {
	case ack := <-recorder.registered:
		client := s.clientByID(ack.ClientID)
		if ack.ClientID != "stable-id" || client == nil || client.DisplayName != "Office Mac" || clientOS(client) != "darwin" {
			t.Fatalf("websocket metadata: id=%q client=%#v", ack.ClientID, client)
		}
		if s.clientByID("Office Mac") != nil {
			t.Fatal("display name became a route")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("registration timed out")
	}
}

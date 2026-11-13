package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/lxzan/gws"
	"rdev/internal/protocol"
)

type desktopRTCConn struct {
	srv      *Server
	socket   *gws.Conn
	deviceID string
	session  string
	client   *ClientConn
	writeMu  sync.Mutex
	once     sync.Once
}

type desktopRTCWSHandler struct {
	gws.BuiltinEventHandler
	srv *Server
}

// HandleDesktopRTCAPI exposes the server opt-in without starting a peer.
func (s *Server) HandleDesktopRTCAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuth(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(struct {
		Enabled bool `json:"enabled"`
	}{Enabled: s.WebRTCEnabled})
}

// HandleDesktopRTCWS relays only signaling. Video may use the browser-device
// DataChannel once ICE connects; human controls retain the server WebSocket
// for authorization and Agent arbitration, with relayed video as fallback.
func (s *Server) HandleDesktopRTCWS(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuth(w, r) || !requireOrigin(w, r) {
		return
	}
	if !s.WebRTCEnabled {
		http.Error(w, "desktop WebRTC is disabled", http.StatusForbidden)
		return
	}
	deviceID := strings.TrimSpace(r.URL.Query().Get("device"))
	session := strings.TrimSpace(r.URL.Query().Get("session"))
	if deviceID == "" || session == "" || len(session) > 128 {
		http.Error(w, "device and session are required", http.StatusBadRequest)
		return
	}
	client := s.clientByID(deviceID)
	if client == nil {
		http.Error(w, "device is not connected", http.StatusNotFound)
		return
	}
	adminBypass := s.deviceAuthBypassed(r)
	if client.Password != "" && !adminBypass && !constantTimeEqual(passwordFingerprint(r.URL.Query().Get("password")), passwordFingerprint(client.Password)) {
		http.Error(w, "wrong device password", http.StatusUnauthorized)
		return
	}
	if client.Desktop == nil || !client.Desktop.RTC {
		http.Error(w, "device does not support desktop WebRTC", http.StatusConflict)
		return
	}
	upgrader := gws.NewUpgrader(&desktopRTCWSHandler{srv: s}, &gws.ServerOption{
		ReadMaxPayloadSize: 1024 * 1024,
		Authorize: func(_ *http.Request, storage gws.SessionStorage) bool {
			storage.Store("deviceID", deviceID)
			storage.Store("session", session)
			storage.Store("client", client)
			return true
		},
	})
	socket, err := upgrader.Upgrade(w, r)
	if err != nil {
		return
	}
	socket.ReadLoop()
}

func (h *desktopRTCWSHandler) OnOpen(socket *gws.Conn) {
	deviceIDValue, _ := socket.Session().Load("deviceID")
	sessionValue, _ := socket.Session().Load("session")
	deviceID, deviceOK := deviceIDValue.(string)
	session, sessionOK := sessionValue.(string)
	if !deviceOK || !sessionOK || deviceID == "" || session == "" {
		_ = socket.WriteClose(1008, []byte("invalid desktop signaling session"))
		return
	}
	client := h.srv.clientByID(deviceID)
	expectedClient, _ := socket.Session().Load("client")
	if !h.srv.WebRTCEnabled || client == nil || client != expectedClient || client.Desktop == nil || !client.Desktop.RTC {
		_ = socket.WriteClose(1008, []byte("desktop WebRTC is unavailable"))
		return
	}
	conn := &desktopRTCConn{srv: h.srv, socket: socket, deviceID: deviceID, session: session, client: client}
	h.srv.desktopMu.Lock()
	route := h.srv.desktops[session]
	if route != nil && route.clientID != "" && route.clientID != deviceID {
		h.srv.desktopMu.Unlock()
		_ = socket.WriteClose(1008, []byte("desktop session belongs to another device"))
		return
	}
	if route == nil {
		route = &desktopRoute{id: session, clientID: deviceID}
		h.srv.desktops[session] = route
	}
	if route.rtc != nil {
		h.srv.desktopMu.Unlock()
		_ = socket.WriteClose(1008, []byte("desktop signaling session already attached"))
		return
	}
	route.rtc = conn
	h.srv.desktopMu.Unlock()
	_ = conn.writeJSON(desktopMsg{Op: "rtc_ready", Device: deviceID, Session: session})
}

func (h *desktopRTCWSHandler) OnMessage(socket *gws.Conn, message *gws.Message) {
	defer message.Close()
	if message.Opcode != gws.OpcodeText {
		return
	}
	var msg desktopMsg
	if json.Unmarshal(message.Bytes(), &msg) != nil || msg.Op != "rtc_signal" {
		return
	}
	conn := h.srv.desktopRTCConnFromSocket(socket)
	if !h.srv.WebRTCEnabled || conn == nil || conn.client.Desktop == nil || !conn.client.Desktop.RTC || !validDesktopRTCSignal(msg.RTCType, msg.RTCData) {
		return
	}
	_ = conn.client.Send(&protocol.Message{
		Type: protocol.MsgDesktopRTCSignal, SessionID: conn.session,
		RTCType: msg.RTCType, RTCData: append(json.RawMessage(nil), msg.RTCData...),
	})
}

func (h *desktopRTCWSHandler) OnClose(socket *gws.Conn, _ error) {
	if conn := h.srv.desktopRTCConnFromSocket(socket); conn != nil {
		conn.close(true)
	}
}

func validDesktopRTCSignal(kind string, data json.RawMessage) bool {
	switch kind {
	case "offer", "answer", "candidate":
		return len(data) > 0 && len(data) <= 64*1024 && json.Valid(data)
	case "close":
		return len(data) == 0 || string(data) == "null"
	default:
		return false
	}
}

func (s *Server) desktopRTCConnFromSocket(socket *gws.Conn) *desktopRTCConn {
	deviceValue, _ := socket.Session().Load("deviceID")
	sessionValue, _ := socket.Session().Load("session")
	deviceID, deviceOK := deviceValue.(string)
	session, sessionOK := sessionValue.(string)
	if !deviceOK || !sessionOK {
		return nil
	}
	s.desktopMu.RLock()
	route := s.desktops[session]
	var conn *desktopRTCConn
	if route != nil && route.clientID == deviceID && route.rtc != nil && route.rtc.socket == socket {
		conn = route.rtc
	}
	s.desktopMu.RUnlock()
	return conn
}

func (c *desktopRTCConn) writeJSON(msg desktopMsg) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.socket.WriteMessage(gws.OpcodeText, data)
}

func (c *desktopRTCConn) close(notifyDevice bool) {
	c.once.Do(func() {
		if notifyDevice {
			_ = c.client.Send(&protocol.Message{Type: protocol.MsgDesktopRTCSignal, SessionID: c.session, RTCType: "close"})
		}
		c.srv.desktopMu.Lock()
		if route := c.srv.desktops[c.session]; route != nil && route.rtc == c {
			route.rtc = nil
			if route.conn == nil && route.vnc == nil && route.stream == nil && route.automation == nil {
				delete(c.srv.desktops, c.session)
			}
		}
		c.srv.desktopMu.Unlock()
		_ = c.socket.WriteClose(1000, nil)
	})
}

func (s *Server) handleDesktopRTCSignal(client *ClientConn, msg *protocol.Message) {
	if !s.WebRTCEnabled || client == nil || client.Desktop == nil || !client.Desktop.RTC || msg.SessionID == "" || !validDesktopRTCSignal(msg.RTCType, msg.RTCData) {
		return
	}
	s.desktopMu.RLock()
	route := s.desktops[msg.SessionID]
	var rtc *desktopRTCConn
	if route != nil && route.clientID == client.ID {
		rtc = route.rtc
	}
	s.desktopMu.RUnlock()
	if rtc == nil || rtc.client != client {
		return
	}
	if msg.RTCType == "close" {
		rtc.close(false)
		return
	}
	_ = rtc.writeJSON(desktopMsg{Op: "rtc_signal", Session: msg.SessionID, RTCType: msg.RTCType, RTCData: append(json.RawMessage(nil), msg.RTCData...)})
}

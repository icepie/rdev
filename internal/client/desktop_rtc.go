package client

import (
	"encoding/json"

	"github.com/pion/webrtc/v4"
	"rdev/internal/protocol"
)

type desktopRTCSession struct {
	rtcPeer
	client    *Client
	sessionID string
	closed    bool
}

func (s *desktopSession) closeRTC() {
	s.rtcMu.Lock()
	rtc := s.rtc
	s.rtc = nil
	s.rtcMu.Unlock()
	if rtc != nil {
		rtc.close()
	}
}

func (s *desktopSession) sendFrame(payload []byte) bool {
	s.rtcMu.Lock()
	rtc := s.rtc
	s.rtcMu.Unlock()
	return rtc != nil && rtc.send(payload)
}

func (c *Client) handleDesktopRTCSignal(msg *protocol.Message) {
	if msg == nil || msg.SessionID == "" {
		return
	}
	c.mu.Lock()
	session := c.desktopSessions[msg.SessionID]
	c.mu.Unlock()
	if session == nil {
		return
	}

	session.rtcMu.Lock()
	if session.stopped() {
		session.rtcMu.Unlock()
		return
	}
	rtc := session.rtc
	if msg.RTCType == "offer" {
		if rtc != nil {
			session.rtcMu.Unlock()
			return
		}
		rtc = &desktopRTCSession{client: c, sessionID: msg.SessionID}
		session.rtc = rtc
	}
	session.rtcMu.Unlock()
	if msg.RTCType == "close" {
		session.closeRTC()
		return
	}
	if rtc == nil {
		return
	}

	switch msg.RTCType {
	case "offer":
		rtc.acceptOffer(msg.RTCData, msg.IceServers)
	case "candidate":
		var candidate webrtc.ICECandidateInit
		if len(msg.RTCData) == 0 || json.Unmarshal(msg.RTCData, &candidate) != nil {
			return
		}
		rtc.addCandidate(candidate)
	}
}

func (r *desktopRTCSession) acceptOffer(raw json.RawMessage, iceServers *[]protocol.IceServer) {
	var offer webrtc.SessionDescription
	if len(raw) == 0 || json.Unmarshal(raw, &offer) != nil || offer.Type != webrtc.SDPTypeOffer {
		r.close()
		return
	}
	peer, err := newRTCPeer(iceServers)
	if err != nil {
		r.close()
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		_ = peer.Close()
		return
	}
	r.peer = peer
	r.mu.Unlock()
	r.wire(r.sendSignal, r.acceptChannel, r.close)
	answer, err := r.acceptRemote(offer)
	if err != nil {
		r.close()
		return
	}
	r.sendSignal("answer", answer)
}

// acceptChannel keeps only the desktop media channel; human input and
// clipboard stay on the server-arbitrated desktop socket.
func (r *desktopRTCSession) acceptChannel(channel *webrtc.DataChannel) {
	if channel.Label() != "rdev-desktop" {
		_ = channel.Close()
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		_ = channel.Close()
		return
	}
	r.channel = channel
	r.mu.Unlock()
}

func (r *desktopRTCSession) addCandidate(candidate webrtc.ICECandidateInit) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	peer := r.bufferCandidateLocked(candidate)
	r.mu.Unlock()
	if peer != nil {
		if err := peer.AddICECandidate(candidate); err != nil {
			r.close()
		}
	}
}

func (r *desktopRTCSession) sendSignal(kind string, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		return
	}
	_ = r.client.send(&protocol.Message{
		Type: protocol.MsgDesktopRTCSignal, SessionID: r.sessionID,
		RTCType: kind, RTCData: raw,
	})
}

func (r *desktopRTCSession) send(payload []byte) bool {
	r.mu.Lock()
	channel := r.channel
	closed := r.closed
	r.mu.Unlock()
	if closed || channel == nil || channel.ReadyState() != webrtc.DataChannelStateOpen {
		return false
	}
	if err := channel.Send(payload); err != nil {
		return false
	}
	return true
}

func (r *desktopRTCSession) close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	peer := r.peer
	channel := r.channel
	r.peer = nil
	r.channel = nil
	r.pending = nil
	r.mu.Unlock()
	if channel != nil {
		_ = channel.Close()
	}
	if peer != nil {
		_ = peer.Close()
	}
}

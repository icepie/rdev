package client

import (
	"encoding/json"
	"sync"

	"github.com/pion/webrtc/v4"
	"rdev/internal/protocol"
)

type desktopRTCSession struct {
	client    *Client
	sessionID string
	peer      *webrtc.PeerConnection

	mu                sync.Mutex
	channel           *webrtc.DataChannel
	remoteDescription bool
	pendingCandidates []webrtc.ICECandidateInit
	closed            bool
}

func (s *desktopSession) setRTC(rtc *desktopRTCSession) {
	s.rtcMu.Lock()
	old := s.rtc
	s.rtc = rtc
	s.rtcMu.Unlock()
	if old != nil && old != rtc {
		old.close()
	}
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
		rtc.acceptOffer(msg.RTCData)
	case "candidate":
		var candidate webrtc.ICECandidateInit
		if len(msg.RTCData) == 0 || json.Unmarshal(msg.RTCData, &candidate) != nil {
			return
		}
		rtc.addCandidate(candidate)
	}
}

func (r *desktopRTCSession) acceptOffer(raw json.RawMessage) {
	var offer webrtc.SessionDescription
	if len(raw) == 0 || json.Unmarshal(raw, &offer) != nil || offer.Type != webrtc.SDPTypeOffer {
		r.close()
		return
	}
	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}},
	})
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

	peer.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			return
		}
		r.sendSignal("candidate", candidate.ToJSON())
	})
	peer.OnDataChannel(func(channel *webrtc.DataChannel) {
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
		// Human input and clipboard stay on the server-arbitrated desktop socket.
	})
	peer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			r.close()
		}
	})

	if err := peer.SetRemoteDescription(offer); err != nil {
		r.close()
		return
	}
	r.mu.Lock()
	r.remoteDescription = true
	pending := append([]webrtc.ICECandidateInit(nil), r.pendingCandidates...)
	r.pendingCandidates = nil
	r.mu.Unlock()
	for _, candidate := range pending {
		if err := peer.AddICECandidate(candidate); err != nil {
			r.close()
			return
		}
	}
	answer, err := peer.CreateAnswer(nil)
	if err != nil {
		r.close()
		return
	}
	if err := peer.SetLocalDescription(answer); err != nil {
		r.close()
		return
	}
	r.sendSignal("answer", answer)
}

func (r *desktopRTCSession) addCandidate(candidate webrtc.ICECandidateInit) {
	r.mu.Lock()
	peer := r.peer
	if r.closed {
		r.mu.Unlock()
		return
	}
	if peer == nil || !r.remoteDescription {
		r.pendingCandidates = append(r.pendingCandidates, candidate)
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()
	if err := peer.AddICECandidate(candidate); err != nil {
		r.close()
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
	r.pendingCandidates = nil
	r.mu.Unlock()
	if channel != nil {
		_ = channel.Close()
	}
	if peer != nil {
		_ = peer.Close()
	}
}

package client

import (
	"sync"

	"github.com/pion/webrtc/v4"
	"rdev/internal/protocol"
)

// rtcICEServers keeps old servers' STUN default while honoring explicit []
// from a configured server (host candidates only).
func rtcICEServers(config *[]protocol.IceServer) []webrtc.ICEServer {
	if config == nil {
		return []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}}
	}
	servers := make([]webrtc.ICEServer, len(*config))
	for i, server := range *config {
		servers[i] = webrtc.ICEServer{URLs: server.URLs, Username: server.Username, Credential: server.Credential}
	}
	return servers
}

func newRTCPeer(iceServers *[]protocol.IceServer) (*webrtc.PeerConnection, error) {
	return webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: rtcICEServers(iceServers)})
}

// rtcPeer is the negotiation state both direct transports share: the pion
// connection, the single data channel the remote opens, and the ICE
// candidates that arrive before the remote description can accept them.
// Close guards, channel policy, framing and liveness stay on the sessions.
type rtcPeer struct {
	mu      sync.Mutex
	peer    *webrtc.PeerConnection
	channel *webrtc.DataChannel
	remote  bool
	pending []webrtc.ICECandidateInit
}

// wire installs the trickle-ICE, failure and channel handlers both roles
// need. Signalling payloads remain role-specific.
func (p *rtcPeer) wire(signal func(kind string, data any), onChannel func(*webrtc.DataChannel), failed func()) {
	p.peer.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate != nil {
			signal("candidate", candidate.ToJSON())
		}
	})
	p.peer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			failed()
		}
	})
	p.peer.OnDataChannel(onChannel)
}

// bufferCandidateLocked queues candidate until the remote description exists,
// otherwise returning the peer for the caller to apply once unlocked.
// Callers hold p.mu and check their own close guard first.
func (p *rtcPeer) bufferCandidateLocked(candidate webrtc.ICECandidateInit) *webrtc.PeerConnection {
	if p.peer == nil || !p.remote {
		p.pending = append(p.pending, candidate)
		return nil
	}
	return p.peer
}

// acceptRemote applies the remote offer, drains buffered ICE and returns the
// local answer. Callers signal it themselves so each role keeps its own offer
// metadata and message shape.
func (p *rtcPeer) acceptRemote(offer webrtc.SessionDescription) (webrtc.SessionDescription, error) {
	if err := p.peer.SetRemoteDescription(offer); err != nil {
		return webrtc.SessionDescription{}, err
	}
	p.mu.Lock()
	p.remote = true
	pending := p.pending
	p.pending = nil
	p.mu.Unlock()
	for _, candidate := range pending {
		if err := p.peer.AddICECandidate(candidate); err != nil {
			return webrtc.SessionDescription{}, err
		}
	}
	answer, err := p.peer.CreateAnswer(nil)
	if err != nil {
		return webrtc.SessionDescription{}, err
	}
	if err := p.peer.SetLocalDescription(answer); err != nil {
		return webrtc.SessionDescription{}, err
	}
	return answer, nil
}

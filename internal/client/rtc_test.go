package client

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"rdev/internal/protocol"
)

// loopbackPeer builds the browser side of a direct peer connection. Empty ICE
// servers keep gathering on host candidates only, so the handshake never
// leaves the machine.
func loopbackPeer(t *testing.T, label string) (*webrtc.PeerConnection, *webrtc.DataChannel, <-chan struct{}) {
	t.Helper()
	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: []webrtc.ICEServer{}})
	if err != nil {
		t.Fatalf("new peer: %v", err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	channel, err := peer.CreateDataChannel(label, nil)
	if err != nil {
		t.Fatalf("create data channel: %v", err)
	}
	opened := make(chan struct{}, 1)
	channel.OnOpen(func() {
		select {
		case opened <- struct{}{}:
		default:
		}
	})
	return peer, channel, opened
}

func nextRTCMessage(t *testing.T, tr *uploadTestTransport) protocol.Message {
	t.Helper()
	select {
	case m := <-tr.messages:
		return m
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for RTC signal")
		return protocol.Message{}
	}
}

func waitOpened(t *testing.T, opened <-chan struct{}) {
	t.Helper()
	select {
	case <-opened:
	case <-time.After(15 * time.Second):
		t.Fatal("data channel did not open")
	}
}

// TestRTCPeerHandshakeOverLoopback drives the full offer/answer/candidate
// exchange between a browser-shaped pion peer and the client's session
// object, then carries one message over the ordered data channel before
// closing. It is the behaviour contract for both direct transports.
func TestRTCPeerHandshakeOverLoopback(t *testing.T) {
	t.Run("file-upload", func(t *testing.T) {
		c, tr := newUploadTestClient()
		path := filepath.Join(t.TempDir(), "received.bin")
		payload := []byte("rtc-over-loopback")
		peer, channel, opened := loopbackPeer(t, "rdev-file")

		offer, err := peer.CreateOffer(nil)
		if err != nil {
			t.Fatalf("create offer: %v", err)
		}
		offerData, err := json.Marshal(offer)
		if err != nil {
			t.Fatalf("marshal offer: %v", err)
		}
		empty := []protocol.IceServer{}
		c.startFileTransfer(&protocol.Message{
			Type: protocol.MsgFileUploadStart, TaskID: "task", Path: path,
			Size: int64(len(payload)), SHA256: transferDigest(string(payload)),
			RTCType: "offer", RTCData: offerData, IceServers: &empty,
		})
		session := c.fileRTCFor("task")
		if session == nil {
			t.Fatal("file RTC session was not registered")
		}
		peer.OnICECandidate(func(candidate *webrtc.ICECandidate) {
			if candidate == nil {
				return
			}
			raw, _ := json.Marshal(candidate.ToJSON())
			c.handleFileRTCSignal(&protocol.Message{TaskID: "task", RTCType: "candidate", RTCData: raw})
		})
		if err := peer.SetLocalDescription(offer); err != nil {
			t.Fatalf("set local description: %v", err)
		}

		answered, ready := false, false
		// Trickled candidates can arrive before the answer; pion refuses them
		// until the remote description exists, so hold them back.
		var pendingCandidates []webrtc.ICECandidateInit
		for !answered || !ready {
			m := nextRTCMessage(t, tr)
			switch {
			case m.Type == protocol.MsgFileRTCSignal && m.RTCType == "candidate":
				var candidate webrtc.ICECandidateInit
				if err := json.Unmarshal(m.RTCData, &candidate); err != nil {
					t.Fatalf("decode candidate: %v", err)
				}
				if !answered {
					pendingCandidates = append(pendingCandidates, candidate)
					continue
				}
				if err := peer.AddICECandidate(candidate); err != nil {
					t.Fatalf("add candidate: %v", err)
				}
			case m.Type == protocol.MsgFileRTCSignal && m.RTCType == "answer":
				var answer fileRTCAnswer
				if err := json.Unmarshal(m.RTCData, &answer); err != nil {
					t.Fatalf("decode answer: %v", err)
				}
				if err := peer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer.SDP}); err != nil {
					t.Fatalf("set remote description: %v", err)
				}
				answered = true
				for _, pending := range pendingCandidates {
					if err := peer.AddICECandidate(pending); err != nil {
						t.Fatalf("add buffered candidate: %v", err)
					}
				}
				pendingCandidates = nil
			case m.Type == protocol.MsgFileUploadReady:
				ready = true
			case m.Type == protocol.MsgFileRTCSignal && m.RTCType == "close":
				t.Fatalf("peer closed during negotiation")
			}
		}
		waitOpened(t, opened)

		if err := channel.Send(protocol.EncodeBinFrameOffset(protocol.BinFileUploadChunk, "", 0, payload)); err != nil {
			t.Fatalf("send chunk: %v", err)
		}
		deadline := time.Now().Add(15 * time.Second)
		for {
			part, err := os.ReadFile(path + ".rdevpart")
			if err == nil && bytes.Equal(part, payload) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("chunk was not received: err=%v data=%q", err, part)
			}
			time.Sleep(10 * time.Millisecond)
		}

		session.mu.Lock()
		adopted := session.channel != nil && session.channel.Ordered()
		session.mu.Unlock()
		if !adopted {
			t.Fatal("session did not adopt the ordered file channel")
		}

		session.close(true)
		for c.fileRTCFor("task") != nil {
			if time.Now().After(deadline) {
				t.Fatal("closed file session stayed registered")
			}
			time.Sleep(10 * time.Millisecond)
		}
	})

	t.Run("desktop", func(t *testing.T) {
		c, tr := newUploadTestClient()
		peer, channel, opened := loopbackPeer(t, "rdev-desktop")
		received := make(chan []byte, 1)
		channel.OnMessage(func(message webrtc.DataChannelMessage) {
			if !message.IsString {
				select {
				case received <- message.Data:
				default:
				}
			}
		})

		offer, err := peer.CreateOffer(nil)
		if err != nil {
			t.Fatalf("create offer: %v", err)
		}
		offerData, err := json.Marshal(offer)
		if err != nil {
			t.Fatalf("marshal offer: %v", err)
		}
		session := &desktopRTCSession{client: c, sessionID: "desktop"}
		peer.OnICECandidate(func(candidate *webrtc.ICECandidate) {
			if candidate == nil {
				return
			}
			session.addCandidate(candidate.ToJSON())
		})
		empty := []protocol.IceServer{}
		session.acceptOffer(offerData, &empty)
		if err := peer.SetLocalDescription(offer); err != nil {
			t.Fatalf("set local description: %v", err)
		}

		answered := false
		// Trickled candidates can arrive before the answer; pion refuses them
		// until the remote description exists, so hold them back.
		var pendingCandidates []webrtc.ICECandidateInit
		for !answered {
			m := nextRTCMessage(t, tr)
			if m.Type != protocol.MsgDesktopRTCSignal {
				continue
			}
			switch m.RTCType {
			case "candidate":
				var candidate webrtc.ICECandidateInit
				if err := json.Unmarshal(m.RTCData, &candidate); err != nil {
					t.Fatalf("decode candidate: %v", err)
				}
				if !answered {
					pendingCandidates = append(pendingCandidates, candidate)
					continue
				}
				if err := peer.AddICECandidate(candidate); err != nil {
					t.Fatalf("add candidate: %v", err)
				}
			case "answer":
				var answer webrtc.SessionDescription
				if err := json.Unmarshal(m.RTCData, &answer); err != nil {
					t.Fatalf("decode answer: %v", err)
				}
				if err := peer.SetRemoteDescription(answer); err != nil {
					t.Fatalf("set remote description: %v", err)
				}
				answered = true
				for _, pending := range pendingCandidates {
					if err := peer.AddICECandidate(pending); err != nil {
						t.Fatalf("add buffered candidate: %v", err)
					}
				}
				pendingCandidates = nil
			default:
				t.Fatalf("unexpected signal during negotiation: %+v", m)
			}
		}
		waitOpened(t, opened)

		frame := []byte("desktop-frame")
		if !session.send(frame) {
			t.Fatal("desktop session refused to send over the open channel")
		}
		select {
		case got := <-received:
			if !bytes.Equal(got, frame) {
				t.Fatalf("received %q, want %q", got, frame)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("frame was not received")
		}

		session.close()
		session.mu.Lock()
		closed, channelNil := session.closed, session.channel == nil
		session.mu.Unlock()
		if !closed || !channelNil {
			t.Fatalf("close left closed=%v channel=%v", closed, channelNil)
		}
	})
}

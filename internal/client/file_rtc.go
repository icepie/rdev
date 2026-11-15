package client

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"
	"rdev/internal/protocol"
)

const fileRTCChunkSize = 16 * 1024
const fileRTCHighWater = 512 * 1024
const fileRTCRangeAck byte = 0x25

func rtcRangeAckOffered(raw json.RawMessage) bool {
	var offer struct {
		RangeAck bool `json:"rangeAck"`
	}
	return json.Unmarshal(raw, &offer) == nil && offer.RangeAck
}

type fileRTCAnswer struct {
	Type     string `json:"type"`
	SDP      string `json:"sdp"`
	RangeAck bool   `json:"rangeAck,omitempty"`
}

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

// A peer has exactly one authorized operation. It cannot receive paths,
// controls, or chunks for any other task. The control transport owns its life.
type fileRTCSession struct {
	client       *Client
	start        protocol.Message
	transport    clientTransport
	mu           sync.Mutex
	startMu      sync.Mutex
	sendMu       sync.Mutex
	peer         *webrtc.PeerConnection
	channel      *webrtc.DataChannel
	pending      []webrtc.ICECandidateInit
	remote       bool
	opened       bool
	candidates   int
	closed       atomic.Bool
	lastActivity atomic.Int64
	terminal     atomic.Bool
	awaitingAck  bool // guarded by mu; one bounded-download segment at a time
	rangeAck     bool
	expectedAck  int64
	ack          chan int64
	done         chan struct{}
	wake         chan struct{}
}

func (c *Client) fileRTCFor(id string) *fileRTCSession {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fileRTC[id]
}

func (c *Client) startFileTransfer(msg *protocol.Message) {
	if msg.RTCType != "offer" {
		if msg.Type == protocol.MsgFileUploadStart {
			go c.handleManagedUploadStart(msg)
		} else {
			go c.handleManagedDownloadStart(msg)
		}
		return
	}
	if msg.TaskID == "" {
		return
	}
	c.mu.Lock()
	if c.transport == nil || c.fileRTC[msg.TaskID] != nil || len(c.fileRTC) >= 32 {
		c.mu.Unlock()
		_ = c.send(&protocol.Message{Type: protocol.MsgFileRTCSignal, TaskID: msg.TaskID, RTCType: "close"})
		return
	}
	rangeAck := msg.Type == protocol.MsgFileDownloadStart && msg.Length > 0 && rtcRangeAckOffered(msg.RTCData)
	r := &fileRTCSession{client: c, start: *msg, transport: c.transport, rangeAck: rangeAck, done: make(chan struct{}), wake: make(chan struct{}, 1)}
	if rangeAck {
		r.ack = make(chan int64, 1)
	}
	r.lastActivity.Store(time.Now().UnixNano())
	if c.fileRTC == nil {
		c.fileRTC = make(map[string]*fileRTCSession)
	}
	c.fileRTC[msg.TaskID] = r
	c.mu.Unlock()
	go r.watchdog()
	go r.acceptOffer()
}

func (c *Client) handleFileRTCSignal(msg *protocol.Message) {
	r := c.fileRTCFor(msg.TaskID)
	if r == nil {
		return
	}
	switch msg.RTCType {
	case "close":
		r.close(false)
	case "candidate":
		var candidate webrtc.ICECandidateInit
		if len(msg.RTCData) > 8*1024 || json.Unmarshal(msg.RTCData, &candidate) != nil {
			r.close(true)
			return
		}
		r.mu.Lock()
		if r.closed.Load() {
			r.mu.Unlock()
			return
		}
		r.candidates++
		if r.candidates > 128 {
			r.mu.Unlock()
			r.close(true)
			return
		}
		peer := r.peer
		if peer == nil || !r.remote {
			r.pending = append(r.pending, candidate)
			r.mu.Unlock()
			return
		}
		r.mu.Unlock()
		if peer.AddICECandidate(candidate) != nil {
			r.close(true)
		}
	}
}

func (r *fileRTCSession) acceptOffer() {
	var offer webrtc.SessionDescription
	if len(r.start.RTCData) > 64*1024 || json.Unmarshal(r.start.RTCData, &offer) != nil || offer.Type != webrtc.SDPTypeOffer {
		r.close(true)
		return
	}
	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: rtcICEServers(r.start.IceServers)})
	if err != nil {
		r.close(true)
		return
	}
	r.mu.Lock()
	if r.closed.Load() {
		r.mu.Unlock()
		_ = peer.Close()
		return
	}
	r.peer = peer
	r.mu.Unlock()
	peer.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate != nil {
			r.signal("candidate", candidate.ToJSON())
		}
	})
	peer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			r.close(true)
		}
	})
	peer.OnDataChannel(r.acceptChannel)
	if peer.SetRemoteDescription(offer) != nil {
		r.close(true)
		return
	}
	r.mu.Lock()
	r.remote = true
	pending := r.pending
	r.pending = nil
	r.mu.Unlock()
	for _, candidate := range pending {
		if peer.AddICECandidate(candidate) != nil {
			r.close(true)
			return
		}
	}
	answer, err := peer.CreateAnswer(nil)
	if err != nil || peer.SetLocalDescription(answer) != nil {
		r.close(true)
		return
	}
	r.signal("answer", fileRTCAnswer{Type: answer.Type.String(), SDP: answer.SDP, RangeAck: r.rangeAck})
}

func (r *fileRTCSession) acceptChannel(channel *webrtc.DataChannel) {
	if channel.Label() != "rdev-file" || !channel.Ordered() || channel.MaxRetransmits() != nil || channel.MaxPacketLifeTime() != nil {
		_ = channel.Close()
		r.close(true)
		return
	}
	r.mu.Lock()
	if r.closed.Load() || r.channel != nil {
		r.mu.Unlock()
		_ = channel.Close()
		return
	}
	r.channel = channel
	r.mu.Unlock()
	channel.SetBufferedAmountLowThreshold(fileRTCHighWater / 2)
	channel.OnBufferedAmountLow(func() {
		select {
		case r.wake <- struct{}{}:
		default:
		}
	})
	channel.OnClose(func() { r.close(true) })
	channel.OnError(func(error) { r.close(true) })
	channel.OnMessage(func(message webrtc.DataChannelMessage) {
		if r.closed.Load() {
			return
		}
		if r.start.Type == protocol.MsgFileDownloadStart {
			if !r.rangeAck || !r.downloadRangeAck(message) {
				r.close(true)
			}
			return
		}
		offset, payload, ok := r.uploadFrame(message)
		if !ok {
			r.close(true)
			return
		}
		r.lastActivity.Store(time.Now().UnixNano())
		r.client.handleManagedUploadChunk(r.start.TaskID, offset, payload)
	})
	channel.OnOpen(func() {
		r.mu.Lock()
		if r.opened || r.closed.Load() {
			r.mu.Unlock()
			return
		}
		r.opened = true
		r.mu.Unlock()
		r.lastActivity.Store(time.Now().UnixNano())
		// One worker per transfer; callbacks never spawn per-frame work.
		go func() {
			if r.start.Type == protocol.MsgFileUploadStart {
				r.startMu.Lock()
				if !r.closed.Load() {
					r.client.handleManagedUploadStart(&r.start)
				}
				r.startMu.Unlock()
			} else if !r.closed.Load() {
				r.client.handleManagedDownloadStart(&r.start)
			}
		}()
	})
}

func (r *fileRTCSession) uploadFrame(message webrtc.DataChannelMessage) (int64, []byte, bool) {
	if message.IsString || r.start.Type != protocol.MsgFileUploadStart || len(message.Data) > fileRTCChunkSize+265 {
		return 0, nil, false
	}
	typ, id, offset, payload, err := protocol.DecodeBinFrameOffset(message.Data)
	localID := r.start.TaskID[strings.IndexByte(r.start.TaskID, ':')+1:]
	return offset, payload, err == nil && typ == protocol.BinFileUploadChunk && (id == "" || id == localID) && offset >= 0 && len(payload) <= fileRTCChunkSize
}

func (r *fileRTCSession) downloadRangeAck(message webrtc.DataChannelMessage) bool {
	if message.IsString || len(message.Data) != 10 || message.Data[0] != fileRTCRangeAck {
		return false
	}
	typ, id, offset, payload, err := protocol.DecodeBinFrameOffset(message.Data)
	if err != nil || typ != fileRTCRangeAck || id != "" || len(payload) != 0 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.awaitingAck || offset != r.expectedAck || r.closed.Load() || r.ack == nil {
		return false
	}
	r.awaitingAck = false
	r.terminal.Store(false)
	r.lastActivity.Store(time.Now().UnixNano())
	select {
	case r.ack <- offset:
		return true
	default:
		return false
	}
}

func (r *fileRTCSession) expectRangeAck(end int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.rangeAck || r.closed.Load() || r.awaitingAck {
		return errors.New("invalid file RTC range acknowledgement state")
	}
	r.awaitingAck = true
	r.expectedAck = end
	r.terminal.Store(true) // disk commit may take a terminal-like watchdog window
	r.lastActivity.Store(time.Now().UnixNano())
	return nil
}

func (r *fileRTCSession) waitRangeAck(end int64) error {
	select {
	case <-r.done:
		return errors.New("file RTC closed")
	case offset := <-r.ack:
		if offset != end {
			r.close(true)
			return errors.New("invalid file RTC range acknowledgement offset")
		}
		return nil
	}
}

func (r *fileRTCSession) signal(kind string, data any) {
	if r.closed.Load() {
		return
	}
	raw, _ := json.Marshal(data)
	_ = r.client.send(&protocol.Message{Type: protocol.MsgFileRTCSignal, TaskID: r.start.TaskID, RTCType: kind, RTCData: raw})
}

func (r *fileRTCSession) send(typ byte, offset int64, payload []byte) error {
	r.sendMu.Lock()
	defer r.sendMu.Unlock()
	r.mu.Lock()
	channel := r.channel
	r.mu.Unlock()
	if channel == nil {
		return errors.New("file RTC channel unavailable")
	}
	for {
		n := min(len(payload), fileRTCChunkSize)
		frame := protocol.EncodeBinFrameOffset(typ, "", offset, payload[:n])
		for channel.BufferedAmount()+uint64(len(frame)) > fileRTCHighWater {
			select {
			case <-r.done:
				return errors.New("file RTC closed")
			case <-r.wake:
			}
		}
		if r.closed.Load() {
			return errors.New("file RTC closed")
		}
		if err := channel.Send(frame); err != nil {
			r.close(true)
			return err
		}
		r.lastActivity.Store(time.Now().UnixNano())
		payload = payload[n:]
		offset += int64(n)
		if len(payload) == 0 {
			return nil
		}
	}
}

func (r *fileRTCSession) completed() {
	r.lastActivity.Store(time.Now().UnixNano())
	r.terminal.Store(true)
}

func (r *fileRTCSession) watchdog() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.done:
			return
		case <-ticker.C:
			r.mu.Lock()
			opened := r.opened
			r.mu.Unlock()
			limit := 5 * time.Second
			if opened {
				limit = 15 * time.Second
			}
			if r.terminal.Load() {
				limit = 30 * time.Second
			}
			if time.Since(time.Unix(0, r.lastActivity.Load())) > limit {
				r.close(true)
				return
			}
		}
	}
}

func (r *fileRTCSession) close(notify bool) {
	if !r.closed.CompareAndSwap(false, true) {
		return
	}
	close(r.done) // unblock backpressure before waiting on file operations
	r.mu.Lock()
	peer := r.peer
	r.pending = nil
	r.mu.Unlock()
	// Pion invokes callbacks on its read loop; close asynchronously once, not
	// from that loop. Cancel runs first so a relay resume cannot race file I/O.
	go func() {
		r.startMu.Lock()
		r.client.cancelFileOperations(r.start.TaskID)
		r.startMu.Unlock()
		if notify {
			_ = r.client.send(&protocol.Message{Type: protocol.MsgFileRTCSignal, TaskID: r.start.TaskID, RTCType: "close"})
		}
		if peer != nil {
			_ = peer.Close()
		}
		r.client.mu.Lock()
		if r.client.fileRTC[r.start.TaskID] == r {
			delete(r.client.fileRTC, r.start.TaskID)
		}
		r.client.mu.Unlock()
	}()
}

func (c *Client) closeFileRTCSessions() {
	c.mu.Lock()
	peers := make([]*fileRTCSession, 0, len(c.fileRTC))
	for _, peer := range c.fileRTC {
		peers = append(peers, peer)
	}
	c.mu.Unlock()
	for _, peer := range peers {
		peer.close(false)
	}
}

func (c *Client) sendFileBinary(rtc *fileRTCSession, typ byte, id string, offset int64, payload []byte) error {
	if rtc != nil {
		return rtc.send(typ, offset, payload)
	}
	return c.sendBinaryOffset(typ, id, offset, payload)
}

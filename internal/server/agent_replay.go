package server

import (
	"encoding/json"
	"strings"
)

const (
	agentReplayChunkBytes = 8 * 1024
	agentReplayMergeLimit = 256
)

// agentReplayBuffer retains the current turn for reconnects. Adjacent text
// deltas share bounded chunks instead of retaining one JSON object per token.
// Callers hold agentSessionsMu; snapshots own their slice and pending frame.
type agentReplayBuffer struct {
	frames  []json.RawMessage
	pending strings.Builder
	op      string
	thread  string
}

func (b *agentReplayBuffer) Append(msg agentMsg, data []byte) {
	if (msg.Op == "delta" || msg.Op == "reasoning") && len(msg.Text) < agentReplayMergeLimit {
		if b.op != msg.Op || b.thread != msg.Thread || b.pending.Len()+len(msg.Text) > agentReplayChunkBytes {
			b.flush()
		}
		if b.op == "" {
			b.pending.Grow(agentReplayChunkBytes)
		}
		b.op, b.thread = msg.Op, msg.Thread
		b.pending.WriteString(msg.Text)
		return
	}
	b.flush()
	b.frames = append(b.frames, json.RawMessage(data))
}

func (b *agentReplayBuffer) pendingFrame() json.RawMessage {
	data, _ := json.Marshal(struct {
		Op     string `json:"op"`
		Text   string `json:"text,omitempty"`
		Thread string `json:"thread,omitempty"`
	}{b.op, b.pending.String(), b.thread})
	return data
}

func (b *agentReplayBuffer) flush() {
	if b.op == "" {
		return
	}
	b.frames = append(b.frames, b.pendingFrame())
	b.pending.Reset()
	b.op, b.thread = "", ""
}

func (b *agentReplayBuffer) Snapshot() []json.RawMessage {
	frames := make([]json.RawMessage, len(b.frames), len(b.frames)+1)
	copy(frames, b.frames)
	if b.op != "" {
		frames = append(frames, b.pendingFrame())
	}
	return frames
}

func (b *agentReplayBuffer) Reset() { *b = agentReplayBuffer{} }

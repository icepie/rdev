package server

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func appendReplay(t *testing.T, b *agentReplayBuffer, msg agentMsg) {
	t.Helper()
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	b.Append(msg, data)
}

func normalizedReplay(t *testing.T, frames []json.RawMessage) []agentMsg {
	t.Helper()
	var result []agentMsg
	for _, raw := range frames {
		var msg agentMsg
		if err := json.Unmarshal(raw, &msg); err != nil {
			t.Fatal(err)
		}
		n := len(result)
		if n > 0 && (msg.Op == "delta" || msg.Op == "reasoning") &&
			result[n-1].Op == msg.Op && result[n-1].Thread == msg.Thread {
			result[n-1].Text += msg.Text
		} else {
			result = append(result, msg)
		}
	}
	return result
}

func TestAgentReplayPreservesContentAndEventBoundaries(t *testing.T) {
	var replay agentReplayBuffer
	var original []json.RawMessage
	add := func(msg agentMsg) {
		data, _ := json.Marshal(msg)
		original = append(original, data)
		replay.Append(msg, data)
	}
	add(agentMsg{Op: "turn_start", Thread: "one", Text: "inspect"})
	for _, op := range []string{"reasoning", "delta"} {
		for range 1200 {
			add(agentMsg{Op: op, Thread: "one", Text: "中文🙂\"\n"})
		}
	}
	add(agentMsg{Op: "tool_start", Thread: "one", ID: "call", Name: "shell", Args: `{"command":"pwd"}`})
	add(agentMsg{Op: "approval_request", Thread: "one", ID: "approve", Text: "Approve?"})
	add(agentMsg{Op: "tool_end", Thread: "one", ID: "call", Result: "/tmp"})
	add(agentMsg{Op: "steering", Thread: "one", Text: "continue"})
	add(agentMsg{Op: "delta", Thread: "one", Text: strings.Repeat("large", 4000)})
	add(agentMsg{Op: "delta", Thread: "two", Text: "another thread"})
	add(agentMsg{Op: "done", Thread: "one", Reasoning: "final reasoning"})
	actual := replay.Snapshot()
	if !reflect.DeepEqual(normalizedReplay(t, original), normalizedReplay(t, actual)) {
		t.Fatal("replay changed transcript content or event order")
	}
	if len(actual) >= 20 {
		t.Fatalf("small deltas were not coalesced: %d frames", len(actual))
	}
}

func TestAgentReplaySnapshotIsImmutableAndResetDropsPendingText(t *testing.T) {
	var replay agentReplayBuffer
	appendReplay(t, &replay, agentMsg{Op: "delta", Thread: "one", Text: "before"})
	old := replay.Snapshot()
	encoded, _ := json.Marshal(old)
	for range 2000 {
		appendReplay(t, &replay, agentMsg{Op: "delta", Thread: "one", Text: " after"})
	}
	replay.Reset()
	appendReplay(t, &replay, agentMsg{Op: "delta", Thread: "new", Text: "new turn"})
	after, _ := json.Marshal(old)
	if string(after) != string(encoded) {
		t.Fatal("later append/reset mutated a previous snapshot")
	}
	current := normalizedReplay(t, replay.Snapshot())
	if len(current) != 1 || current[0].Text != "new turn" || current[0].Thread != "new" {
		t.Fatalf("reset leaked old turn: %+v", current)
	}
}

func BenchmarkAgentReplay(b *testing.B) {
	for _, size := range []int{32, 1024} {
		for _, coalesce := range []bool{false, true} {
			name := "legacy"
			if coalesce {
				name = "coalesced"
			}
			b.Run(fmt.Sprintf("%s/%dB", name, size), func(b *testing.B) {
				msg := agentMsg{Op: "delta", Thread: "thread", Text: strings.Repeat("x", size)}
				b.ReportAllocs()
				for b.Loop() {
					var replay agentReplayBuffer
					var frames []json.RawMessage
					for range 256 * 1024 / size {
						// Both paths still marshal every frame for live delivery.
						data, _ := json.Marshal(msg)
						if coalesce {
							replay.Append(msg, data)
						} else {
							frames = append(frames, data)
						}
					}
					if coalesce {
						frames = replay.Snapshot()
					} else {
						frames = append([]json.RawMessage(nil), frames...)
					}
					retained := 0
					for _, frame := range frames {
						retained += len(frame)
					}
					b.ReportMetric(float64(retained), "replay-bytes/op")
					b.ReportMetric(float64(len(frames)), "frames/op")
				}
			})
		}
	}
}

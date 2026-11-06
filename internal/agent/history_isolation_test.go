package agent

import (
	"reflect"
	"testing"
)

func TestHistoryOwnsNestedToolCallsAndImages(t *testing.T) {
	for _, write := range []string{"set", "append"} {
		t.Run(write, func(t *testing.T) {
			input := []Message{
				{Role: "user", Content: "inspect image", Images: []MessageImage{{Name: "screen.png", Mime: "image/png", Data: "original"}}},
				{Role: "assistant", ToolCalls: []ToolCall{{ID: "call-1", Name: "read_file", Arguments: `{"path":"original"}`, Summary: "original summary"}}},
			}
			first, second := New(Config{}), New(Config{})
			for _, session := range []*Session{first, second} {
				if write == "set" {
					session.SetHistory(input)
				} else {
					session.appendHistory(input...)
				}
			}
			input[0].Images[0].Data = "changed input"
			input[1].ToolCalls[0].Arguments = "changed input"
			snapshot := first.History()
			if snapshot[0].Images[0].Data != "original" || snapshot[1].ToolCalls[0].Arguments != `{"path":"original"}` {
				t.Fatal("caller-owned slices changed session history")
			}
			snapshot[0].Images[0].Name = "changed snapshot"
			snapshot[1].ToolCalls[0].Summary = "changed snapshot"
			for _, session := range []*Session{first, second} {
				got := session.History()
				if got[0].Images[0].Name != "screen.png" || got[1].ToolCalls[0].Summary != "original summary" {
					t.Fatal("editing one snapshot changed a session")
				}
			}
			if !reflect.DeepEqual(first.History(), second.History()) {
				t.Fatal("sibling session histories diverged")
			}
		})
	}
}

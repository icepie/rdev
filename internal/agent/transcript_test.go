package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestTranscriptIdentityIsNotProviderInput(t *testing.T) {
	message := Message{MessageID: "private-message-id", ContextOnly: true, ArchiveContent: "private original", Role: "user", Content: "visible"}
	for _, images := range [][]MessageImage{nil, {{Name: "image", Mime: "image/png", Data: "AA=="}}} {
		message.Images = images
		data, err := json.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "private") || strings.Contains(string(data), "messageId") || strings.Contains(string(data), "ContextOnly") {
			t.Fatalf("private transcript metadata leaked: %s", data)
		}
	}
}
func TestDurableCaptureFailurePreventsModelAndCompaction(t *testing.T) {
	provider, hits := newFakeProvider(t, sseChunk("should not run", "")+sseDone)
	failure := errors.New("archive quota reached")
	cfg := testConfig(provider.URL, nil)
	cfg.OnHistory = func([]Message) error { return failure }
	session := New(cfg)
	if _, err := session.Run(t.Context(), "request", nil); !errors.Is(err, failure) {
		t.Fatalf("capture error not returned: %v", err)
	}
	session.SetHistory([]Message{{Role: "user", Content: "old"}, {Role: "assistant", Content: "answer"}, {Role: "user", Content: "latest"}})
	before := session.History()
	if _, err := session.Compact(t.Context(), ""); !errors.Is(err, failure) {
		t.Fatalf("compaction ignored capture error: %v", err)
	}
	if hits.Load() != 0 || !reflect.DeepEqual(before, session.History()) {
		t.Fatal("capture failure ran provider or changed context")
	}
}
func TestCaptureBeforeCompactionPreservesOriginalAndStableIdentity(t *testing.T) {
	provider, _ := newFakeProvider(t, sseChunk("compressed", "")+sseDone)
	var captured []Message
	cfg := testConfig(provider.URL, nil)
	cfg.OnHistory = func(messages []Message) error { captured = messages; return nil }
	session := New(cfg)
	session.SetHistory([]Message{{Role: "user", Content: "old original"}, {Role: "assistant", Content: "old answer"}, {Role: "user", Content: "latest"}})
	before := session.History()
	if _, err := session.Compact(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(captured, before) {
		t.Fatal("archive did not receive original pre-compaction history")
	}
	after := session.History()
	if !after[0].ContextOnly || !after[1].ContextOnly || after[2].MessageID != before[2].MessageID {
		t.Fatal("compaction summaries entered archive or retained identity changed")
	}
}
func TestLiveMessageIdentitiesMatchCapturedHistory(t *testing.T) {
	provider, _ := newFakeProvider(t, sseChunk("answer", "")+sseDone)
	var captured []Message
	cfg := testConfig(provider.URL, nil)
	cfg.OnHistory = func(messages []Message) error { captured = messages; return nil }
	session := New(cfg)
	var assistantID string
	_, err := session.Run(WithUserMessageID(t.Context(), "admitted-user"), "request", func(e Event) {
		if e.Type == EventAssistantStart {
			assistantID = e.MessageID
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(captured) != 2 || captured[0].MessageID != "admitted-user" || assistantID == "" || captured[1].MessageID != assistantID {
		t.Fatalf("live/archive identity mismatch: %+v %q", captured, assistantID)
	}
}

type transcriptLargeTool struct {
	fakeTool
	output string
}

func (t *transcriptLargeTool) Execute(context.Context, json.RawMessage) (string, error) {
	return t.output, nil
}

func TestTranscriptCapturesToolOutputBeforeModelTruncation(t *testing.T) {
	raw := strings.Repeat("original-output-", 20000)
	tool := &transcriptLargeTool{fakeTool: fakeTool{name: "large"}, output: raw}
	var archived string
	cfg := Config{Tools: []Tool{tool}, OnHistory: func(messages []Message) error {
		for _, m := range messages {
			if m.Role == "tool" {
				archived = m.ArchiveContent
			}
		}
		return nil
	}}
	session := New(cfg)
	if err := session.RunToolCommand(t.Context(), "run large", "large", json.RawMessage(`{}`), nil); err != nil {
		t.Fatal(err)
	}
	history := session.History()
	if archived != raw || len(history[2].Content) >= len(raw) || history[2].ArchiveContent != "" {
		t.Fatal("original output not archived before bounded model context")
	}
}

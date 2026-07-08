package protocol

import (
	"encoding/json"
	"testing"
)

func TestLogConfigJSONCompatibility(t *testing.T) {
	in := &Message{Type: MsgLogConfig, LogEnabled: true, LogLevel: "debug", SampleRate: 0.5, MaxLineBytes: 4096, FlushIntervalMs: 750}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Message
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Type != MsgLogConfig || !out.LogEnabled || out.LogLevel != "debug" || out.SampleRate != 0.5 || out.MaxLineBytes != 4096 || out.FlushIntervalMs != 750 {
		t.Fatalf("decoded log_config mismatch: %#v", out)
	}
}

func TestLogBatchJSONCompatibility(t *testing.T) {
	in := &Message{Type: MsgLogBatch, Logs: []LogEntry{{Timestamp: "2026-07-08T12:00:00Z", Level: "info", Target: "go", Module: "client", Message: "hello", Fields: map[string]string{"k": "v"}}}}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Message
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Type != MsgLogBatch || len(out.Logs) != 1 || out.Logs[0].Message != "hello" || out.Logs[0].Fields["k"] != "v" {
		t.Fatalf("decoded log_batch mismatch: %#v", out)
	}
}

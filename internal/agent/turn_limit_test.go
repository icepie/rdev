package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestDefaultRunContinuesBeyond25ToolRounds(t *testing.T) {
	var rounds []string
	for i := 0; i < 30; i++ {
		rounds = append(rounds, sseChunk("", "", map[string]any{"index": 0, "id": fmt.Sprint(i), "type": "function", "function": map[string]any{"name": "check", "arguments": "{}"}})+sseDone)
	}
	rounds = append(rounds, sseChunk("finished", "")+sseDone)
	provider, hits := newFakeProvider(t, rounds...)
	cfg := testConfig(provider.URL, []Tool{&fakeTool{name: "check"}})
	cfg.MaxTurns = 0
	session := New(cfg)
	result, err := session.Run(context.Background(), "work", func(Event) {})
	if err != nil || result != "finished" || hits.Load() != 31 {
		t.Fatalf("result=%s rounds=%d err=%v", result, hits.Load(), err)
	}
}
func TestExplicitTurnLimitAndUnlimitedCancellation(t *testing.T) {
	round := sseChunk("", "", map[string]any{"index": 0, "id": "1", "type": "function", "function": map[string]any{"name": "check", "arguments": "{}"}}) + sseDone
	provider, hits := newFakeProvider(t, round)
	cfg := testConfig(provider.URL, []Tool{&fakeTool{name: "check"}})
	cfg.MaxTurns = 1
	if _, err := New(cfg).Run(context.Background(), "work", func(Event) {}); err == nil || !strings.Contains(err.Error(), "MaxTurns") {
		t.Fatalf("explicit limit ignored: %v", err)
	}
	cfg.MaxTurns = 0
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(cfg).Run(ctx, "work", func(Event) {}); err != context.Canceled {
		t.Fatalf("cancellation: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatal("canceled run called provider")
	}
}

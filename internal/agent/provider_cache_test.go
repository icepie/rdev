package agent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestChatCacheUsageDialects(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		cached       int
	}{
		{"openai", `"prompt_tokens_details":{"cached_tokens":80}`, 80},
		{"deepseek", `"prompt_cache_hit_tokens":80,"prompt_cache_miss_tokens":20`, 80},
		{"gateway both", `"prompt_tokens_details":{"cached_tokens":80},"prompt_cache_hit_tokens":80`, 80},
		{"explicit zero", `"prompt_tokens_details":{"cached_tokens":0},"prompt_cache_hit_tokens":80`, 0},
		{"uncached compatible", `"total_tokens":103`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := sseChunk("ok", "") + `data: {"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":3,` + tc.fields + "}}\n\n" + sseDone
			_, usage, err := readStream(strings.NewReader(wire), nil, nil, nil)
			if err != nil || usage.InputTokens != 100 || usage.OutputTokens != 3 || usage.CacheReadTokens != tc.cached {
				t.Fatalf("usage=%+v err=%v", usage, err)
			}
		})
	}
}

func TestAnthropicCacheBoundariesPreserveReplay(t *testing.T) {
	cfg := testConfig("https://provider.invalid", []Tool{&fakeTool{name: "shell"}})
	cfg.API = "anthropic-messages"
	signed := json.RawMessage(`{"type":"thinking","thinking":"plan","signature":"unchanged"}`)
	original := string(signed)
	history := []Message{{Role: "user", Content: "first"}, {Role: "assistant", Anthropic: &ProviderReplay{Scope: providerReplayScope(cfg), Items: []json.RawMessage{signed, json.RawMessage(`{"type":"text","text":"answer"}`)}}}, {Role: "user", Content: "next"}}
	for range 25 {
		history[2].Images = append(history[2].Images, MessageImage{Mime: "image/png", Data: "image"})
	}
	raw, err := marshalAnthropicRequest(cfg, history)
	if err != nil {
		t.Fatal(err)
	}
	var request map[string]any
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(raw), `"cache_control"`); count != 4 {
		t.Fatalf("breakpoints=%d", count)
	}
	messages := request["messages"].([]any)
	first := messages[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	lastBlocks := messages[2].(map[string]any)["content"].([]any)
	last := lastBlocks[len(lastBlocks)-1].(map[string]any)
	if first["cache_control"] == nil || last["cache_control"] == nil {
		t.Fatal("lost old or new cache boundary beyond lookback window")
	}
	replay := messages[1].(map[string]any)["content"].([]any)[0].(map[string]any)
	if string(signed) != original || replay["signature"] != "unchanged" || replay["cache_control"] != nil {
		t.Fatal("modified signed replay")
	}
	history = append(history, Message{Role: "system", Content: "new instructions"})
	next, err := marshalAnthropicRequest(cfg, history)
	if err != nil {
		t.Fatal(err)
	}
	var updated map[string]any
	if err := json.Unmarshal(next, &updated); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(request["system"].([]any)[0], updated["system"].([]any)[0]) {
		t.Fatal("later instructions rewrote cached system block")
	}
}

func TestCachePrefixStableAcrossToolRegistryOrder(t *testing.T) {
	tools := []Tool{&fakeTool{name: "z"}, &fakeTool{name: "a"}}
	cfg := testConfig("https://provider.invalid", tools)
	first := New(cfg)
	cfg.Tools = []Tool{tools[1], tools[0]}
	second := New(cfg)
	if tools[0].Name() != "z" {
		t.Fatal("caller registry reordered")
	}
	for _, marshal := range []func(Config, []Message) ([]byte, error){marshalChatRequest, marshalResponsesRequest, marshalAnthropicRequest} {
		a, err := marshal(first.cfg, []Message{{Role: "user", Content: "first"}})
		if err != nil {
			t.Fatal(err)
		}
		b, err := marshal(second.cfg, []Message{{Role: "user", Content: "first"}})
		if err != nil {
			t.Fatal(err)
		}
		if string(a) != string(b) {
			t.Fatalf("registry order changed provider prefix: %s != %s", a, b)
		}
	}
}

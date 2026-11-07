package server

import (
	"reflect"
	"testing"

	"rdev/internal/agent"
)

func TestAgentProviderAPISelection(t *testing.T) {
	provider := AgentProvider{ID: "test", BaseURL: "http://localhost", API: "openai-responses", Models: []AgentModelConfig{{ID: "model"}}}
	cfg := AgentConfig{Providers: []AgentProvider{provider}}
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	if provider.modelAPI(&provider.Models[0]) != "openai-responses" {
		t.Fatal("provider API was not inherited")
	}
	model := provider.Models[0]
	model.API = "openai-completions"
	if provider.modelAPI(&model) != "openai-completions" {
		t.Fatal("model API did not override provider")
	}
	cfg.Providers[0].API = "unsupported"
	if cfg.validate() == nil {
		t.Fatal("unsupported provider API silently accepted")
	}
	cfg.Providers[0].API = ""
	cfg.Providers[0].Models[0].API = "unsupported"
	if cfg.validate() == nil {
		t.Fatal("unsupported model API silently accepted")
	}
}

func TestAnthropicConfigValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		model   AgentModelConfig
		invalid bool
	}{
		{name: "plain", model: AgentModelConfig{ID: "fixture"}},
		{name: "explicit adaptive", model: AgentModelConfig{ID: "fixture", Reasoning: true, Thinking: agent.AnthropicThinkingConfig{Mode: "anthropic-adaptive"}}},
		{name: "missing capability", model: AgentModelConfig{ID: "fixture", Reasoning: true}, invalid: true},
		{name: "invalid budget", model: AgentModelConfig{ID: "fixture", MaxTokens: 4096, Thinking: agent.AnthropicThinkingConfig{Mode: "anthropic-budget", BudgetTokens: 4096}}, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := AgentConfig{Providers: []AgentProvider{{ID: "test", API: "anthropic-messages", BaseURL: "https://fixture.invalid/v1", Models: []AgentModelConfig{tc.model}}}}
			err := cfg.validate()
			if (err != nil) != tc.invalid {
				t.Fatalf("err=%v invalid=%v", err, tc.invalid)
			}
		})
	}
}

func TestAdaptiveModelCatalogOmitsOffEffort(t *testing.T) {
	cfg := AgentConfig{Providers: []AgentProvider{{ID: "test", API: "anthropic-messages", Models: []AgentModelConfig{
		{ID: "adaptive", Thinking: agent.AnthropicThinkingConfig{Mode: "anthropic-adaptive"}},
		{ID: "budget", Thinking: agent.AnthropicThinkingConfig{Mode: "anthropic-budget"}},
		{ID: "chat", API: "openai-completions"},
	}}}}
	models := cfg.Models()
	if !reflect.DeepEqual(models[0]["reasoningLevels"], []string{"low", "medium", "high"}) || models[1]["reasoningLevels"] != nil || models[2]["reasoningLevels"] != nil {
		t.Fatalf("invalid effort catalog: %+v", models)
	}
}

package server

import "testing"

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

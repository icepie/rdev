package server

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// AgentModelConfig is one model offered by a provider.
type AgentModelConfig struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Reasoning bool   `json:"reasoning,omitempty"`
	// ContextWindow is the model's max context tokens, used by the
	// composer context-usage indicator. 0 = unknown (indicator hidden).
	ContextWindow int `json:"contextWindow,omitempty"`
}

// AgentProvider is one OpenAI-compatible LLM provider.
type AgentProvider struct {
	ID      string             `json:"id"`
	Name    string             `json:"name"`
	BaseURL string             `json:"base_url"`
	APIKey  string             `json:"api_key"`
	Models  []AgentModelConfig `json:"models"`
}

// AgentConfig is the multi-provider LLM agent configuration, a subset of the
// omp/nicecode model-config.json format. Unknown JSON fields are ignored so
// config files written for the full format keep working here.
type AgentConfig struct {
	DefaultModel string          `json:"default_model"`
	Providers    []AgentProvider `json:"providers"`
}

// LoadAgentConfig reads and validates a model-config.json file. ${VAR} and
// $VAR in provider api_key values are expanded from the environment.
func LoadAgentConfig(path string) (*AgentConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg AgentConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for i := range cfg.Providers {
		cfg.Providers[i].APIKey = os.ExpandEnv(cfg.Providers[i].APIKey)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cfg, nil
}

func (c *AgentConfig) validate() error {
	if len(c.Providers) == 0 {
		return fmt.Errorf("no providers configured")
	}
	providerIDs := make(map[string]bool, len(c.Providers))
	modelIDs := make(map[string]bool)
	for _, p := range c.Providers {
		if p.ID == "" {
			return fmt.Errorf("provider with empty id")
		}
		if strings.Contains(p.ID, "/") {
			return fmt.Errorf("provider %q: id must not contain '/'", p.ID)
		}
		if providerIDs[p.ID] {
			return fmt.Errorf("duplicate provider id %q", p.ID)
		}
		providerIDs[p.ID] = true
		if p.BaseURL == "" {
			return fmt.Errorf("provider %q: empty base_url", p.ID)
		}
		if len(p.Models) == 0 {
			return fmt.Errorf("provider %q: no models", p.ID)
		}
		for _, m := range p.Models {
			if m.ID == "" {
				return fmt.Errorf("provider %q: model with empty id", p.ID)
			}
			canonical := p.ID + "/" + m.ID
			if modelIDs[canonical] {
				return fmt.Errorf("duplicate model id %q", canonical)
			}
			modelIDs[canonical] = true
		}
	}
	return nil
}

// Resolve maps a model reference to its provider and model. A canonical
// "provider/model" reference splits on the first "/"; a bare model id is
// searched across providers in order (first match wins). An empty ref uses
// default_model, falling back to the first provider's first model.
func (c *AgentConfig) Resolve(ref string) (provider *AgentProvider, model *AgentModelConfig, canonical string, err error) {
	if ref == "" {
		ref = c.DefaultModel
	}
	if ref != "" {
		if provider, model, canonical, ok := c.lookup(ref); ok {
			return provider, model, canonical, nil
		}
		return nil, nil, "", fmt.Errorf("unknown model %q", ref)
	}
	if len(c.Providers) == 0 || len(c.Providers[0].Models) == 0 {
		return nil, nil, "", fmt.Errorf("no models configured")
	}
	p := &c.Providers[0]
	m := &p.Models[0]
	return p, m, p.ID + "/" + m.ID, nil
}

func (c *AgentConfig) lookup(ref string) (*AgentProvider, *AgentModelConfig, string, bool) {
	if i := strings.Index(ref, "/"); i >= 0 {
		providerID, modelID := ref[:i], ref[i+1:]
		for pi := range c.Providers {
			p := &c.Providers[pi]
			if p.ID != providerID {
				continue
			}
			for mi := range p.Models {
				if p.Models[mi].ID == modelID {
					return p, &p.Models[mi], ref, true
				}
			}
			return nil, nil, "", false
		}
		return nil, nil, "", false
	}
	for pi := range c.Providers {
		p := &c.Providers[pi]
		for mi := range p.Models {
			if p.Models[mi].ID == ref {
				return p, &p.Models[mi], p.ID + "/" + ref, true
			}
		}
	}
	return nil, nil, "", false
}

// Models returns the catalog advertised to browsers in auth_ok: one entry per
// configured model with its canonical id, display name, provider id, and
// reasoning flag (omitted when false).
func (c *AgentConfig) Models() []map[string]any {
	var out []map[string]any
	for _, p := range c.Providers {
		for _, m := range p.Models {
			name := m.Name
			if name == "" {
				name = m.ID
			}
			entry := map[string]any{
				"id":       p.ID + "/" + m.ID,
				"name":     name,
				"provider": p.ID,
			}
			if m.Reasoning {
				entry["reasoning"] = true
			}
			if m.ContextWindow > 0 {
				entry["contextWindow"] = m.ContextWindow
			}
			out = append(out, entry)
		}
	}
	return out
}

// agentDefaultModel returns the canonical id of the default model, or "".
func agentDefaultModel(c *AgentConfig) string {
	if c == nil {
		return ""
	}
	_, _, canonical, err := c.Resolve("")
	if err != nil {
		return ""
	}
	return canonical
}

// agentProviderIDs returns the comma-joined provider ids, or "".
func agentProviderIDs(c *AgentConfig) string {
	if c == nil {
		return ""
	}
	ids := make([]string, 0, len(c.Providers))
	for _, p := range c.Providers {
		ids = append(ids, p.ID)
	}
	return strings.Join(ids, ",")
}

// Enabled reports whether at least one provider has an API key.
func (c *AgentConfig) Enabled() bool {
	for _, p := range c.Providers {
		if p.APIKey != "" {
			return true
		}
	}
	return false
}

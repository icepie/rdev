package server

// RPC model identifiers follow OMP: id is local to provider. CanonicalID is a
// rdev extension for clients that also use browser operations. Only configured
// public metadata is exposed; unknown capabilities and prices are not invented.
type rpcModel struct {
	ID            string `json:"id"`
	Provider      string `json:"provider"`
	CanonicalID   string `json:"canonicalId"`
	Name          string `json:"name"`
	API           string `json:"api"`
	Reasoning     bool   `json:"reasoning"`
	ContextWindow int    `json:"contextWindow,omitempty"`
	MaxTokens     int    `json:"maxTokens,omitempty"`
}

func describeRPCModel(provider *AgentProvider, model *AgentModelConfig) rpcModel {
	name := model.Name
	if name == "" {
		name = model.ID
	}
	api := provider.modelAPI(model)
	if api == "" {
		api = "openai-completions"
	}
	return rpcModel{
		ID: model.ID, Provider: provider.ID, CanonicalID: provider.ID + "/" + model.ID,
		Name: name, API: api, Reasoning: model.Reasoning,
		ContextWindow: model.ContextWindow, MaxTokens: model.MaxTokens,
	}
}

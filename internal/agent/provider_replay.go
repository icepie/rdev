package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
)

// ProviderReplay holds opaque continuation state. Separate protocol fields on
// Message prevent cross-protocol replay; the digest further binds it to a route.
// It is persisted locally, never projected into UI or another provider's text.
type ProviderReplay struct {
	Scope string            `json:"scope"`
	Items []json.RawMessage `json:"items"`
}

// ResponseReplay preserves the existing exported/persisted Responses contract.
type ResponseReplay = ProviderReplay

func providerReplayScope(cfg Config) string {
	scope, _ := json.Marshal([]string{cfg.ProviderID, strings.TrimRight(cfg.BaseURL, "/"), cfg.Model})
	digest := sha256.Sum256(scope)
	return hex.EncodeToString(digest[:])
}

func cloneProviderReplay(src *ProviderReplay) *ProviderReplay {
	if src == nil {
		return nil
	}
	dst := &ProviderReplay{Scope: src.Scope, Items: make([]json.RawMessage, len(src.Items))}
	for i, item := range src.Items {
		dst.Items[i] = slices.Clone(item)
	}
	return dst
}

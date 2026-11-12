package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// cuaToolDiscovery validates an MCP tools result and exposes its complete
// name set without retaining the potentially very large schemas in the model
// response. The backend inventory remains the source of truth for schemas.
type cuaToolDiscovery struct {
	Names map[string]bool
}

// cuaToolResult returns either a compact catalog (when tool is empty) or the
// exact selected backend definition. The compact form deliberately omits
// inputSchema and all enforcement/output metadata; callers request a selected
// definition with action=tools,tool=<exact name> when they need its schema.
func cuaToolResult(raw json.RawMessage, tool string) (json.RawMessage, cuaToolDiscovery, error) {
	var result map[string]json.RawMessage
	if err := json.Unmarshal(raw, &result); err != nil || result == nil {
		return nil, cuaToolDiscovery{}, fmt.Errorf("invalid Cua tools result")
	}
	var definitions []json.RawMessage
	if err := json.Unmarshal(result["tools"], &definitions); err != nil || definitions == nil {
		return nil, cuaToolDiscovery{}, fmt.Errorf("invalid Cua tools result: missing tools")
	}
	discovery := cuaToolDiscovery{Names: make(map[string]bool, len(definitions))}
	var selected json.RawMessage
	for _, definition := range definitions {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(definition, &fields); err != nil || fields == nil {
			return nil, cuaToolDiscovery{}, fmt.Errorf("invalid Cua tool definition")
		}
		var name string
		if err := json.Unmarshal(fields["name"], &name); err != nil || strings.TrimSpace(name) == "" || len(name) > 128 {
			return nil, cuaToolDiscovery{}, fmt.Errorf("invalid Cua tool name")
		}
		discovery.Names[name] = true
		if tool != "" && name == tool {
			selected = bytes.Clone(definition)
		}
	}
	if tool != "" {
		if selected == nil {
			return nil, discovery, fmt.Errorf("native Cua tool %q is unavailable on this device; discover tools with action=tools", tool)
		}
		// Preserve every field in the selected backend definition, including the
		// complete inputSchema and any future schema extensions.
		return json.RawMessage(`{"tools":[` + string(selected) + `]}`), discovery, nil
	}
	catalog := make([]map[string]json.RawMessage, 0, len(definitions))
	for _, definition := range definitions {
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(definition, &fields)
		entry := map[string]json.RawMessage{"name": fields["name"]}
		if description := fields["description"]; len(description) > 0 {
			var text string
			if json.Unmarshal(description, &text) == nil && text != "" {
				entry["description"] = json.RawMessage(marshalCuaDescription(text))
			}
		}
		catalog = append(catalog, entry)
	}
	compact, err := json.Marshal(map[string]any{
		"tools": catalog,
		"next":  "For a full input schema, request action=tools with tool=<exact name>.",
	})
	if err != nil {
		return nil, cuaToolDiscovery{}, err
	}
	return compact, discovery, nil
}

// Keep catalog descriptions useful without allowing backend prose to dominate
// the model context. Truncate by rune so the JSON string is always valid UTF-8.
func marshalCuaDescription(text string) json.RawMessage {
	const maxRunes = 320
	runes := []rune(text)
	if len(runes) > maxRunes {
		runes = runes[:maxRunes]
		text = string(runes) + "… [truncated]"
	}
	b, _ := json.Marshal(text)
	return b
}

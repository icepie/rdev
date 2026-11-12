package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCuaToolResultCompactCatalogAndExactSchema(t *testing.T) {
	raw := json.RawMessage(`{"tools":[{"name":"short","description":"brief","inputSchema":{"type":"object","required":["x"]},"enforcement":{"secret":"omit"}},{"name":"long","description":"` + strings.Repeat("x", 500) + `","inputSchema":{"type":"object","properties":{"value":{"type":"string"}}}}]}`)
	catalog, discovery, err := cuaToolResult(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	if !discovery.Names["short"] || !discovery.Names["long"] {
		t.Fatal("catalog omitted discoverable tool")
	}
	if strings.Contains(string(catalog), strings.Repeat("x", 321)) || strings.Contains(string(catalog), "inputSchema") || strings.Contains(string(catalog), "enforcement") {
		t.Fatalf("catalog was not compact: %d bytes", len(catalog))
	}
	var selected map[string]json.RawMessage
	selectedRaw, _, err := cuaToolResult(raw, "short")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(selectedRaw, &selected); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(selectedRaw), `"required":["x"]`) || !strings.Contains(string(selectedRaw), `"enforcement"`) {
		t.Fatalf("selected schema was not preserved: %s", selectedRaw)
	}
}

func TestCuaToolResultRejectsUnknownTool(t *testing.T) {
	raw := json.RawMessage(`{"tools":[{"name":"known","inputSchema":{"type":"object"}}]}`)
	if _, discovery, err := cuaToolResult(raw, "missing"); err == nil || discovery.Names["missing"] {
		t.Fatalf("unknown tool accepted: %v", err)
	}
}

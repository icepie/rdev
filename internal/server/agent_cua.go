package server

import (
	"context"
	"encoding/json"
	"fmt"

	"rdev/internal/agent"
)

type cuaTool struct{ automationTool }

func (*cuaTool) Name() string { return "cua" }
func (*cuaTool) Description() string {
	return "Use the selected client's embedded CUA Driver for native desktop actions. When computer status reports cua=true, read skill cua-driver/SKILL.md and WORKFLOW.md. action=tools returns a compact catalog; action=tools with tool=<exact name> returns that tool's complete input schema. Fetch the needed schema before action=call; never guess parameters or repeatedly load the full inventory. RDev creates and owns the session: no create_session, session ID, standalone CLI or cloud login is needed. For virtual cursors follow the skill's enable/place/verify workflow; the overlay is distinct from the system pointer and ends with session cleanup. Screenshots are optional; use them when visual context or pixel coordinates are needed, and verify meaningful actions with fresh observations. Native coordinates follow each discovered schema, not necessarily computer screenshot pixels. Success, enabled=true and unverifiable are not proof of visible effect. Fall back to computer input only for unsupported capability or an absent action, never permission denial, takeover or an uncertain result."
}
func (*cuaTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"action":{"type":"string","enum":["tools","call"]},"tool":{"type":"string","maxLength":128,"description":"Exact catalog name: optional for tools to fetch one complete input schema; required for call"},"arguments":{"type":"object","description":"Call arguments matching the selected tool's input schema"},"source":{"type":"string"},"device":{"type":"string"}},"required":["action"],"additionalProperties":false}`)
}
func (t *cuaTool) Summary(raw json.RawMessage) string { return automationSummary(raw) }
func (t *cuaTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Action    string          `json:"action"`
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
		Source    string          `json:"source"`
		Device    string          `json:"device"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid CUA arguments: %w", err)
	}
	var internalAction string
	switch args.Action {
	case "tools":
		internalAction = "native_tools"
	case "call":
		if args.Tool == "" || len(args.Tool) > 128 {
			return "", fmt.Errorf("cua call requires a tool name from action=tools")
		}
		internalAction = "native"
	default:
		return "", fmt.Errorf("unsupported CUA action %q", args.Action)
	}
	if len(args.Arguments) == 0 {
		args.Arguments = json.RawMessage(`{}`)
	}
	var encoded []byte
	var err error
	if args.Action == "tools" {
		encoded, err = json.Marshal(map[string]any{"action": internalAction, "device": args.Device, "source": args.Source, "tool": args.Tool})
	} else {
		encoded, err = json.Marshal(map[string]any{"action": internalAction, "device": args.Device, "source": args.Source, "tool": args.Tool, "arguments": args.Arguments})
	}
	if err != nil {
		return "", err
	}
	computer := &computerTool{automationTool: t.automationTool}
	return computer.Execute(ctx, encoded)
}

var _ agent.Tool = (*cuaTool)(nil)

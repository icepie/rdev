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
	return "Use the selected client's CUA Driver accessibility tools. Read the cua-driver skill via skill (action=read, skill=cua-driver, file=SKILL.md) before GUI work; load its referenced guides as needed. Call action=tools to discover every tool and its input schema, then call action=call with the exact returned tool name and arguments. Includes all tools provided by the connected driver's tools/list, not only a built-in subset."
}
func (*cuaTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"action":{"type":"string","enum":["tools","call"]},"tool":{"type":"string","maxLength":128,"description":"Exact tool name returned by action=tools"},"arguments":{"type":"object","description":"Arguments matching that tool's returned input schema"},"source":{"type":"string"},"device":{"type":"string"}},"required":["action"],"additionalProperties":false}`)
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
		encoded, err = json.Marshal(map[string]any{"action": internalAction, "device": args.Device, "source": args.Source})
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

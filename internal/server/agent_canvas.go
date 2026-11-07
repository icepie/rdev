package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type canvasTool struct{}

func (*canvasTool) Name() string    { return "canvas" }
func (*canvasTool) Dangerous() bool { return false }
func (*canvasTool) Description() string {
	return "Present a self-contained visual report or interactive HTML demo in the conversation. Use markdown for reports, html for interactive diagrams/forms/calculators. HTML runs in an isolated browser sandbox with inline CSS/JS only, no network, device, filesystem or application access. Each call creates a retained revision; include the complete content. Use ask separately when you need a user decision returned to you."
}
func (*canvasTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"title":{"type":"string","maxLength":160},"format":{"type":"string","enum":["markdown","html"]},"content":{"type":"string","maxLength":65536}},"required":["title","format","content"],"additionalProperties":false}`)
}
func (*canvasTool) Summary(raw json.RawMessage) string {
	var args struct{ Title string }
	_ = json.Unmarshal(raw, &args)
	return truncateRunes(args.Title, 80)
}
func (*canvasTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var args struct{ Title, Format, Content string }
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid canvas: %w", err)
	}
	if strings.TrimSpace(args.Title) == "" || len([]rune(args.Title)) > 160 {
		return "", fmt.Errorf("canvas title must contain 1-160 characters")
	}
	if args.Format != "markdown" && args.Format != "html" {
		return "", fmt.Errorf("canvas format must be markdown or html")
	}
	if strings.TrimSpace(args.Content) == "" || len(args.Content) > 64*1024 {
		return "", fmt.Errorf("canvas content must contain 1-65536 bytes")
	}
	// Source is already retained in the tool-call arguments. Do not repeat it in
	// model context or claim the browser has opened/evaluated the preview.
	result, _ := json.Marshal(map[string]string{"type": "canvas", "title": args.Title, "format": args.Format, "status": "ready"})
	return string(result), nil
}

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"rdev/internal/agent"
)

// subagentTool launches an isolated conversation with a fresh set of device
// tool instances. The launch approval delegates only the selected capabilities;
// children cannot spawn further jobs or inherit the parent's full transcript.
type subagentTool struct {
	jobs   *agentJobs
	config agent.Config
	tools  func() []agent.Tool
}

func (*subagentTool) Name() string    { return "subagent" }
func (*subagentTool) Dangerous() bool { return true }
func (*subagentTool) Description() string {
	return "Delegate a self-contained task to a background subagent using the current model and device. Launch approval grants the explicitly selected tools for that task. Defaults to read-only tools. Children have isolated context, cannot spawn other jobs, and automatically report results to the parent. Inspect, cancel, or send additional guidance with jobs. Include all needed context in task."
}
func (*subagentTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"task":{"type":"string","description":"Self-contained task, context, constraints, and expected result"},"tools":{"type":"array","items":{"type":"string"},"description":"Subset of device tool names to delegate; omitted defaults to list_dir, read_file, grep, glob, web_search, web_fetch"}},"required":["task"],"additionalProperties":false}`)
}
func (*subagentTool) Summary(raw json.RawMessage) string {
	var args struct {
		Task  string   `json:"task"`
		Tools []string `json:"tools"`
	}
	if json.Unmarshal(raw, &args) != nil {
		return ""
	}
	label := truncateRunes(args.Task, 80)
	if args.Tools != nil {
		label += " [" + strings.Join(args.Tools, ", ") + "]"
	}
	return label
}
func (t *subagentTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Task  string   `json:"task"`
		Tools []string `json:"tools"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.Task) == "" {
		return "", fmt.Errorf("task is required")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	available := t.tools()
	if args.Tools == nil {
		for _, tool := range available {
			switch tool.Name() {
			case "list_dir", "read_file", "grep", "glob", "web_search", "web_fetch", "list_devices":
				args.Tools = append(args.Tools, tool.Name())
			}
		}
	}
	selected := make([]agent.Tool, 0, len(args.Tools))
	seen := make(map[string]bool)
	for _, name := range args.Tools {
		if seen[name] {
			continue
		}
		seen[name] = true
		var found agent.Tool
		for _, tool := range available {
			if tool.Name() == name {
				found = tool
				break
			}
		}
		if found == nil {
			return "", fmt.Errorf("tool %q is not available for delegation", name)
		}
		selected = append(selected, found)
	}
	cfg := t.config
	cfg.Tools = selected
	// The parent approves the complete capability grant at launch. Reusing its
	// foreground approval callback would tie a child to an already-ended turn.
	cfg.Approve = nil
	cfg.ApproveAll = nil
	cfg.SystemPrompt += "\nYou are a delegated subagent. Complete only the assigned task using the granted tools. Report results, evidence, and unresolved issues. Do not claim unverified outcomes."
	guidance := &agentJobGuidance{}
	job, err := t.jobs.start("subagent", truncateRunes(args.Task, 120), func(ctx context.Context, write func(string)) (string, error) {
		childCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		defer cancel()
		child := agent.New(cfg)
		emit := func(event agent.Event) {
			switch event.Type {
			case agent.EventTextDelta:
				write(event.Text)
			case agent.EventUserInput:
				write("\n[guidance] " + event.Text + "\n")
			case agent.EventToolStart:
				write("\n[tool: " + event.ToolName + "] " + event.Summary + "\n")
			case agent.EventToolEnd:
				if event.IsError {
					write("\n[tool error] " + event.ToolResult + "\n")
				}
			}
		}
		defer guidance.close()
		text := args.Task
		for {
			result, err := child.RunTurnWithSteering(childCtx, text, nil, "", emit, guidance.take)
			if err != nil {
				return result, err
			}
			// Include messages admitted after the final in-turn boundary. If
			// none remain, close admission before publishing completion.
			next := guidance.next(true)
			if next == nil {
				return result, nil
			}
			text = next.Content
			write("\n[guidance] " + text + "\n")
		}
	}, guidance)
	if err != nil {
		return "", err
	}
	return marshalJobResult(job)
}

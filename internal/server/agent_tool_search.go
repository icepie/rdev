package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// --- grep ---

// grepTool searches file contents on the device through the shell channel
// (grep on unix, findstr on windows).
type grepTool struct {
	shell *shellTool
}

func (t *grepTool) Name() string { return "grep" }

func (t *grepTool) Description() string {
	return "Search file contents on the remote device for an extended regex pattern, recursively. Returns matching lines with file paths and line numbers."
}

func (t *grepTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern": {"type": "string", "description": "Extended regular expression to search for"},
			"path":    {"type": "string", "description": "File or directory to search (default: current directory)"},
			"include": {"type": "string", "description": "Optional file name glob to limit matches (e.g. \"*.go\"); unix only"}
		},
		"required": ["pattern"]
	}`)
}

// Dangerous marks grep as read-only (no approval needed).
func (t *grepTool) Dangerous() bool { return false }

func (t *grepTool) Summary(raw json.RawMessage) string {
	return toolPatternArg(raw)
}

func (t *grepTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
		Include string `json:"include"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %v", err)
	}
	if args.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	if args.Path == "" {
		args.Path = "."
	}

	var cmd string
	if clientOS(t.shell.client) == "windows" {
		cmd = windowsSearchCommand(args.Pattern, args.Path, true)
	} else {
		var b strings.Builder
		b.WriteString("grep -rnE")
		if args.Include != "" {
			b.WriteString(" --include=" + shellQuote(args.Include))
		}
		b.WriteString(" -- ")
		b.WriteString(shellQuote(args.Pattern))
		b.WriteString(" ")
		b.WriteString(shellQuote(args.Path))
		cmd = b.String()
	}
	return t.shell.runShell(ctx, cmd, 120)
}

// --- glob ---

// globTool finds files by name pattern on the device through the shell
// channel (find on unix, dir on windows).
type globTool struct {
	shell *shellTool
}

func (t *globTool) Name() string { return "glob" }

func (t *globTool) Description() string {
	return "Find files on the remote device by name pattern, recursively from a starting directory."
}

func (t *globTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern": {"type": "string", "description": "File name pattern (e.g. \"*.go\")"},
			"path":    {"type": "string", "description": "Directory to search from (default: current directory)"}
		},
		"required": ["pattern"]
	}`)
}

// Dangerous marks glob as read-only (no approval needed).
func (t *globTool) Dangerous() bool { return false }

func (t *globTool) Summary(raw json.RawMessage) string {
	return toolPatternArg(raw)
}

func (t *globTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %v", err)
	}
	if args.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	if args.Path == "" {
		args.Path = "."
	}

	var cmd string
	if clientOS(t.shell.client) == "windows" {
		cmd = windowsSearchCommand(args.Pattern, args.Path, false)
	} else {
		cmd = fmt.Sprintf("find %s -name %s", shellQuote(args.Path), shellQuote(args.Pattern))
	}
	return t.shell.runShell(ctx, cmd, 120)
}

// --- todo_write ---

// todoItem is one checklist entry maintained by the todo_write tool.
type todoItem struct {
	Blocker string   `json:"blocker,omitempty"`
	Details string   `json:"details,omitempty"`
	Notes   []string `json:"notes,omitempty"`
	Content string   `json:"content"`
	Status  string   `json:"status"` // "pending" | "in_progress" | "completed"
}

// todoWriteTool records the model's plan/progress checklist. It never
// touches the device; the server mirrors the list to attached browsers.
type todoWriteTool struct{}

func (t *todoWriteTool) Name() string { return "todo_write" }

func (t *todoWriteTool) Description() string {
	return "Record and update a checklist of tasks for multi-step work. Replaces the whole list each call; every item needs a content and a status (pending, in_progress or completed)."
}

func (t *todoWriteTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"items": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"content": {"type": "string", "description": "Task description"},
						"status":   {"type": "string", "enum": ["pending", "in_progress", "completed"]}
					},
					"required": ["content", "status"]
				}
			}
		},
		"required": ["items"]
	}`)
}

// Dangerous marks todo_write as a no-op on the device (no approval needed).
func (t *todoWriteTool) Dangerous() bool { return false }

func (t *todoWriteTool) Summary(raw json.RawMessage) string {
	var args struct {
		Items []todoItem `json:"items"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return ""
	}
	return fmt.Sprintf("%d items", len(args.Items))
}

func (t *todoWriteTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Items []todoItem `json:"items"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %v", err)
	}
	for i, item := range args.Items {
		if strings.TrimSpace(item.Content) == "" {
			return "", fmt.Errorf("item %d: content is required", i)
		}
		switch item.Status {
		case "pending", "in_progress", "completed":
		default:
			return "", fmt.Errorf("item %d: invalid status %q (pending, in_progress or completed)", i, item.Status)
		}
	}
	return fmt.Sprintf("todo list updated: %d items", len(args.Items)), nil
}

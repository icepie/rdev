package server

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"rdev/internal/agent"
)
//go:embed cua-skill
var agentSkillFiles embed.FS

// skillPackages maps public skill IDs to their private embedded resource roots.
var skillPackages = map[string]string{
	"cua-driver": "cua-skill",
}

type skillTool struct{}

func (*skillTool) Name() string { return "skill" }
func (*skillTool) Description() string {
	return "Discover and read bundled agent skills. Use action=list to discover skill packages, then read each package's SKILL.md entrypoint before applying it."
}
func (*skillTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"action":{"type":"string","enum":["list","read"]},"skill":{"type":"string","description":"Skill package ID returned by action=list"},"file":{"type":"string","description":"Markdown resource path within the skill package; use SKILL.md as the entrypoint"}},"required":["action"],"additionalProperties":false}`)
}
func (*skillTool) Summary(raw json.RawMessage) string { return automationSummary(raw) }
func (*skillTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Action string `json:"action"`
		Skill  string `json:"skill"`
		File   string `json:"file"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid skill request: %w", err)
	}
	switch args.Action {
	case "list":
		if args.Skill == "" {
            ids := make([]string, 0, len(skillPackages))
			for id := range skillPackages {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			encoded, err := json.Marshal(ids)
			return string(encoded), err
		}
		root, ok := skillPackages[args.Skill]
		if !ok {
			return "", fmt.Errorf("unknown skill %q; use skill action=list", args.Skill)
		}
		entries, err := fs.Glob(agentSkillFiles, path.Join(root, "*.md"))
		if err != nil {
			return "", err
		}
		files := make([]string, 0, len(entries))
		for _, entry := range entries {
			files = append(files, strings.TrimPrefix(entry, root+"/"))
		}
		sort.Strings(files)
		encoded, err := json.Marshal(files)
		return string(encoded), err
	case "read":
		root, ok := skillPackages[args.Skill]
		if !ok {
			return "", fmt.Errorf("unknown skill %q; use skill action=list", args.Skill)
		}
		if !fs.ValidPath(args.File) || path.Ext(args.File) != ".md" {
			return "", fmt.Errorf("skill resources must be Markdown files within the selected package")
		}
		content, err := agentSkillFiles.ReadFile(path.Join(root, args.File))
		if err != nil {
			return "", fmt.Errorf("read %s skill resource %q: %w", args.Skill, args.File, err)
		}
		return string(content), nil
	default:
		return "", fmt.Errorf("unsupported skill action %q", args.Action)
	}
}

var _ agent.Tool = (*skillTool)(nil)

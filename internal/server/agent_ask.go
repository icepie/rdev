package server

// The ask tool (omp ask parity): the model pauses mid-turn to ask the user a
// structured question — options to pick from, or free text — instead of
// guessing. The question is broadcast like an approval card; the browser's
// answer becomes the tool result.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// askOption is one selectable answer in an ask tool question.
type askOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// askFunc blocks until the user answers (or the question is dismissed,
// returning "").
type askFunc func(ctx context.Context, question string, options []askOption) string

type askTool struct {
	ask askFunc
}

func (t *askTool) Name() string { return "ask" }

func (t *askTool) Description() string {
	return "Ask the user a question mid-turn and wait for the answer. Use when the request is ambiguous, a decision is needed, or multiple interpretations exist — do not guess. Provide 2-5 concrete options when choices exist; omit options for free-text answers. A dismissal returns an empty answer — proceed with your best judgment and say what you assumed."
}

func (t *askTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"question": {"type": "string", "description": "The question to ask the user"},
			"options": {"type": "array", "description": "Selectable answers (2-5); omit for free-text", "items": {"type": "object", "properties": {"label": {"type": "string", "description": "Short answer label"}, "description": {"type": "string", "description": "What choosing this means"}}, "required": ["label"]}}
		},
		"required": ["question"]
	}`)
}

// Dangerous is false: asking needs no approval — it IS the interaction.
func (t *askTool) Dangerous() bool { return false }

func (t *askTool) Summary(raw json.RawMessage) string {
	var args struct {
		Question string `json:"question"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return ""
	}
	return truncateRunes(args.Question, 80)
}

func (t *askTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Question string      `json:"question"`
		Options  []askOption `json:"options"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %v", err)
	}
	if strings.TrimSpace(args.Question) == "" {
		return "", fmt.Errorf("question is required")
	}
	if len(args.Options) == 1 {
		return "", fmt.Errorf("options must have 2-5 entries or be omitted")
	}
	if len(args.Options) > 5 {
		return "", fmt.Errorf("options capped at 5, got %d", len(args.Options))
	}
	for i, o := range args.Options {
		if strings.TrimSpace(o.Label) == "" {
			return "", fmt.Errorf("option %d has an empty label", i+1)
		}
	}

	answer := t.ask(ctx, args.Question, args.Options)
	if answer == "" {
		return "[question dismissed — no answer; proceed with your best judgment]", nil
	}
	return answer, nil
}

// marshalAskOptions renders the option list for the ask_request frame (nil
// when the question is free-text only).
func marshalAskOptions(options []askOption) json.RawMessage {
	if len(options) == 0 {
		return nil
	}
	raw, err := json.Marshal(options)
	if err != nil {
		return nil
	}
	return raw
}

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"rdev/internal/agent"
)

// runAgentConversation keeps command dispatch separate from turn lifecycle
// management. historyReplaced tells persistence to replace, rather than append,
// even when a summary happens to contain the same number of messages.
func runAgentConversation(ctx context.Context, sess *agent.Session, text string, images []agent.MessageImage, emit func(agent.Event)) (historyReplaced bool, err error) {
	command, argument := splitAgentCommand(text)
	if command == "/bg" || command == "/subagent" {
		name, args, err := agentCommandTool(command, argument)
		if err != nil {
			return false, err
		}
		return false, sess.RunToolCommand(ctx, text, name, args, emit)
	}
	if command != "/compact" {
		_, err = sess.RunTurn(ctx, text, images, magicTurnEffort(text), emit)
		return false, err
	}
	usage, err := sess.Compact(ctx, argument)
	if err != nil {
		return false, err
	}
	const message = "Context compacted. The latest conversation and its tool results were preserved."
	emit(agent.Event{Type: agent.EventTextDelta, Text: message})
	emit(agent.Event{Type: agent.EventDone, Text: message, Usage: usage})
	return true, nil
}

func splitAgentCommand(text string) (string, string) {
	text = strings.TrimSpace(text)
	if i := strings.IndexFunc(text, unicode.IsSpace); i >= 0 {
		return strings.ToLower(text[:i]), strings.TrimSpace(text[i:])
	}
	return strings.ToLower(text), ""
}

// /bg --device <id> <command> makes the target explicit in server mode.
// Everything after the optional target is preserved as shell text.
func agentCommandTool(command, argument string) (string, json.RawMessage, error) {
	args := map[string]string{}
	switch command {
	case "/bg":
		if argument == "--device" || strings.HasPrefix(argument, "--device ") || strings.HasPrefix(argument, "--device\t") {
			rest := strings.TrimSpace(argument[len("--device"):])
			i := strings.IndexFunc(rest, unicode.IsSpace)
			if i < 1 {
				return "", nil, fmt.Errorf("usage: /bg --device <id> <command>")
			}
			args["device"] = rest[:i]
			argument = strings.TrimSpace(rest[i:])
		}
		args["command"] = argument
	case "/subagent":
		args["task"] = argument
	default:
		return "", nil, fmt.Errorf("unknown command %s", command)
	}
	if argument == "" {
		return "", nil, fmt.Errorf("%s requires an argument", command)
	}
	raw, err := json.Marshal(args)
	return strings.TrimPrefix(command, "/"), raw, err
}

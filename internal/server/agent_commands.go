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
func runAgentConversation(ctx context.Context, sess *agent.Session, text string, images []agent.MessageImage, emit func(agent.Event), steering ...func() *agent.Message) (historyReplaced bool, err error) {
	// Return unexpected execution failures through the normal turn finalizer
	// so persistence, terminal replay and queued work still settle together.
	defer func() {
		if failure := recover(); failure != nil {
			err = fmt.Errorf("internal conversation failure: %v", failure)
		}
	}()
	command, argument := splitAgentCommand(text)
	if len(images) > 0 && (command == "/compact" || command == "/bg" || command == "/subagent") {
		return false, fmt.Errorf("%s does not accept images; remove the attachments or change the message", command)
	}
	if command == "/bg" || command == "/subagent" {
		name, args, err := agentCommandTool(command, argument)
		if err != nil {
			return false, err
		}
		return false, sess.RunToolCommand(ctx, text, name, args, emit)
	}
	if command != "/compact" {
		var takeSteering func() *agent.Message
		if len(steering) > 0 {
			takeSteering = steering[0]
		}
		_, err = sess.RunTurnWithSteering(ctx, text, images, magicTurnEffort(text), emit, takeSteering)
		return false, err
	}
	usage, err := sess.Compact(ctx, argument)
	if err != nil {
		return false, err
	}
	historyReplaced = true
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
		optionEnd := strings.IndexFunc(argument, unicode.IsSpace)
		if optionEnd < 0 {
			optionEnd = len(argument)
		}
		if argument[:optionEnd] == "--device" {
			rest := strings.TrimSpace(argument[optionEnd:])
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

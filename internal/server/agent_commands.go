package server

import (
	"context"
	"strings"
	"unicode"

	"rdev/internal/agent"
)

// runAgentConversation keeps command dispatch separate from turn lifecycle
// management. historyReplaced tells persistence to replace, rather than append,
// even when a summary happens to contain the same number of messages.
func runAgentConversation(ctx context.Context, sess *agent.Session, text string, images []agent.MessageImage, emit func(agent.Event)) (historyReplaced bool, err error) {
	command, argument := splitAgentCommand(text)
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

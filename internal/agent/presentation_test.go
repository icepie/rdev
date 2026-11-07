package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTurnPresentationSurvivesHistoryButNeverReachesProvider(t *testing.T) {
	s := New(Config{})
	s.SetHistory([]Message{{Role: "user", Content: "first"}, {Role: "assistant", Content: "old"}, {Role: "user", Content: "steer"}, {Role: "assistant", Content: "new"}})
	s.AnnotateTurn(2, json.RawMessage(`{"steering":true}`), json.RawMessage(`{"time":1000,"endedAt":9000}`))
	history := s.History()
	if len(history[0].Display) != 0 || string(history[2].Display) != `{"steering":true}` {
		t.Fatalf("wrong turn annotated: %+v", history)
	}
	raw, err := json.Marshal(history)
	if err != nil || strings.Contains(string(raw), "steering") || strings.Contains(string(raw), "endedAt") {
		t.Fatalf("presentation leaked to provider: %s %v", raw, err)
	}
	history[2].Display[0] = 'x'
	if s.History()[2].Display[0] != '{' {
		t.Fatal("history shares mutable metadata")
	}
}

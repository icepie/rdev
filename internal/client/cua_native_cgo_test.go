//go:build x264cgo && cuanative && cgo

package client

import (
	"encoding/json"
	"os"
	"testing"
)

func callNativeCuaForTest(t *testing.T, session *nativeCuaSession, name string, arguments map[string]any) json.RawMessage {
	t.Helper()
	request, err := json.Marshal(map[string]any{
		"action":    "call",
		"name":      name,
		"arguments": arguments,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.request(request)
	if err != nil {
		t.Fatalf("native CUA %s: %v", name, err)
	}
	return result
}

func TestNativeCuaSessionSurvivesToolErrorAndReadsBackCursorState(t *testing.T) {
	if os.Getenv("DISPLAY") == "" {
		t.Skip("requires an X11 display; run under Xvfb")
	}
	session, err := newNativeCuaSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.close()

	callNativeCuaForTest(t, session, "set_agent_cursor_enabled", map[string]any{"enabled": false})
	enabled := callNativeCuaForTest(t, session, "set_agent_cursor_enabled", map[string]any{"enabled": true})
	callNativeCuaForTest(t, session, "set_agent_cursor_motion", map[string]any{"glide_duration_ms": 321.0})

	if _, err := session.request([]byte(`{"action":"call","name":"not_a_real_tool","arguments":{}}`)); err == nil {
		t.Fatal("unknown native CUA tool unexpectedly succeeded")
	}

	stateRaw := callNativeCuaForTest(t, session, "get_agent_cursor_state", map[string]any{})
	var enabledResult, state struct {
		StructuredContent struct {
			Session string `json:"session"`
			Enabled bool   `json:"enabled"`
			Motion  struct {
				GlideDurationMS float64 `json:"glide_duration_ms"`
			} `json:"motion"`
		} `json:"structuredContent"`
	}
	if err := json.Unmarshal(enabled, &enabledResult); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(stateRaw, &state); err != nil {
		t.Fatal(err)
	}
	if !state.StructuredContent.Enabled || state.StructuredContent.Motion.GlideDurationMS != 321 {
		t.Fatalf("cursor state was not retained: %s", stateRaw)
	}
	if state.StructuredContent.Session == "" || state.StructuredContent.Session != enabledResult.StructuredContent.Session {
		t.Fatalf("native CUA session changed after tool error: enabled=%s state=%s", enabled, stateRaw)
	}
}

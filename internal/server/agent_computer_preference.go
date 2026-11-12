package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"rdev/internal/protocol"
)

// The bridge exposes the SDK's portable input schemas (cua-driver-rs v0.47.1).
// Select an adapter before sending any input; errors after selection never retry
// through the legacy backend, including SDK isError/permission refusals.
type computerCuaRequest func(context.Context, any) (json.RawMessage, error)

type computerCuaSchema struct {
	Name  string `json:"name"`
	Input struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	} `json:"inputSchema"`
}

func cuaResultFailed(raw json.RawMessage) bool {
	var result map[string]json.RawMessage
	if json.Unmarshal(raw, &result) != nil || result == nil {
		return true
	}
	var failed bool
	if value := result["isError"]; value != nil {
		if json.Unmarshal(value, &failed) != nil {
			return true
		}
	}
	return failed
}

func cuaSchemaAccepts(schema computerCuaSchema, args map[string]any) bool {
	for key := range args {
		if schema.Input.Properties[key] == nil {
			return false
		}
	}
	for _, key := range schema.Input.Required {
		if _, ok := args[key]; !ok {
			return false
		}
	}
	return true
}

func computerCuaKey(a computerAction, platform string) (string, []string, bool) {
	// press_key has no DOM physical-code/location argument. Do not silently
	// turn a keypad or layout-specific physical key into a different keypress.
	if a.Code != "" && a.Code != a.Key {
		letter := len(a.Key) == 1 && ((a.Key[0] >= 'a' && a.Key[0] <= 'z') || (a.Key[0] >= 'A' && a.Key[0] <= 'Z')) && a.Code == "Key"+strings.ToUpper(a.Key)
		digit := len(a.Key) == 1 && a.Key[0] >= '0' && a.Key[0] <= '9' && a.Code == "Digit"+a.Key
		if !letter && !digit && !(a.Key == " " && a.Code == "Space") {
			return "", nil, false
		}
	}
	key := a.Key
	switch key {
	case "Enter":
		key = "return"
	case "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight":
		key = strings.ToLower(strings.TrimPrefix(key, "Arrow"))
	case " ":
		key = "space"
	default:
		if len(key) != 1 {
			key = strings.ToLower(key)
			switch key {
			case "tab", "escape", "backspace", "delete", "insert", "home", "end", "pageup", "pagedown", "f1", "f2", "f3", "f4", "f5", "f6", "f7", "f8", "f9", "f10", "f11", "f12":
			default:
				return "", nil, false
			}
		} else if key[0] < 32 || key[0] > 126 || key == "+" {
			return "", nil, false
		}
	}
	var modifiers []string
	if a.Ctrl {
		modifiers = append(modifiers, "ctrl")
	}
	if a.Alt {
		modifiers = append(modifiers, "alt")
	}
	if a.Shift {
		modifiers = append(modifiers, "shift")
	}
	if a.Meta {
		meta := "super"
		if platform == "darwin" {
			meta = "cmd"
		}
		if platform == "windows" {
			meta = "win"
		}
		modifiers = append(modifiers, meta)
	}
	return key, modifiers, true
}

// Only source metadata that identifies a primary display at the desktop origin
// can be compared with the SDK's primary-display capture. GPU name-only sources,
// windows and secondary displays cannot safely be translated to this target.
func computerCuaSource(client *ClientConn, source string) *protocol.DesktopSource {
	if client.Desktop == nil {
		return nil
	}
	if source == "" {
		source = "auto"
	}
	for _, s := range client.Desktop.Sources {
		if s.ID == source && s.Primary && (s.Kind == "screen" || s.Kind == "monitor") && s.X == 0 && s.Y == 0 && s.Width > 0 && s.Height > 0 {
			return &s
		}
	}
	return nil
}

func preferComputerCua(ctx context.Context, request computerCuaRequest, client *ClientConn, a computerAction, raw json.RawMessage, width, height int) (string, bool, error) {
	args := map[string]any{"target": map[string]string{"kind": "desktop", "display_id": "primary"}}
	name := a.Action
	pointer := false
	switch a.Action {
	case "key":
		if a.Key == "" {
			return "", true, fmt.Errorf("key is required")
		}
		key, modifiers, ok := computerCuaKey(a, client.OS)
		if !ok {
			return "", false, nil
		}
		name, args["key"] = "press_key", key
		if len(modifiers) > 0 {
			args["modifiers"] = modifiers
		}
	case "text":
		name, args["text"] = "type_text", a.Text
	case "click", "drag", "scroll":
		pointer = true
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return "", true, err
		}
		valid := func(x, y int) bool { return x >= 0 && y >= 0 && x < width && y < height }
		if fields["x"] == nil || fields["y"] == nil || !valid(a.X, a.Y) || a.Button < 0 || a.Button > 2 {
			return "", true, fmt.Errorf("valid screenshot coordinates and button are required")
		}
		if a.Action == "drag" && (fields["toX"] == nil || fields["toY"] == nil || !valid(a.ToX, a.ToY)) {
			return "", true, fmt.Errorf("drag requires valid toX/toY coordinates")
		}
		if computerCuaSource(client, a.Source) == nil {
			return "", false, nil
		}
		if a.Action == "scroll" {
			// The portable SDK accepts whole lines, not arbitrary pixel deltas or
			// two axes in one call. Never split an action into partially retried calls.
			delta, direction := a.DeltaY, "down"
			if a.DeltaX != 0 {
				if a.DeltaY != 0 {
					return "", false, nil
				}
				delta, direction = a.DeltaX, "right"
			}
			if delta == 0 || delta < -2000 || delta > 2000 || delta%120 != 0 {
				return "", false, nil
			}
			if delta < 0 {
				delta = -delta
				if direction == "down" {
					direction = "up"
				} else {
					direction = "left"
				}
			}
			args["direction"], args["by"], args["amount"] = direction, "line", delta/120
		} else {
			args["button"] = []string{"left", "middle", "right"}[a.Button]
		}
		if a.Action == "drag" {
			args["from_x"], args["from_y"], args["to_x"], args["to_y"] = a.X, a.Y, a.ToX, a.ToY
		} else {
			args["x"], args["y"] = a.X, a.Y
		}
		if a.Action == "click" {
			args["delivery_mode"] = "foreground"
		}
	default:
		return "", false, nil
	}
	inventory, err := request(ctx, map[string]any{"action": "tools"})
	if err != nil {
		return "", true, err
	}
	if cuaResultFailed(inventory) {
		return string(inventory), true, fmt.Errorf("native Cua tool discovery failed")
	}
	var list struct {
		Tools []computerCuaSchema `json:"tools"`
	}
	if err := json.Unmarshal(inventory, &list); err != nil || list.Tools == nil {
		return "", true, fmt.Errorf("invalid native Cua tool inventory")
	}
	available, capture := false, false
	for _, tool := range list.Tools {
		if tool.Name == name && cuaSchemaAccepts(tool, args) {
			available = true
		}
		if tool.Name == "get_desktop_state" && cuaSchemaAccepts(tool, map[string]any{}) {
			capture = true
		}
	}
	if !available || (pointer && !capture) {
		return "", false, nil
	}
	if pointer {
		// Reset any SDK screenshot scale left by explicit cua calls and obtain
		// the current native pixel dimensions (including Retina scaling).
		observed, err := request(ctx, map[string]any{"action": "call", "name": "get_desktop_state", "arguments": map[string]any{}})
		if err != nil {
			return "", true, err
		}
		if cuaResultFailed(observed) {
			return string(observed), true, fmt.Errorf("native Cua desktop observation failed")
		}
		var result struct {
			State struct {
				Width        int `json:"screenshot_width"`
				Height       int `json:"screenshot_height"`
				ScreenWidth  int `json:"screen_width"`
				ScreenHeight int `json:"screen_height"`
			} `json:"structuredContent"`
		}
		if err := json.Unmarshal(observed, &result); err != nil {
			return "", true, err
		}
		s, state := computerCuaSource(client, a.Source), result.State
		if state.Width <= 0 || state.Height <= 0 || state.ScreenWidth <= 0 || state.ScreenHeight <= 0 {
			return "", true, fmt.Errorf("native Cua desktop dimensions are missing")
		}
		if s.Width != state.ScreenWidth || s.Height != state.ScreenHeight {
			return "", false, nil
		}
		for _, key := range []string{"x", "from_x", "to_x"} {
			if x, ok := args[key].(int); ok {
				args[key] = float64(x) * float64(state.Width) / float64(width)
			}
		}
		for _, key := range []string{"y", "from_y", "to_y"} {
			if y, ok := args[key].(int); ok {
				args[key] = float64(y) * float64(state.Height) / float64(height)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return "", true, err
	}
	result, err := request(ctx, map[string]any{"action": "call", "name": name, "arguments": args})
	return string(result), true, err
}

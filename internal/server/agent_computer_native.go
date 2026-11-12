package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"rdev/internal/protocol"
)

func (t *computerTool) executeNativeCua(ctx context.Context, client *ClientConn, action computerAction, raw json.RawMessage) (string, error) {
	ctx, finish, err := t.begin(ctx)
	if err != nil {
		return "", err
	}
	defer finish()
	success := false
	defer func() {
		if !success {
			t.srv.clearAutomationDesktop(t.state)
		}
	}()
	if action.Action == "native" {
		screen := t.state.screens[client.ID]
		if screen.client != client || time.Since(screen.at) > 2*time.Minute || (action.Source != "" && action.Source != screen.source) {
			return "", fmt.Errorf("take a fresh computer screenshot before a native CUA call")
		}
		action.Source = screen.source
		screen.at = time.Time{}
		t.state.screens[client.ID] = screen
	}
	if client.Desktop == nil || !client.Desktop.NativeCua {
		return "", fmt.Errorf("native Cua requires a Cua-enabled Go cgo client")
	}
	if err := t.claim(client.ID); err != nil {
		return "", err
	}
	d, err := t.openDesktop(ctx, client, action.Source)
	if err != nil {
		return "", err
	}
	defer func() {
		if !success || !d.request.OnDemand {
			d.close()
			delete(t.state.desktop, client.ID)
		}
	}()
	var request any
	var requestedTool string
	if action.Action == "native_tools" {
		var args struct {
			Tool string `json:"tool"`
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			return "", fmt.Errorf("invalid native Cua tools arguments: %w", err)
		}
		requestedTool = args.Tool
		request = map[string]any{"action": "tools"}
		if requestedTool != "" {
			request = map[string]any{"action": "tools", "tool": requestedTool}
		}
	} else {
		var args struct {
			Tool      string                     `json:"tool"`
			Arguments map[string]json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			return "", fmt.Errorf("invalid native Cua arguments: %w", err)
		}
		if args.Tool == "" || len(args.Tool) > 128 {
			return "", fmt.Errorf("native Cua call requires a tool name from cua action=tools")
		}
		request = map[string]any{"action": "call", "name": args.Tool, "arguments": args.Arguments}
	}
	result, err := d.cuaRequest(ctx, request)
	if err != nil {
		return "", err
	}
	if action.Action == "native_tools" {
		compact, _, discoverErr := cuaToolResult(result, requestedTool)
		if discoverErr != nil {
			return "", discoverErr
		}
		result = compact
	}
	success = !cuaResultFailed(result)
	return string(result), nil
}

func (d *agentDesktopSession) cuaRequest(ctx context.Context, request any) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	if len(encoded) > gpuCuaRequestMax {
		return nil, fmt.Errorf("native Cua request exceeds %d byte limit", gpuCuaRequestMax)
	}
	requestID := generateID()
	if err := d.client.Send(&protocol.Message{Type: protocol.MsgDesktopCua, SessionID: d.id, RequestID: requestID, CuaRequest: encoded}); err != nil {
		return nil, err
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-d.capture.done:
			return nil, fmt.Errorf("device disconnected")
		case msg := <-d.capture.messages:
			if msg.Type != protocol.MsgDesktopCuaResult || msg.RequestID != requestID {
				continue
			}
			if msg.Error != "" {
				return nil, fmt.Errorf("native Cua: %s", msg.Error)
			}
			if len(msg.CuaResult) > gpuCuaResultMax {
				return nil, fmt.Errorf("native Cua result exceeds %d byte limit", gpuCuaResultMax)
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if !json.Valid(msg.CuaResult) {
				return nil, fmt.Errorf("invalid native Cua result")
			}
			return msg.CuaResult, nil
		}
	}
}

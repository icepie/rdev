package server

import (
	"context"
	"encoding/json"
	"fmt"

	"rdev/internal/protocol"
)

func (t *computerTool) executeNativeCua(ctx context.Context, client *ClientConn, action computerAction, raw json.RawMessage) (string, error) {
	ctx, finish, err := t.begin(ctx)
	if err != nil {
		return "", err
	}
	defer finish()
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
	var request any
	if action.Action == "native_tools" {
		request = map[string]any{"action": "tools"}
	} else {
		var args struct {
			Tool      string                     `json:"tool"`
			Arguments map[string]json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			return "", fmt.Errorf("invalid native Cua arguments: %w", err)
		}
		if args.Tool == "" || len(args.Tool) > 128 || !gpuCuaAllowedTool(args.Tool) {
			return "", fmt.Errorf("native Cua tool is not allowed; use native_tools")
		}
		request = map[string]any{"action": "call", "name": args.Tool, "arguments": args.Arguments}
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	if len(encoded) > gpuCuaRequestMax {
		return "", fmt.Errorf("native Cua request exceeds %d byte limit", gpuCuaRequestMax)
	}
	requestID := generateID()
	if err := d.client.Send(&protocol.Message{Type: protocol.MsgDesktopCua, SessionID: d.id, RequestID: requestID, CuaRequest: encoded}); err != nil {
		return "", err
	}
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-d.capture.done:
			return "", fmt.Errorf("device disconnected")
		case msg := <-d.capture.messages:
			if msg.Type != protocol.MsgDesktopCuaResult || msg.RequestID != requestID {
				continue
			}
			if msg.Error != "" {
				return "", fmt.Errorf("native Cua: %s", msg.Error)
			}
			if len(msg.CuaResult) > gpuCuaResultMax {
				return "", fmt.Errorf("native Cua result exceeds %d byte limit", gpuCuaResultMax)
			}
			return string(msg.CuaResult), nil
		}
	}
}

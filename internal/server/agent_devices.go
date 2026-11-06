package server

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"

	"rdev/internal/agent"
)

const agentServerDeviceID = "@server"

var agentRemoteToolNames = []string{"shell", "list_dir", "read_file", "write_file", "edit", "grep", "glob"}

func (s *Server) agentBaseTools(entry *agentSessionEntry, client *ClientConn, cwd string) []agent.Tool {
	if entry.deviceID != agentServerDeviceID {
		return newDeviceTools(s, client, cwd)
	}
	return []agent.Tool{&connectedDevicesTool{srv: s}, &remoteDeviceTool{srv: s}, &todoWriteTool{}, &webFetchTool{}, &webSearchTool{}}
}

// resolveDeviceTool binds each invocation to the current remote connection.
// No host process or filesystem tool is exposed by server mode.
func (s *Server) resolveDeviceTool(device, name string) (agent.Tool, error) {
	if device == "" || device == agentServerDeviceID {
		return nil, fmt.Errorf("a remote device ID is required; server host tools are unavailable")
	}
	if !slices.Contains(agentRemoteToolNames, name) {
		return nil, fmt.Errorf("unknown remote tool %q", name)
	}
	s.mu.RLock()
	client := s.clients[device]
	s.mu.RUnlock()
	if client == nil {
		return nil, fmt.Errorf("device %q is offline", device)
	}
	if s.AdminToken == "" && client.Password != "" {
		return nil, fmt.Errorf("an admin token is required to route tools to password-protected devices")
	}
	for _, tool := range newDeviceTools(s, client, "") {
		if tool.Name() == name {
			return tool, nil
		}
	}
	return nil, fmt.Errorf("unknown device tool %q", name)
}

type connectedDevicesTool struct {
	srv *Server
}

func (*connectedDevicesTool) Name() string { return "list_devices" }
func (*connectedDevicesTool) Description() string {
	return "List currently connected remote devices and their tool schemas. Use device_tool with an explicit device ID to execute a tool. No registration is needed."
}
func (*connectedDevicesTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}
func (t *connectedDevicesTool) Execute(context.Context, json.RawMessage) (string, error) {
	t.srv.mu.RLock()
	clients := make([]*ClientConn, 0, len(t.srv.clients))
	for id, client := range t.srv.clients {
		if id != agentServerDeviceID {
			clients = append(clients, client)
		}
	}
	t.srv.mu.RUnlock()
	sort.Slice(clients, func(i, j int) bool { return clients[i].ID < clients[j].ID })
	devices := make([]map[string]any, 0, len(clients))
	for _, client := range clients {
		tools := []map[string]any{}
		requiresAdmin := t.srv.AdminToken == "" && client.Password != ""
		if !requiresAdmin {
			for _, tool := range newDeviceTools(t.srv, client, "") {
				if slices.Contains(agentRemoteToolNames, tool.Name()) {
					tools = append(tools, map[string]any{"name": tool.Name(), "description": tool.Description(), "parameters": tool.Schema()})
				}
			}
		}
		devices = append(devices, map[string]any{"device": client.ID, "os": client.OS, "online": true, "requiresAdmin": requiresAdmin, "tools": tools})
	}
	return marshalJobResult(devices)
}

type remoteDeviceTool struct {
	srv *Server
}

func (*remoteDeviceTool) Name() string    { return "device_tool" }
func (*remoteDeviceTool) Dangerous() bool { return true }
func (*remoteDeviceTool) Description() string {
	return "Invoke a tool on the explicitly selected connected remote device. Use list_devices for device IDs and argument schemas. Never executes a shell or file operation on the server host."
}
func (*remoteDeviceTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"device":{"type":"string"},"tool":{"type":"string"},"arguments":{"type":"object"}},"required":["device","tool","arguments"],"additionalProperties":false}`)
}
func (*remoteDeviceTool) Summary(raw json.RawMessage) string {
	var args struct{ Device, Tool string }
	_ = json.Unmarshal(raw, &args)
	return args.Device + ": " + args.Tool
}
func (t *remoteDeviceTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Device    string          `json:"device"`
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", err
	}
	tool, err := t.srv.resolveDeviceTool(args.Device, args.Tool)
	if err != nil {
		return "", err
	}
	if len(args.Arguments) == 0 {
		return "", fmt.Errorf("arguments are required")
	}
	return tool.Execute(ctx, args.Arguments)
}

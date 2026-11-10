package server

import (
	"context"
	"encoding/json"
	"testing"

	"rdev/internal/protocol"
)

func TestComputerNativeCuaCorrelatesClientResult(t *testing.T) {
	srv, client, transport := newProxyTestServer(t, "cua-go-device", "")
	client.Desktop = &protocol.DesktopCapabilities{
		Supported: true,
		NativeCua:  true,
		Sources:   []protocol.DesktopSource{{ID: "screen:primary"}},
	}
	tool := &computerTool{automationTool: testAutomation(t, srv, client.ID, "thread")}
	t.Cleanup(func() {
		tool.state.opMu.Lock()
		closeDesktopAutomation(tool.state)
		tool.state.opMu.Unlock()
	})
	transport.onSend = func(msg *protocol.Message) {
		switch msg.Type {
		case protocol.MsgDesktopStart:
			srv.handleDesktopMessage(&protocol.Message{
				Type: protocol.MsgDesktopReady, SessionID: msg.SessionID,
				Width: 1280, Height: 720, Format: "mjpeg",
			})
		case protocol.MsgDesktopCua:
			var request map[string]any
			if err := json.Unmarshal(msg.CuaRequest, &request); err != nil {
				t.Errorf("decode Cua request: %v", err)
				return
			}
			if request["action"] != "tools" {
				t.Errorf("request action = %v, want tools", request["action"])
			}
			srv.handleDesktopMessage(&protocol.Message{
				Type: protocol.MsgDesktopCuaResult, SessionID: msg.SessionID,
				RequestID: msg.RequestID, CuaResult: json.RawMessage(`{"tools":[{"name":"list_apps"}]}`),
			})
		}
	}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"action":"native_tools"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result != `{"tools":[{"name":"list_apps"}]}` {
		t.Fatalf("native tools result = %s", result)
	}
}
